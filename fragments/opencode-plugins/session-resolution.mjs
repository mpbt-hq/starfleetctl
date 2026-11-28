// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

// Recover the most recently updated session for a ship in the expected workspace.
// OpenCode's Session schema stores timestamps under time.updated. Accept the
// legacy top-level updated shape too, so older SDK wrappers remain compatible.
// SDK response wrappers have appeared as either {body: ...} or direct data.
export function resolveLatestShipSession(response, ship, root) {
  const body = response?.body ?? response
  const data = body?.data ?? body
  const sessions = Array.isArray(data)
    ? data
    : Array.isArray(data?.sessions)
      ? data.sessions
      : data && typeof data === 'object'
        ? Object.values(data)
        : []
  const matches = sessions.filter(session =>
    session?.id &&
    session.title === ship &&
    (!session.directory || session.directory === root)
  )
  const updated = matches.map(session =>
    Number.isFinite(session?.time?.updated) ? session.time.updated : session?.updated
  )
  if (matches.length === 0 || updated.some(value => !Number.isFinite(value))) {
    return ''
  }

  const latestUpdated = Math.max(...updated)
  const latest = matches.filter((session, index) => updated[index] === latestUpdated)
  return latest.length === 1 ? latest[0].id : ''
}

 
// Select a status entry only when its session identity is unambiguous. OpenCode
// has returned both keyed maps and arrays; never fall back to the first of
// several sessions when the requested ID is absent.
export function selectSessionStatusEntry(response, sessionID) {
  const body = response?.body ?? response
  const data = body?.data ?? body
  const entries = Array.isArray(data?.sessions) ? data.sessions : data
  if (Array.isArray(entries)) {
    if (sessionID) {
      const matches = entries.filter(entry => entry?.id === sessionID)
      return matches.length === 1 ? matches[0] : undefined
    }
    return entries.length === 1 ? entries[0] : undefined
  }
  if (!entries || typeof entries !== 'object') return undefined
  if (sessionID && Object.prototype.hasOwnProperty.call(entries, sessionID)) {
    return entries[sessionID]
  }
  // Some SDK versions return the single status object directly rather than
  // a session-ID keyed map. Accept that shape only when its status fields are
  // on the object itself; never unwrap an unrelated map entry by position.
  if ('model' in entries || 'modelID' in entries || 'providerID' in entries) {
    return entries
  }
  return undefined
}
