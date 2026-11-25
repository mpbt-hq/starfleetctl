// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

/**
 * A single in-memory coordinator for both OpenCode delivery adapters.
 * It coalesces concurrent inbox reads, de-duplicates startup/live messages,
 * and leases each message to at most one adapter at a time.
 */

/** @param {unknown} message */
export function isSupportedInboxMessage(message) {
  if (!message || typeof message !== 'object') return false;
  const rawType = message.type;
  const type = rawType == null || rawType === '' ? 'ship' : rawType;
  return typeof type === 'string' &&
    ['ship', 'user', 'control', 'message', 'command'].includes(type);
}

export function createInboxCoordinator() {
  /** @type {Map<string, any>} */
  const pending = new Map();
  const submitted = new Set();
  const inFlight = new Set();
  let pollPromise = null;

  /** @param {unknown} messages */
  const add = (messages) => {
    if (!Array.isArray(messages)) return;
    for (const message of messages) {
      const id = message && typeof message.id === 'string' ? message.id : '';
      if (!id.trim() || submitted.has(id)) continue;
      // Reorder to the source inbox's current ordering while retaining the
      // first-seen immutable record (notably the startup message type).
      if (pending.has(id)) {
        const existing = pending.get(id);
        pending.delete(id);
        pending.set(id, existing);
      } else {
        pending.set(id, message);
      }
    }
  };

  const available = () => Array.from(pending.values()).filter((message) =>
    !submitted.has(message.id) && !inFlight.has(message.id)
  );

  return {
    /** @param {unknown} messages */
    seed(messages) {
      add(messages);
    },

    /**
     * @param {() => any | Promise<any>} readInbox
     * @returns {Promise<{response: any, messages: any[]}>}
     */
    async poll(readInbox) {
      if (!pollPromise) {
        pollPromise = Promise.resolve()
          .then(readInbox)
          .then((response) => {
            if (response?.ok && Array.isArray(response.messages)) add(response.messages);
            return response;
          })
          .finally(() => {
            pollPromise = null;
          });
      }

      const response = await pollPromise;
      return { response, messages: available() };
    },

    /** @param {unknown} messages */
    claim(messages) {
      const claimed = [];
      if (!Array.isArray(messages)) return claimed;
      for (const message of messages) {
        const id = message && typeof message.id === 'string' ? message.id : '';
        if (!id.trim() || submitted.has(id) || inFlight.has(id)) continue;
        inFlight.add(id);
        claimed.push(message);
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
