// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

import test from 'node:test'
import assert from 'node:assert/strict'
import { normalizeInboxResponse } from './inbox-response.mjs'

test('accepts a successful inbox with messages', () => {
  const messages = [{ id: 'm1', target: 'Discovery' }]
  const result = normalizeInboxResponse({ ok: true, messages })
  assert.equal(result.ok, true)
  assert.deepEqual(result.messages, messages)
})

test('accepts a successful empty inbox', () => {
  assert.deepEqual(normalizeInboxResponse({ ok: true }), {
    ok: true, error: '', messages: [],
  })
})

test('does not turn a dispatch failure into an empty inbox', () => {
  assert.deepEqual(normalizeInboxResponse({ ok: false, error: 'CLI failed' }), {
    ok: false, error: 'CLI failed', messages: [],
  })
})

test('rejects malformed message collections', () => {
  const result = normalizeInboxResponse({ ok: true, messages: {} })
  assert.equal(result.ok, false)
  assert.match(result.error, /not an array/)
})

test('rejects missing and non-object responses', () => {
  assert.equal(normalizeInboxResponse(null).ok, false)
  assert.equal(normalizeInboxResponse('bad').ok, false)
})
