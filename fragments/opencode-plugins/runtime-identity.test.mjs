// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

import test from 'node:test'
import assert from 'node:assert/strict'
import {
  createFirstCommsReporter,
  createRuntimeIdentity,
  formatRuntimeIdentity,
} from './runtime-identity.mjs'

test('runtime identity reports the version and process that evaluated the plugin module', () => {
  const identity = createRuntimeIdentity('2.5.16', 4321, '2026-11-25T10:00:00.000Z')
  assert.equal(
    formatRuntimeIdentity(identity),
    'plugin=2.5.16 pid=4321 loaded_at=2026-11-25T10:00:00.000Z',
  )
  assert.equal(Object.isFrozen(identity), true)
})

test('first Comms receipt is emitted once even without a session or model turn', () => {
  const identity = createRuntimeIdentity('2.5.16', 4321, '2026-11-25T10:00:00.000Z')
  const report = createFirstCommsReporter(identity)

  assert.equal(
    report({ ship: 'Defiant', sessionID: '', messageIDs: ['m1'] }),
    'runtime-first-comms plugin=2.5.16 pid=4321 loaded_at=2026-11-25T10:00:00.000Z ship=Defiant sid=- ids=[m1]',
  )
  assert.equal(
    report({ ship: 'Defiant', sessionID: 'ses-existing', messageIDs: ['m2'] }),
    null,
  )
})
