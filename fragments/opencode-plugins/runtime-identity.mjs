// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

// Runtime identity is captured when the plugin module is evaluated. It must
// not depend on chat hooks: OpenCode can restart a process and reuse a session
// without starting a new model turn.
export function createRuntimeIdentity(version, pid, loadedAt) {
  return Object.freeze({ version, pid, loadedAt })
}

export function formatRuntimeIdentity(identity) {
  return `plugin=${identity.version} pid=${identity.pid} loaded_at=${identity.loadedAt}`
}

// Return a receipt exactly once per plugin process, on the first Comms inbox
// delivery. A missing session ID is useful diagnostic evidence, not a reason
// to suppress the receipt.
export function createFirstCommsReporter(identity) {
  let reported = false
  return ({ ship, sessionID, messageIDs }) => {
    if (reported) return null
    reported = true
    const ids = Array.isArray(messageIDs) ? messageIDs.join(',') : ''
    return `runtime-first-comms ${formatRuntimeIdentity(identity)} ship=${ship} sid=${sessionID || '-'} ids=[${ids}]`
  }
}
