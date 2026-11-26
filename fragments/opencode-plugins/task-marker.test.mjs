// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

import { readFileSync } from 'node:fs'
import { test } from 'node:test'
import assert from 'node:assert/strict'

const plugin = readFileSync(new URL('./starfleet-dispatch.ts', import.meta.url), 'utf8')

test('Dashboard-Topic markers are references, not automatic task adoption', () => {
  assert.doesNotMatch(plugin, /Dashboard-Topic/)
  assert.doesNotMatch(plugin, /task-adopt/)
  assert.doesNotMatch(plugin, /currentTask/)
  assert.match(plugin, /case 'task':/)
})

test('plugin heartbeat cannot overwrite explicit task lifecycle state', () => {
  const heartbeat = plugin.match(/const heartbeatTimer = setInterval\(\(\) => \{([\s\S]*?)\n  \}, HEARTBEAT_MS\)/)
  assert.ok(heartbeat, 'heartbeat handler must exist')
  assert.doesNotMatch(heartbeat[1], /\bstate\s*:/)
  assert.doesNotMatch(heartbeat[1], /\btask\s*:/)
})

test('plugin lifecycle events preserve task status instead of inferring it', () => {
  assert.match(plugin, /cmd: 'health', plugin_last_run:/)
  assert.doesNotMatch(plugin, /reset: true/)
  assert.doesNotMatch(plugin, /state: 'idle'/)
  assert.doesNotMatch(plugin, /state: 'working'/)
})
