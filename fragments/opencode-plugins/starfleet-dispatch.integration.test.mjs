// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Exercises the production plugin module against a fake starfleetctl CLI and
// a mocked OpenCode client. Unlike pure-logic tests, this imports and invokes
// the actual plugin hooks and observes the durable event lifecycle calls.

import test from 'node:test'
import assert from 'node:assert/strict'
import {
  chmodSync,
  copyFileSync,
  existsSync,
  mkdirSync,
  mkdtempSync,
  readFileSync,
  rmSync,
  writeFileSync,
} from 'node:fs'
import { tmpdir } from 'node:os'
import { dirname, join } from 'node:path'
import { pathToFileURL, fileURLToPath } from 'node:url'

const SOURCE_DIR = dirname(fileURLToPath(import.meta.url))
const PLUGIN_FILES = [
  'starfleet-dispatch.ts',
  'event-lifecycle.mjs',
  'session-resolution.mjs',
  'inbox-response.mjs',
  'prompt-preparation.mjs',
  'inbox-coordinator.mjs',
  'runtime-identity.mjs',
]

function makeEvent(id) {
  return {
    id,
    type: 'prompt.inject',
    source: { kind: 'comms.message', from: 'Enterprise', message_type: 'ship' },
    payload: { prompt: `directive payload for ${id}` },
    actions: { start_turn: true },
    lifecycle: { state: 'pending', session_ref: '' },
  }
}

function readCalls(path) {
  if (!existsSync(path)) return []
  return readFileSync(path, 'utf8')
    .split('\n')
    .filter(Boolean)
    .map((line) => JSON.parse(line))
}

async function waitFor(predicate, description, timeoutMs = 3000) {
  const deadline = Date.now() + timeoutMs
  while (Date.now() < deadline) {
    if (predicate()) return
    await new Promise((resolve) => setTimeout(resolve, 10))
  }
  assert.fail(`timed out waiting for ${description}`)
}

test('production plugin poll and transform adapters persist the real event lifecycle', async () => {
  const root = mkdtempSync(join(tmpdir(), 'starfleet-plugin-integration-'))
  const pluginDir = join(root, 'fragments', 'opencode-plugins')
  const cliPath = join(root, '.starfleet-ai', 'bin', 'starfleetctl')
  const statePath = join(root, 'state.json')
  const callsPath = join(root, 'calls.jsonl')
  mkdirSync(pluginDir, { recursive: true })
  mkdirSync(dirname(cliPath), { recursive: true })

  for (const file of PLUGIN_FILES) {
    copyFileSync(join(SOURCE_DIR, file), join(pluginDir, file))
  }

  writeFileSync(statePath, JSON.stringify({
    pollError: true,
    events: [makeEvent('evt-retryable-poll')],
  }))
  const fakeCLI = `#!/usr/bin/env node
const fs = require('node:fs')
let input = ''
process.stdin.setEncoding('utf8')
process.stdin.on('data', chunk => { input += chunk })
process.stdin.on('end', () => {
  let request
  try { request = JSON.parse(input) } catch { process.stdout.write(JSON.stringify({ok:false,error:'bad request'})); return }
  fs.appendFileSync(process.env.STARFLEET_TEST_CALLS, JSON.stringify(request) + '\\n')
  const state = JSON.parse(fs.readFileSync(process.env.STARFLEET_TEST_STATE, 'utf8'))
  let response = { ok: true }
  if (request.cmd === 'config') {
    response = { ok: true, heartbeat_ms: 60000, poll_ms: 60000, retry_poll_ms: 60000, log_poll_ms: 60000, log_cooldown_ms: 60000 }
  } else if (request.cmd === 'events.poll') {
    response = state.pollError
      ? { ok: false, error: 'temporary event-store failure', protocol_version: 1, events: [] }
      : { ok: true, protocol_version: 1, events: state.events }
  }
  process.stdout.write(JSON.stringify(response))
})
`
  writeFileSync(cliPath, fakeCLI, { mode: 0o755 })
  chmodSync(cliPath, 0o755)

  const envKeys = [
    'HOME',
    'STARFLEET_SHIP_ID',
    'STARFLEET_ROLE',
    'STARFLEET_TARGET',
    'STARFLEET_TEST_STATE',
    'STARFLEET_TEST_CALLS',
  ]
  const previousEnv = Object.fromEntries(envKeys.map((key) => [key, process.env[key]]))
  const previousCwd = process.cwd()
  const originalSetInterval = globalThis.setInterval
  const originalClearInterval = globalThis.clearInterval
  const intervals = []
  const promptCalls = []
  const promptResults = [
    { ok: true },
    new Error('promptAsync rejected transiently'),
    { ok: true },
  ]

  try {
    process.env.HOME = root
    process.env.STARFLEET_SHIP_ID = 'TestShip'
    process.env.STARFLEET_ROLE = 'ship'
    process.env.STARFLEET_TARGET = 'Enterprise'
    process.env.STARFLEET_TEST_STATE = statePath
    process.env.STARFLEET_TEST_CALLS = callsPath
    process.chdir(root)

    // Capture plugin timers instead of leaving real background pollers running.
    globalThis.setInterval = (fn, delay, ...args) => {
      const timer = { fn, delay, args, active: true }
      intervals.push(timer)
      return timer
    }
    globalThis.clearInterval = (timer) => {
      if (timer) timer.active = false
    }

    const pluginURL = pathToFileURL(join(pluginDir, 'starfleet-dispatch.ts')).href
      + `?integration=${Date.now()}`
    const { plugin } = await import(pluginURL)
    const client = {
      session: {
        async list() {
          return {
            data: [{
              id: 'ses-integration',
              title: 'TestShip',
              directory: root,
              time: { updated: 100 },
            }],
          }
        },
        async status() {
          return { data: { 'ses-integration': { type: 'busy' } } }
        },
        async messages() {
          return { data: [] }
        },
        async promptAsync(params) {
          promptCalls.push(params)
          const next = promptResults.shift()
          if (next instanceof Error) throw next
          return next ?? { ok: true }
        },
        async update() {
          return { ok: true }
        },
      },
      app: { async log() { return { ok: true } } },
      tui: { showToast() {} },
    }
    const hooks = await plugin({ client, $: {} })
    const runtimeCalls = () => readCalls(callsPath).filter((call) => call.cmd === 'runtime.update')
    assert.ok(runtimeCalls().some((call) => call.runtime === 'starting'), 'plugin startup publishes runtime starting')
    assert.ok(readCalls(callsPath).some((call) =>
      call.cmd === 'health' && call.heartbeat_ms === 60000 && call.plugin_last_run,
    ), 'plugin reports its configured heartbeat cadence independently of model activity')
    const pollTimer = intervals.find((timer) => timer.fn.name === 'poll')
    assert.ok(pollTimer, 'production poll timer was installed')
    assert.ok(typeof hooks.event === 'function', 'production event hook was returned')
    assert.ok(typeof hooks['chat.message'] === 'function', 'production chat.message hook was returned')
    assert.ok(typeof hooks['experimental.chat.system.transform'] === 'function', 'production transform hook was returned')

    // A failed event-store read must not be interpreted as an empty inbox or
    // release/ack the event. The initial production poll starts inside plugin().
    await waitFor(
      () => readCalls(callsPath).some((call) => call.cmd === 'events.poll'),
      'initial failed events.poll',
    )
    await new Promise((resolve) => setTimeout(resolve, 20))
    assert.equal(promptCalls.length, 0, 'no prompt is sent after an event-store read failure')
    assert.equal(
      readCalls(callsPath).some((call) => call.cmd === 'events.release'),
      false,
      'a failed poll does not release or acknowledge an event it never claimed',
    )

    const poll = async () => {
      await pollTimer.fn()
    }
    const updateState = (value) => writeFileSync(statePath, JSON.stringify(value))

    // First event: real poll adapter -> actual promptAsync -> durable submitted.
    updateState({ pollError: false, events: [makeEvent('evt-retryable-poll')] })
    await poll()
    assert.equal(promptCalls.length, 1)
    assert.equal(promptCalls[0].path.id, 'ses-integration')
    assert.match(promptCalls[0].body.parts[0].text, /directive payload for evt-retryable-poll/)
    const firstMessageID = promptCalls[0].body.messageID
    assert.ok(firstMessageID, 'plugin supplied deterministic message correlation')
    const firstStates = readCalls(callsPath)
      .filter((call) => call.cmd === 'events.transition' && call.id === 'evt-retryable-poll')
      .map((call) => call.lifecycle_state)
    assert.ok(firstStates.includes('prepared'))
    assert.ok(firstStates.includes('submitted'))

    await hooks.event({
      event: {
        type: 'message.updated',
        properties: {
          info: {
            role: 'assistant',
            sessionID: 'ses-integration',
            parentID: firstMessageID,
            time: { completed: Date.now() },
          },
        },
      },
    })
    const completedStates = readCalls(callsPath)
      .filter((call) => call.cmd === 'events.transition' && call.id === 'evt-retryable-poll')
      .map((call) => call.lifecycle_state)
    assert.ok(completedStates.includes('turn_started'))
    assert.ok(completedStates.includes('completed'), 'only the correlated assistant completion closes the event')
    assert.ok(runtimeCalls().some((call) =>
      call.runtime === 'idle' && call.health === 'healthy' && call.session_id === 'ses-integration',
    ), 'correlated assistant completion publishes idle/healthy runtime state')

    // Second event: a promptAsync error remains retryable and is released.
    updateState({ pollError: false, events: [makeEvent('evt-prompt-retry')] })
    await poll()
    assert.equal(promptCalls.length, 2)
    const retryCalls = readCalls(callsPath)
    assert.ok(retryCalls.some((call) =>
      call.cmd === 'events.transition' &&
      call.id === 'evt-prompt-retry' &&
      call.lifecycle_state === 'failed',
    ))
    assert.ok(retryCalls.some((call) =>
      call.cmd === 'events.release' &&
      Array.isArray(call.ids) &&
      call.ids.includes('evt-prompt-retry'),
    ), 'failed prompt handoff releases the event lease for retry')
    assert.ok(runtimeCalls().some((call) =>
      call.runtime === 'waiting' && call.health === 'degraded' && call.error_tag === 'comms-prompt-handoff',
    ), 'promptAsync rejection publishes waiting/degraded runtime health')

    // The same durable event is then returned by the fake CLI. The actual
    // production poller retries it and records submission after promptAsync.
    updateState({ pollError: false, events: [makeEvent('evt-prompt-retry')] })
    await poll()
    assert.equal(promptCalls.length, 3)
    const secondMessageID = promptCalls[2].body.messageID
    assert.ok(readCalls(callsPath).some((call) =>
      call.cmd === 'events.transition' &&
      call.id === 'evt-prompt-retry' &&
      call.lifecycle_state === 'submitted',
    ))

    await hooks.event({
      event: {
        type: 'message.updated',
        properties: {
          info: {
            role: 'assistant',
            sessionID: 'ses-integration',
            parentID: secondMessageID,
            time: { completed: Date.now() },
          },
        },
      },
    })
    assert.ok(readCalls(callsPath).some((call) =>
      call.cmd === 'events.transition' &&
      call.id === 'evt-prompt-retry' &&
      call.lifecycle_state === 'completed',
    ))

    // Third event: exercise the other real adapter, chat.message ->
    // experimental.chat.system.transform, without duplicating its logic.
    updateState({ pollError: false, events: [makeEvent('evt-transform')] })
    await hooks['chat.message'](
      { sessionID: 'ses-integration' },
      { message: { id: 'user-message-1' }, parts: [] },
    )
    const systemOutput = { system: [] }
    await hooks['experimental.chat.system.transform'](
      { sessionID: 'ses-integration' },
      systemOutput,
    )
    assert.ok(systemOutput.system.some((part) => part.includes('directive payload for evt-transform')))
    const transformStates = readCalls(callsPath)
      .filter((call) => call.cmd === 'events.transition' && call.id === 'evt-transform')
      .map((call) => call.lifecycle_state)
    assert.ok(transformStates.includes('prepared'))
    assert.ok(transformStates.includes('submitted'))
    assert.ok(runtimeCalls().some((call) =>
      call.runtime === 'working' && call.session_id === 'ses-integration',
    ), 'transform adapter publishes runtime working')

    const beforeIdleStatus = runtimeCalls().length
    await hooks.event({
      event: {
        type: 'session.status',
        properties: { sessionID: 'ses-integration', status: { type: 'idle' } },
      },
    })
    assert.equal(runtimeCalls().length, beforeIdleStatus, 'session idle alone is not treated as assistant completion')
    assert.equal(readCalls(callsPath).some((call) =>
      call.cmd === 'events.transition' && call.id === 'evt-transform' && call.lifecycle_state === 'completed',
    ), false, 'session idle does not complete an unrelated Comms event')

    await hooks.event({
      event: {
        type: 'message.updated',
        properties: {
          info: {
            role: 'assistant',
            sessionID: 'ses-integration',
            parentID: 'user-message-1',
            time: { completed: Date.now() },
          },
        },
      },
    })
    const transformCompleted = readCalls(callsPath)
      .filter((call) => call.cmd === 'events.transition' && call.id === 'evt-transform')
      .map((call) => call.lifecycle_state)
    assert.ok(transformCompleted.includes('turn_started'))
    assert.ok(transformCompleted.includes('completed'))
    assert.ok(runtimeCalls().some((call) =>
      call.runtime === 'idle' && call.health === 'healthy' && call.session_id === 'ses-integration',
    ), 'only correlated assistant completion returns runtime to idle/healthy')
  } finally {
    for (const timer of intervals) globalThis.clearInterval(timer)
    globalThis.setInterval = originalSetInterval
    globalThis.clearInterval = originalClearInterval
    process.chdir(previousCwd)
    for (const key of envKeys) {
      if (previousEnv[key] === undefined) delete process.env[key]
      else process.env[key] = previousEnv[key]
    }
    rmSync(root, { recursive: true, force: true })
  }
})
