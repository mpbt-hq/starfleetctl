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
  ].join('\n') + '\n\n';

  return preparePrompt(usable.map((message) => message.id), prompt, true);
}


/**
 * Prepare all prompt.inject events in their supplied order. The Go side owns
 * the text and separators; this helper only validates and concatenates parts.
 *
 * @param {unknown} events
 * @returns {PreparedPrompt | null}
 */
export function preparePromptEvents(events) {
  if (!Array.isArray(events) || events.length === 0) return null;

  const eventIds = [];
  const parts = [];
  let startTurn = false;

  for (const event of events) {
    if (!event || typeof event !== 'object' || event.type !== 'prompt.inject') {
      return null;
    }
    const prepared = preparePrompt(
      [event.id],
      event.payload?.prompt,
      event.actions?.start_turn,
    );
    if (!prepared) return null;
    eventIds.push(...prepared.eventIds);
    parts.push(...prepared.parts);
    startTurn = startTurn || prepared.startTurn;
  }

  return preparePrompt(eventIds, parts, startTurn);
}


/**
 * Temporary adapter for older CLIs that expose raw Comms messages rather than
 * the versioned event envelope. New binaries perform this classification and
 * formatting in Go.
 *
 * @param {unknown} messages
 * @returns {any[]}
 */
export function legacyInboxToEvents(messages) {
  if (!Array.isArray(messages)) return [];
  return messages.map((message) => {
    const id = typeof message?.id === 'string' ? message.id : '';
    const messageType = typeof message?.type === 'string' && message.type
      ? message.type
      : 'ship';
    const text = typeof message?.text === 'string' ? message.text : '';
    const source = {
      kind: 'comms.message',
      id,
      from: typeof message?.from === 'string' ? message.from : '',
      target: typeof message?.target === 'string' ? message.target : '',
      reply_to: typeof message?.reply_to === 'string' ? message.reply_to : '',
      message_type: messageType,
    };
    const modelSwitch = ['ship', 'user', 'control'].includes(messageType) &&
      text.trim().toLowerCase().startsWith('model ');

    if (messageType === 'command' || modelSwitch) {
      return { id, type: 'command.execute', source, payload: { command: text } };
    }
    if (['ship', 'user', 'control', 'message'].includes(messageType)) {
      const prepared = prepareLegacyInboxPrompt([message]);
      if (prepared) {
        return {
          id,
          type: 'prompt.inject',
          source,
          payload: { prompt: prepared.parts },
          actions: { start_turn: true },
        };
      }
    }
    return { id, type: 'unsupported', source, payload: {} };
  });
}
