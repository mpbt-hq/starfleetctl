// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

/**
 * @typedef {Object} PreparedPrompt
 * @property {string[]} eventIds
 * @property {string[]} parts
 * @property {boolean} startTurn
 */

/**
 * Validate and normalize the generic Comms prompt payload without changing
 * its text. A string is one contiguous part; a string array keeps its order.
 *
 * @param {unknown} eventIds
 * @param {unknown} prompt
 * @param {boolean} [startTurn=false]
 * @returns {PreparedPrompt | null}
 */
export function preparePrompt(eventIds, prompt, startTurn = false) {
  if (
    !Array.isArray(eventIds) ||
    eventIds.length === 0 ||
    !eventIds.every((id) => typeof id === 'string' && id.trim().length > 0)
  ) {
    return null;
  }

  const parts = typeof prompt === 'string'
    ? [prompt]
    : Array.isArray(prompt) && prompt.every((part) => typeof part === 'string')
      ? [...prompt]
      : null;

  if (!parts || parts.length === 0 || !parts.some((part) => part.trim().length > 0)) {
    return null;
  }

  return {
    eventIds: [...eventIds],
    parts,
    startTurn: startTurn === true,
  };
}

/**
 * Temporary compatibility formatter for the old inbox command.
 *
 * Keep all legacy message composition in this one place so both OpenCode
 * adapters receive exactly the same PreparedPrompt. Remove this bridge when
 * the Go event contract returns its complete Comms-formatted prompt.
 *
 * @param {unknown} messages
 * @returns {PreparedPrompt | null}
 */
export function prepareLegacyInboxPrompt(messages) {
  if (!Array.isArray(messages)) return null;

  const usable = messages.filter((message) =>
    message &&
    typeof message === 'object' &&
    typeof message.id === 'string' &&
    message.id.trim().length > 0 &&
    typeof message.from === 'string' &&
    typeof message.text === 'string'
  );

  // Never silently omit one malformed inbox entry while confirming its peers.
  // Keep the entire batch retryable and surface the invalid payload to callers.
  if (usable.length === 0 || usable.length !== messages.length) return null;

  const lines = [];
  for (const message of usable) {
    lines.push(`Directive ${message.id} from ${message.from}:`, message.text, '');
  }

  const prompt = [
    '--- fleet directives (from other ships via comms) ---',
    'Process each directive and carry out the requested action.',
    '',
    ...lines,
    '--- end fleet directives ---',
  ].join('\n');

  return preparePrompt(usable.map((message) => message.id), prompt, true);
}
