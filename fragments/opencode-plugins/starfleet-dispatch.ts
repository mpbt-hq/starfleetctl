// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Auto-installed by `starfleetctl bootstrap --fix` from
// github.com/mpbt-hq/starfleetctl (fragments/opencode-plugins/).
// Do NOT hand-edit — changes are overwritten on the next bootstrap.
// Edit the canonical copy in the starfleetctl repo instead.

import { execSync } from 'node:child_process'

const ROOT = process.cwd()

// Generic JSON-RPC to starfleetctl agent-bus dispatch.
// JSON in via stdin → JSON out. No shell escaping, no text parsing.
function bus(cmd: Record<string, unknown>): any {
  try {
    const raw = execSync(
      `.starfleet-ai/bin/starfleetctl agent-bus dispatch --stdin`,
      { input: JSON.stringify(cmd), cwd: ROOT, timeout: 5000, stdio: ['pipe', 'pipe', 'ignore'] }
    ).toString().trim()
    return JSON.parse(raw)
  } catch { return { ok: false, error: 'cli failed' } }
}

// Fetch tuning knobs from starfleetctl config.
let HEARTBEAT_MS = 0
let POLL_MS = 0
function loadConfig(): void {
  const r = bus({ cmd: 'config' })
  if (r.ok) { HEARTBEAT_MS = r.heartbeat_ms; POLL_MS = r.poll_ms }
}

function aid(): string {
  return process.env.STARFLEET_SHIP_ID || 'default'
}

export const plugin = async ({ client, $ }: any) => {
  loadConfig()

  // Initial health: reset stale + fresh write.
  bus({ cmd: 'health', reset: true, state: 'working', plugin_last_run: new Date().toISOString(), pid: process.pid })

  const heartbeatTimer = setInterval(() => {
    bus({ cmd: 'health', touch: true, plugin_last_run: new Date().toISOString(), ...currentModel })
  }, HEARTBEAT_MS)

  let tuiReady = false
  let sessionNeedsIdentity = true
  let submitted = new Set<string>()
  let turnCount = 0
  let currentModel: { model?: string; server?: string } = {}
  let currentSessionID = ''

  // Init: ack all inbox, load seen, prune stale, set status — one bus call.
  const init = bus({ cmd: 'init', note: 'opencode ship' })
  for (const id of (init.seen || [])) { submitted.add(id) }

  setTimeout(() => {
    if (!tuiReady) {
      tuiReady = true
      client.app.log({ body: { service: 'starfleet-dispatch', level: 'info', message: 'active (fallback)' } }).catch(() => {})
    }
  }, 3000)

  const poll = async () => {
    if (!tuiReady || !currentSessionID) return
    const r = bus({ cmd: 'inbox' })
    const msgs = (r.messages || []).filter((m: any) => !submitted.has(m.id))
    if (msgs.length === 0) return
    for (const msg of msgs) {
      submitted.add(msg.id)
      client.app.log({ body: { service: 'starfleet-dispatch', level: 'info', message: `inbox: [${msg.id}] from ${msg.from}: ${msg.text.slice(0, 80)}` } }).catch(() => {})
      client.tui.showToast({ body: { title: `[fleet] ${msg.id} von ${msg.from}`, message: msg.text, variant: 'info', duration: 10000 } }).catch(() => {})
    }
    try {
      await client.session.promptAsync({
        path: { id: currentSessionID },
        body: {
          parts: [{ type: 'text', text: `(Fleet directive${msgs.length > 1 ? 's' : ''} received)`, synthetic: true }],
        },
      })
    } catch { /* ignore */ }
  }

  const pollTimer = setInterval(poll, POLL_MS)

  // Sync cleanup on process exit (can't await here).
  process.on('exit', () => {
    clearInterval(heartbeatTimer)
    clearInterval(pollTimer)
    try {
      const { execSync } = require('node:child_process')
      execSync(`.starfleet-ai/bin/starfleetctl agent-bus dispatch --stdin`,
        { input: '{"cmd":"exit","note":"process exit"}', cwd: ROOT, timeout: 2000, stdio: ['pipe', 'ignore', 'ignore'] })
    } catch { /* ignore */ }
  })

  return {
    'experimental.chat.system.transform': async (
      _input: any,
      output: { system: string[] },
    ) => {
      turnCount++
      bus({
        cmd: 'health',
        plugin_last_run: new Date().toISOString(),
        model_last_action: turnCount > 1 ? new Date().toISOString() : undefined,
        state: 'working',
        pid: process.pid,
      })
      bus({ cmd: 'status', state: 'working', note: 'opencode ship' })

      // Fleet identity injection.
      const hasIdentity = output.system.some(l => l.includes('--- fleet identity ---'))
      if (sessionNeedsIdentity || !hasIdentity) {
        sessionNeedsIdentity = false
        const shipId = process.env.STARFLEET_SHIP_ID || 'unknown'
        const role = process.env.STARFLEET_ROLE || 'ship'
        const target = process.env.STARFLEET_TARGET || ''
        const parts = [`You are ${role} ${shipId}.`]
        if (target) parts.push(`Report to ${target}.`)
        parts.push('Re-read and follow the agent instructions in agents.d/index.md.')
        output.system.push('', '--- fleet identity ---', ...parts, '--- end fleet identity ---')
      }

      // Fetch inbox and inject new directives.
      const lines: string[] = []
      const r = bus({ cmd: 'inbox' })
      for (const msg of (r.messages || [])) {
        if (submitted.has(msg.id)) continue
        bus({ cmd: 'seen_mark', id: msg.id })
        submitted.add(msg.id)
        lines.push(`Directive ${msg.id} from ${msg.from}:`, msg.text, '')
      }
      if (lines.length > 0) {
        output.system.push(
          '', '--- fleet directives (from other ships via agent-bus) ---',
          'These are directives received from other ships in the fleet.',
          'Process each directive and carry out the requested action.',
          '', ...lines,
          '--- end fleet directives ---',
        )
      }
    },
    event: async ({ event }: { event: { type: string; properties?: Record<string, unknown> } }) => {
      if (event.type === 'session.created') {
        tuiReady = true
        sessionNeedsIdentity = true
        turnCount = 0
        const sessionId = (event.properties?.info as { id?: string })?.id
        if (sessionId) currentSessionID = sessionId
        bus({ cmd: 'health', model_last_action: new Date().toISOString(), state: 'working', pid: process.pid })
        const shipName = process.env.STARFLEET_SHIP_ID
        if (shipName && sessionId) {
          try {
            await client.session.update({ path: { id: sessionId }, body: { title: shipName } })
          } catch { /* ignore */ }
        }
      }
      if (event.type === 'session.cleared' || event.type === 'session.reset') {
        sessionNeedsIdentity = true
        turnCount = 0
        currentModel = {}
        bus({ cmd: 'health', model_last_action: new Date().toISOString(), state: 'working', pid: process.pid })
      }
      if (event.type === 'message.updated') {
        const info = event.properties?.info as any
        if (info?.role === 'assistant' && info?.modelID) {
          currentModel = { model: info.modelID, server: info.providerID }
          bus({ cmd: 'health', state: 'working', error_tag: undefined, plugin_last_run: new Date().toISOString() })
        }
      }
      if (event.type === 'session.error') {
        const detail =
          (event.properties?.error as { message?: string; code?: string })?.message ||
          (event.properties?.error as { code?: string })?.code ||
          'unknown error'
        bus({ cmd: 'error', detail, ship: aid(), pid: process.pid })
      }
    },
  }
}
