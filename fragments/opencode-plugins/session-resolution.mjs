// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

// Recover the most recently updated session for a ship in the expected workspace.
// OpenCode SDK response wrappers have appeared as either {body: ...} or direct
// data; accept both. Older sessions are retained and ignored for recovery.
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
  if (matches.length === 0 || matches.some(session => !Number.isFinite(session.updated))) {
    return ''
  }

  const latestUpdated = Math.max(...matches.map(session => session.updated))
  const latest = matches.filter(session => session.updated === latestUpdated)
  return latest.length === 1 ? latest[0].id : ''
}
