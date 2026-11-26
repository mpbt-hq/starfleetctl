// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

import { createHash } from 'node:crypto'

/**
 * Prefer the session persisted with a lifecycle correlation during restart
 * recovery. The current OpenCode session is only a fallback for new events or
 * legacy records without a session reference.
 * @param {{session_ref?: unknown} | null | undefined} lifecycle
 * @param {string} fallbackSessionID
 */
export function resolveLifecycleSessionID(lifecycle, fallbackSessionID = '') {
  return typeof lifecycle?.session_ref === 'string' && lifecycle.session_ref.trim()
    ? lifecycle.session_ref
    : fallbackSessionID
}

/** @param {string} sessionID @param {string[]} eventIDs */
export function deterministicMessageID(sessionID, eventIDs) {
  if (typeof sessionID !== 'string' || !sessionID.trim()) throw new TypeError('sessionID is required')
  if (!Array.isArray(eventIDs) || eventIDs.length === 0 || !eventIDs.every((id) => typeof id === 'string' && id.trim())) {
    throw new TypeError('eventIDs must be a non-empty array of strings')
  }
  const canonical = [...new Set(eventIDs)].sort().join('\0')
  const digest = createHash('sha256').update('starfleetctl-comms-v1\0').update(sessionID).update('\0').update(canonical).digest('hex')
  return `msg_${digest.slice(0, 32)}`
}

/** @param {{maxAgeMs?: number}} [options] */
export function createTurnCorrelationQueue(options = {}) {
  const maxAgeMs = Number.isFinite(options.maxAgeMs) && options.maxAgeMs > 0 ? options.maxAgeMs : 120000
  const queues = new Map()
  const prune = (now) => {
    for (const [sessionID, queue] of queues) {
      const fresh = queue.filter((entry) => now - entry.createdAt <= maxAgeMs)
      if (fresh.length) queues.set(sessionID, fresh)
      else queues.delete(sessionID)
    }
  }
  return {
    /** @param {string} sessionID @param {string} messageID @param {any} [metadata] @param {number} [now] */
    capture(sessionID, messageID, metadata = {}, now = Date.now()) {
      if (typeof sessionID !== 'string' || !sessionID || typeof messageID !== 'string' || !messageID) return false
      prune(now)
      const queue = queues.get(sessionID) || []
      if (queue.some((entry) => entry.messageID === messageID)) return true
      queue.push({ sessionID, messageID, metadata, createdAt: now })
      queues.set(sessionID, queue)
      return true
    },
    /** @param {string} sessionID @param {number} [now] */
    consume(sessionID, now = Date.now()) {
      prune(now)
      const queue = queues.get(sessionID)
      if (!queue || queue.length === 0) return null
      const entry = queue.shift()
      if (queue.length) queues.set(sessionID, queue)
      else queues.delete(sessionID)
      return entry
    },
    /** @param {number} [now] */
    prune(now = Date.now()) { prune(now) },
  }
}


/** @param {any} info @param {string} expectedSessionID @param {string} expectedMessageID */
export function classifyAssistantUpdate(info, expectedSessionID, expectedMessageID) {
  if (!info || info.role !== 'assistant' || info.sessionID !== expectedSessionID || info.parentID !== expectedMessageID) return 'unrelated'
  if (info.error) return 'failed'
  if (info.time?.completed != null) return 'completed'
  return 'started'
}

/** @param {any} assistantInfo @param {any} sessionStatus @param {string} expectedSessionID @param {string} expectedMessageID */
export function classifyRecoveredTurn(assistantInfo, sessionStatus, expectedSessionID, expectedMessageID) {
  const assistantState = classifyAssistantUpdate(assistantInfo, expectedSessionID, expectedMessageID)
  if (assistantState === 'failed') return 'failed'
  if (assistantState === 'completed') return 'completed'
  // A correlated assistant message is positive evidence that this turn started;
  // an idle status snapshot must not override that stronger message evidence.
  if (assistantState === 'started') return 'active'
  if (sessionStatus?.type === 'idle') return 'failed'
  if (sessionStatus?.type === 'busy' || sessionStatus?.type === 'retry') return 'active'
  return 'unknown'
}

/**
 * Persist the ordered start/completion transitions for a correlated event batch.
 * The callback must be idempotent: a retry may revisit events already completed.
 * @param {unknown} eventIDs
 * @param {(id: string, state: 'turn_started' | 'completed') => boolean} transition
 */
export function completeEventBatch(eventIDs, transition) {
  if (!Array.isArray(eventIDs) || eventIDs.length === 0 || typeof transition !== 'function') return false
  // Reassert submitted before start/completion. This repairs a transient
  // persistence failure after current-turn injection without allowing the
  // adapter to replay an event that is already in the active model turn.
  for (const id of eventIDs) {
    if (typeof id !== 'string' || !id.trim() || !transition(id, 'submitted')) return false
  }
  for (const id of eventIDs) {
    if (!transition(id, 'turn_started')) return false
  }
  for (const id of eventIDs) {
    if (!transition(id, 'completed')) return false
  }
  return true
}
