// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

/**
 * A single in-memory coordinator for both OpenCode delivery adapters.
 * It coalesces concurrent event polls, de-duplicates startup/live events,
 * and leases each event to at most one adapter at a time.
 */

/** @param {unknown} event */
export function isSupportedEvent(event) {
  return !!event && typeof event === 'object' &&
    (event.type === 'prompt.inject' || event.type === 'command.execute');
}

/** @param {unknown} raw */
export function normalizeEventResponse(raw) {
  if (!raw || typeof raw !== 'object') {
    return { ok: false, error: 'dispatch returned no response object', protocol_version: 0, events: [] };
  }
  if (raw.ok !== true) {
    return {
      ok: false,
      error: String(raw.error || 'event poll failed'),
      protocol_version: Number(raw.protocol_version) || 0,
      events: [],
    };
  }
  if (raw.protocol_version !== 1) {
    return {
      ok: false,
      error: `unsupported event protocol version: ${String(raw.protocol_version)}`,
      protocol_version: Number(raw.protocol_version) || 0,
      events: [],
    };
  }
  if (!Array.isArray(raw.events)) {
    return { ok: false, error: 'events must be an array', protocol_version: 1, events: [] };
  }
  return {
    ok: true,
    error: '',
    protocol_version: 1,
    events: raw.events,
  };
}

export function createEventCoordinator() {
  /** @type {Map<string, any>} */
  const pending = new Map();
  const submitted = new Set();
  const inFlight = new Set();
  let pollPromise = null;

  /** @param {unknown} events */
  const add = (events) => {
    if (!Array.isArray(events)) return;
    for (const event of events) {
      const id = event && typeof event.id === 'string' ? event.id : '';
      if (!id.trim() || submitted.has(id)) continue;
      // Reorder to the source event stream's current ordering while retaining the
      // first-seen immutable record (notably the startup event type).
      if (pending.has(id)) {
        const existing = pending.get(id);
        pending.delete(id);
        // Startup init events do not carry lifecycle metadata. Refresh only
        // the mutable lease/correlation fields from the authoritative poll,
        // while retaining the first-seen immutable event payload and order.
        pending.set(id, event.lifecycle ? { ...existing, lifecycle: event.lifecycle } : existing);
      } else {
        pending.set(id, event);
      }
    }
  };

  const available = () => Array.from(pending.values()).filter((event) =>
    !submitted.has(event.id) && !inFlight.has(event.id)
  );

  return {
    /** @param {unknown} events */
    seed(events) {
      add(events);
    },

    /**
     * @param {() => any | Promise<any>} readEvents
     * @returns {Promise<{response: any, events: any[]}>}
     */
    async poll(readEvents) {
      if (!pollPromise) {
        pollPromise = Promise.resolve()
          .then(readEvents)
          .then((response) => {
            if (response?.ok && Array.isArray(response.events)) add(response.events);
            return response;
          })
          .finally(() => {
            pollPromise = null;
          });
      }

      const response = await pollPromise;
      if (response?.ok === true) {
        // A successful response is the authoritative set leased to this
        // consumer. Drop stale startup/poll snapshots that another consumer
        // now owns or that have already been acknowledged. Keep only local
        // in-flight claims until their adapter releases them.
        const leased = new Set(
          Array.isArray(response.events)
            ? response.events.map((event) => event && typeof event.id === 'string' ? event.id : '').filter(Boolean)
            : [],
        );
        for (const id of pending.keys()) {
          if (!leased.has(id) && !inFlight.has(id)) pending.delete(id);
        }
      }
      // Startup-seeded events are not leased. Never dispatch them from a
      // failed poll; retain them internally until a successful poll refreshes
      // their lifecycle metadata and grants this consumer a lease.
      return { response, events: response?.ok === true ? available() : [] };
    },

    /** @param {unknown} events */
    claim(events) {
      const claimed = [];
      if (!Array.isArray(events)) return claimed;
      for (const event of events) {
        const id = event && typeof event.id === 'string' ? event.id : '';
        if (!id.trim() || submitted.has(id) || inFlight.has(id)) continue;
        inFlight.add(id);
        claimed.push(event);
      }
      return claimed;
    },

    /** @param {unknown} ids */
    release(ids) {
      if (!Array.isArray(ids)) return;
      for (const id of ids) if (typeof id === 'string') inFlight.delete(id);
    },

    /** @param {unknown} ids */
    markSubmitted(ids) {
      if (!Array.isArray(ids)) return;
      for (const id of ids) {
        if (typeof id !== 'string' || !id.trim()) continue;
        submitted.add(id);
        pending.delete(id);
        inFlight.delete(id);
      }
    },

    // Forget a failed lifecycle item so a later durable poll can re-add it
    // after its Go-owned retry backoff expires.
    forget(ids) {
      if (!Array.isArray(ids)) return;
      for (const id of ids) {
        if (typeof id !== 'string' || !id.trim()) continue;
        submitted.delete(id);
        pending.delete(id);
        inFlight.delete(id);
      }
    },
  };
}
