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
  if (raw.events !== undefined && !Array.isArray(raw.events)) {
    return { ok: false, error: 'events must be an array', protocol_version: 1, events: [] };
  }
  return {
    ok: true,
    error: '',
    protocol_version: 1,
    events: Array.isArray(raw.events) ? raw.events : [],
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
        pending.set(id, existing);
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
      return { response, events: available() };
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
  };
}
