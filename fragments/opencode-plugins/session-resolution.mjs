// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

// Recover only a uniquely identifiable ship session in the expected workspace.
// OpenCode SDK response wrappers have appeared as either {body: ...} or direct
// data; accept both without ever choosing an arbitrary session.
export function resolveUniqueShipSession(response, ship, root) {
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
  return matches.length === 1 ? matches[0].id : ''
}
