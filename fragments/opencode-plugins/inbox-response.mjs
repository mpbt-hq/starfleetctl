// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

// Normalize the JSON-RPC inbox response without turning transport/dispatch
// failures into an apparently empty inbox. Callers can safely retry failures.
export function normalizeInboxResponse(response) {
  if (!response || typeof response !== 'object' || response.ok !== true) {
    const detail = response && typeof response === 'object' && response.error
      ? String(response.error)
      : 'inbox dispatch returned no successful response'
    return { ok: false, error: detail, messages: [] }
  }
  if (response.messages !== undefined && !Array.isArray(response.messages)) {
    return { ok: false, error: 'inbox response messages field is not an array', messages: [] }
  }
  return { ok: true, error: '', messages: response.messages || [] }
}
