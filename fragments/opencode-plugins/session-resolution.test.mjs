// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

import test from 'node:test'
import assert from 'node:assert/strict'
import { resolveUniqueShipSession } from './session-resolution.mjs'

const root = '/workspace/fleet'
const voyager = { id: 'ses-voyager', title: 'Voyager', directory: root }

test('recovers a unique ship session from SDK body wrapper', () => {
  assert.equal(resolveUniqueShipSession({ body: { data: [voyager] } }, 'Voyager', root), 'ses-voyager')
})

test('recovers a unique ship session from direct session list', () => {
  assert.equal(resolveUniqueShipSession({ sessions: [voyager] }, 'Voyager', root), 'ses-voyager')
})

test('refuses duplicate sessions with the same ship title', () => {
  assert.equal(resolveUniqueShipSession([voyager, { ...voyager, id: 'ses-voyager-2' }], 'Voyager', root), '')
})

test('ignores a same-title session from another workspace', () => {
  assert.equal(resolveUniqueShipSession([
    { ...voyager, directory: '/workspace/other' },
    { ...voyager, id: 'ses-voyager-valid' },
  ], 'Voyager', root), 'ses-voyager-valid')
})

test('does not recover a session belonging to another ship', () => {
  assert.equal(resolveUniqueShipSession([voyager], 'Defiant', root), '')
})

test('does not recover when the response contains no sessions', () => {
  assert.equal(resolveUniqueShipSession({ body: { data: [] } }, 'Voyager', root), '')
})
