// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

import test from 'node:test'
import assert from 'node:assert/strict'
import { resolveLatestShipSession } from './session-resolution.mjs'

const root = '/workspace/fleet'
const voyager = { id: 'ses-voyager', title: 'Voyager', directory: root, updated: 100 }

test('recovers a unique ship session from SDK body wrapper', () => {
  assert.equal(resolveLatestShipSession({ body: { data: [voyager] } }, 'Voyager', root), 'ses-voyager')
})

test('recovers a unique ship session from direct session list', () => {
  assert.equal(resolveLatestShipSession({ sessions: [voyager] }, 'Voyager', root), 'ses-voyager')
})

test('selects the most recently updated session and leaves older sessions alone', () => {
  const older = { ...voyager, id: 'ses-voyager-old', updated: 100 }
  const newer = { ...voyager, id: 'ses-voyager-new', updated: 200 }
  assert.equal(resolveLatestShipSession([newer, older], 'Voyager', root), 'ses-voyager-new')
})

test('does not choose arbitrarily when newest timestamps are tied', () => {
  const first = { ...voyager, updated: 200 }
  const second = { ...voyager, id: 'ses-voyager-2', updated: 200 }
  assert.equal(resolveLatestShipSession([first, second], 'Voyager', root), '')
})

test('refuses to guess when a matching session has no valid update timestamp', () => {
  assert.equal(resolveLatestShipSession([
    { ...voyager, updated: 200 },
    { ...voyager, id: 'ses-voyager-unknown', updated: undefined },
  ], 'Voyager', root), '')
})

test('ignores a same-title session from another workspace', () => {
  assert.equal(resolveLatestShipSession([
    { ...voyager, directory: '/workspace/other', updated: 300 },
    { ...voyager, id: 'ses-voyager-valid', updated: 100 },
  ], 'Voyager', root), 'ses-voyager-valid')
})

test('does not recover a session belonging to another ship', () => {
  assert.equal(resolveLatestShipSession([voyager], 'Defiant', root), '')
})

test('does not recover when the response contains no sessions', () => {
  assert.equal(resolveLatestShipSession({ body: { data: [] } }, 'Voyager', root), '')
})
