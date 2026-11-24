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
