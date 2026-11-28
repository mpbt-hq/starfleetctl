// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

import test from 'node:test'
import assert from 'node:assert/strict'
import { resolveLatestShipSession, selectSessionStatusEntry } from './session-resolution.mjs'

const root = '/workspace/fleet'
const voyager = { id: 'ses-voyager', title: 'Voyager', directory: root, time: { created: 10, updated: 100 } }

test('recovers a unique ship session from the actual OpenCode Session schema', () => {
  assert.equal(resolveLatestShipSession({ body: { data: [voyager] } }, 'Voyager', root), 'ses-voyager')
})

test('recovers a unique ship session from a direct session list', () => {
  assert.equal(resolveLatestShipSession([voyager], 'Voyager', root), 'ses-voyager')
})

test('selects the most recently updated session and leaves older sessions alone', () => {
  const older = { ...voyager, id: 'ses-voyager-old', time: { created: 1, updated: 100 } }
  const newer = { ...voyager, id: 'ses-voyager-new', time: { created: 2, updated: 200 } }
  assert.equal(resolveLatestShipSession([newer, older], 'Voyager', root), 'ses-voyager-new')
})

test('supports legacy top-level updated timestamps', () => {
  const legacy = { id: 'ses-voyager-legacy', title: 'Voyager', directory: root, updated: 100 }
  assert.equal(resolveLatestShipSession([legacy], 'Voyager', root), 'ses-voyager-legacy')
})

test('does not choose arbitrarily when newest timestamps are tied', () => {
  const first = { ...voyager, time: { created: 1, updated: 200 } }
  const second = { ...voyager, id: 'ses-voyager-2', time: { created: 2, updated: 200 } }
  assert.equal(resolveLatestShipSession([first, second], 'Voyager', root), '')
})

test('refuses to guess when a matching session has no valid update timestamp', () => {
  assert.equal(resolveLatestShipSession([
    { ...voyager, time: { created: 1, updated: 200 } },
    { ...voyager, id: 'ses-voyager-unknown', time: { created: 2 } },
  ], 'Voyager', root), '')
})

test('ignores a same-title session from another workspace', () => {
  assert.equal(resolveLatestShipSession([
    { ...voyager, directory: '/workspace/other', time: { created: 1, updated: 300 } },
    { ...voyager, id: 'ses-voyager-valid', time: { created: 2, updated: 100 } },
  ], 'Voyager', root), 'ses-voyager-valid')
})

test('does not recover a session belonging to another ship', () => {
  assert.equal(resolveLatestShipSession([voyager], 'Defiant', root), '')
})

test('does not recover when the response contains no sessions', () => {
  assert.equal(resolveLatestShipSession({ body: { data: [] } }, 'Voyager', root), '')
})

test('selects the exact session status from a keyed multi-session response', () => {
  const wanted = { model: 'model-wanted' }
  const other = { model: 'model-other' }
  assert.equal(selectSessionStatusEntry({ body: { data: { wanted, other } } }, 'wanted'), wanted)
})

test('accepts a direct single-session status object', () => {
  const only = { model: 'only-model', providerID: 'provider-only' }
  assert.equal(selectSessionStatusEntry({ data: only }, 'missing'), only)
})

test('does not unwrap a single unrelated session from a keyed map', () => {
  const other = { model: 'wrong-model', providerID: 'wrong-provider' }
  assert.equal(selectSessionStatusEntry({ data: { other } }, 'missing'), undefined)
})

test('does not guess the first status entry when multiple sessions are present', () => {
  assert.equal(selectSessionStatusEntry({ data: { first: { model: 'wrong' }, second: { model: 'also-wrong' } } }, 'missing'), undefined)
})

test('selects the matching session from an array response', () => {
  const wanted = { id: 'wanted', model: 'model-wanted' }
  const other = { id: 'other', model: 'model-other' }
  assert.equal(selectSessionStatusEntry({ body: { data: [other, wanted] } }, 'wanted'), wanted)
})

test('does not guess from a multi-entry array without a session ID match', () => {
  assert.equal(selectSessionStatusEntry([{ id: 'first' }, { id: 'second' }], 'missing'), undefined)
})
