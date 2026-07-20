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

  // Initial health: delete stale + fresh write.
  bus({ cmd: 'health', delete: true })
  bus({ cmd: 'health', state: 'working', plugin_last_run: new Date().toISOString(), pid: process.pid })

  const heartbeatTimer = setInterval(() => {
    bus({ cmd: 'health', plugin_last_run: new Date().toISOString(), ...currentModel })
    bus({ cmd: 'touch' })
  }, HEARTBEAT_MS)

  let tuiReady = false
  let sessionNeedsIdentity = true
  let submitted = new Set<string>()
  let turnCount = 0
  let currentModel: { model?: string; server?: string } = {}

  // Seed: ack all current inbox so they don't reappear as "unread".
  const inbox = bus({ cmd: 'inbox' })
  for (const msg of (inbox.messages || [])) {
    bus({ cmd: 'ack', id: msg.id, note: 'init-seen' })
  }
  // Seed submitted set with THIS ship's seen messages.
  const seen = bus({ cmd: 'seen_load' })
  for (const id of (seen.seen || [])) { submitted.add(id) }

  bus({ cmd: 'prune' })
  bus({ cmd: 'status', state: 'idle', note: 'opencode ship' })

  setTimeout(() => {
    if (!tuiReady) {
      tuiReady = true
      client.app.log({ body: { service: 'starfleet-dispatch', level: 'info', message: 'active (fallback)' } }).catch(() => {})
    }
  }, 3000)

  const submit = async (text: string) => {
    if (!tuiReady) return false
    try {
      await client.tui.appendPrompt({ body: { text: `\n📨 ${text.slice(0, 200)}` } })
      await client.tui.submitPrompt()
      return true
    } catch { return false }
  }

  const autoPong = (id: string, from: string, text: string) => {
    if (from === 'Enterprise' && /ping/i.test(text)) {
      bus({ cmd: 'ack', id, note: 'auto-pong' })
      bus({ cmd: 'tell', target: 'Enterprise', text: `Pong! (auto-reply to ${id})`, reply_to: id })
    }
  }

  const poll = async () => {
    if (!tuiReady) return
    const r = bus({ cmd: 'inbox' })
    for (const msg of (r.messages || [])) {
      if (submitted.has(msg.id)) continue
      submitted.add(msg.id)
      client.app.log({ body: { service: 'starfleet-dispatch', level: 'info', message: `inbox: [${msg.id}] from ${msg.from}: ${msg.text.slice(0, 80)}` } }).catch(() => {})
      autoPong(msg.id, msg.from, msg.text)
      const ok = await submit(`[${msg.id}] from ${msg.from}: ${msg.text}`)
      if (!ok) submitted.delete(msg.id)
    }
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
        lines.push(`[${msg.id}] from ${msg.from}: ${msg.text}`)
        autoPong(msg.id, msg.from, msg.text)
      }
      if (lines.length > 0) {
        output.system.push('', '--- agent-bus inbox ---', ...lines, '--- end agent-bus inbox ---')
      }
    },
    event: async ({ event }: { event: { type: string; properties?: Record<string, unknown> } }) => {
      if (event.type === 'session.created') {
        tuiReady = true
        sessionNeedsIdentity = true
        turnCount = 0
        bus({ cmd: 'health', model_last_action: new Date().toISOString(), state: 'working', pid: process.pid })
        const shipName = process.env.STARFLEET_SHIP_ID
        if (shipName) {
          try {
            const sessionId = (event.properties?.info as { id?: string })?.id
            if (sessionId) await client.session.update({ path: { id: sessionId }, body: { title: shipName } })
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
