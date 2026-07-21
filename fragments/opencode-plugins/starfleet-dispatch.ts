// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Auto-installed by `starfleetctl bootstrap --fix` from
// github.com/mpbt-hq/starfleetctl (fragments/opencode-plugins/).
// Do NOT hand-edit — changes are overwritten on the next bootstrap.
// Edit the canonical copy in the starfleetctl repo instead.

import { execSync } from 'node:child_process'
import { appendFileSync, mkdirSync } from 'node:fs'

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
let FALLBACK_MODEL = ''
function loadConfig(): void {
  const r = bus({ cmd: 'config' })
  if (r.ok) { HEARTBEAT_MS = r.heartbeat_ms; POLL_MS = r.poll_ms; FALLBACK_MODEL = r.fallback_model || '' }
}

// Log-monitoring: detect errors that opencode doesn't surface via session.error
// or retry status (e.g. ResourceExhausted stream errors). Reads the tail of
// opencode.log and checks for error patterns.
const LOG_PATH = (typeof process !== 'undefined' && process.env.HOME || '/root') +
  '/.local/share/opencode/log/opencode.log'
let lastLogErrorSeen = ''

function checkLogForErrors(): string | null {
  try {
    const out = execSync(
      `tail -80 "${LOG_PATH}" 2>/dev/null`,
      { cwd: ROOT, timeout: 3000, stdio: ['pipe', 'pipe', 'ignore'] }
    ).toString()
    // Match "stream error" lines with error details
    const streamErrRe = /level=ERROR.*stream error.*error\.error="([^"]+)"/g
    let match: RegExpExecArray | null
    let latest = ''
    while ((match = streamErrRe.exec(out)) !== null) {
      latest = match[1]
    }
    if (latest && latest !== lastLogErrorSeen) {
      lastLogErrorSeen = latest
      return latest
    }
  } catch { /* ignore */ }
  return null
}

function aid(): string {
  return process.env.STARFLEET_SHIP_ID || 'default'
}

// Reliable, TUI-independent tick log: appends a line per poll so the operator
// can `tail -f` it (client.app.log only lands in opencode.log; client.tui.toast
// is unreliable in background/detached ship mode).
const TICK_DIR = ROOT + '/.starfleet-ai/var/agent-bus/poll'
mkdirSync(TICK_DIR, { recursive: true })
function tickLog(line: string): void {
  try { appendFileSync(TICK_DIR + '/' + aid() + '.tick', new Date().toISOString() + ' ' + line + '\n') } catch { /* ignore */ }
}

// Visible TUI toast (best-effort; may be a no-op in detached mode).
function toast(variant: string, title: string, message: string, duration = 2500): void {
  try {
    const t: any = (client as any).tui
    t.showToast({ body: { variant: variant as any, title, message, duration } })
  } catch { /* tui not ready / unavailable */ }
}

// Execute a policy action returned by starfleetctl error-handle.
// This is the ONLY place recovery actions are performed — the plugin is a
// thin detector + executor, all policy logic lives in the Go binary.
async function executeAction(
  action: string, targetModel: string, detail: string,
  client: any, sessionID: string, hasSwitched: { v: boolean },
): Promise<void> {
  const src = `[action=${action}]`
  if (action === 'ignore') {
    tickLog(`ERROR-HANDLE ${src}: ignoring "${detail}"`)
    return
  }
  if (action === 'retry') {
    tickLog(`ERROR-HANDLE ${src}: re-prompting (detail: ${detail})`)
    try {
      await client.session.promptAsync({
        path: { id: sessionID },
        body: { parts: [{ type: 'text', text: 'Please continue.', synthetic: true }] },
      })
      tickLog(`ERROR-HANDLE ${src}: promptAsync sent`)
    } catch (e) {
      tickLog(`ERROR-HANDLE ${src}: promptAsync failed: ${String(e).slice(0, 120)}`)
    }
    return
  }
  if (action === 'switch-model') {
    if (!targetModel || hasSwitched.v) {
      tickLog(`ERROR-HANDLE ${src}: switch-model requested but ${!targetModel ? 'no target' : 'already switched'} — falling back to retry`)
      try {
        await client.session.promptAsync({
          path: { id: sessionID },
          body: { parts: [{ type: 'text', text: 'Please continue.', synthetic: true }] },
        })
      } catch { /* ignore */ }
      return
    }
    hasSwitched.v = true
    const msg = `ERROR-HANDLE ${src}: switching to ${targetModel} (was: ${detail})`
    client.app.log({ body: { service: 'starfleet-dispatch', level: 'warn', message: msg } }).catch(() => {})
    tickLog(msg)
    toast('warning', 'starfleet-dispatch', msg, 8000)
    try {
      await client.session.switchModel({ path: { id: sessionID }, body: { model: targetModel } })
      tickLog(`ERROR-HANDLE ${src}: switchModel ok → ${targetModel}`)
      await client.session.promptAsync({
        path: { id: sessionID },
        body: { parts: [{ type: 'text', text: 'Please continue.', synthetic: true }] },
      })
      tickLog(`ERROR-HANDLE ${src}: promptAsync sent`)
    } catch (e) {
      const emsg = `ERROR-HANDLE ${src}: failed: ${String(e).slice(0, 120)}`
      client.app.log({ body: { service: 'starfleet-dispatch', level: 'error', message: emsg } }).catch(() => {})
      tickLog(emsg)
      hasSwitched.v = false
    }
    return
  }
  tickLog(`ERROR-HANDLE ${src}: unknown action "${action}" — ignoring`)
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
  const hasSwitchedToFallback = { v: false }

  // Model-error retry detection: opencode does NOT surface quota/rate-limit
  // failures as a `session.error` event — it parks the session in a `retry`
  // status with a human-readable message instead. Poll that status so the
  // fleet can see and react to transient model-API faults.
  let lastRetryDetail = ''
  let retryCooldownUntil = 0
  const RETRY_POLL_MS = 15000
  const RETRY_COOLDOWN_MS = 5 * 60 * 1000

  const pollRetryStatus = async () => {
    tickLog(`retry-poll tick sid=${currentSessionID || '(empty)'}`)
    toast('info', 'starfleet-dispatch', `retry-poll tick (sid=${currentSessionID || '(empty)'})`, 1500)
    client.app.log({ body: { service: 'starfleet-dispatch', level: 'info', message: `retry-poll tick: sid=${currentSessionID || '(empty)'} hasStatus=${typeof client?.session?.status}` } }).catch(() => {})
    if (!currentSessionID) return
    let status: any
    try {
      status = await client.session.status()
    } catch (e) {
      client.app.log({ body: { service: 'starfleet-dispatch', level: 'warn', message: `retry-poll status() threw: ${String(e).slice(0, 120)}` } }).catch(() => {})
      return
    }
    const body = status?.body ?? status
    client.app.log({ body: { service: 'starfleet-dispatch', level: 'info', message: `retry-poll raw: sid=${currentSessionID} keys=${body && typeof body === 'object' ? Object.keys(body).join(',') : typeof body} sample=${JSON.stringify(body).slice(0, 200)}` } }).catch(() => {})
    if (!body || typeof body !== 'object') return
    const data: any = (body as any).data ?? body
    const st: any = data[currentSessionID] ?? Object.values(data)[0]
    if (!st || st.type !== 'retry') { lastRetryDetail = ''; return }
    const detail =
      st.action?.message || st.action?.reason || st.message ||
      (st.action?.title ? `${st.action.title}: ${st.action.message || ''}` : '') || 'retry'
    if (!detail) return
    const now = Date.now()
    if (detail === lastRetryDetail && now < retryCooldownUntil) return
    lastRetryDetail = detail
    retryCooldownUntil = now + RETRY_COOLDOWN_MS
    client.app.log({ body: { service: 'starfleet-dispatch', level: 'warn', message: `session retry status: ${detail}` } }).catch(() => {})
    tickLog(`MODEL RETRY (quota/zen): ${detail}`)
    toast('warning', 'starfleet-dispatch', `model retry: ${detail}`, 6000)

    // Delegate policy to starfleetctl — plugin just executes.
    const r = bus({
      cmd: 'error-handle', detail, source: 'retry-status',
      ship: aid(), pid: process.pid, current_model: currentModel.model || '',
      session_id: currentSessionID, has_fallback: hasSwitchedToFallback.v,
    })
    if (r.ok && r.action) {
      await executeAction(r.action, r.target_model || '', detail, client, currentSessionID, hasSwitchedToFallback)
    }
  }

  const retryPollTimer = setInterval(pollRetryStatus, RETRY_POLL_MS)

  // Log-monitoring: detect stream errors (e.g. ResourceExhausted) that opencode
  // doesn't surface via session.error or retry status. Runs every 30s.
  const LOG_POLL_MS = 30000
  const logPollTimer = setInterval(async () => {
    if (!currentSessionID) return
    const errDetail = checkLogForErrors()
    if (!errDetail) return
    const msg = `LOG ERROR detected: ${errDetail}`
    client.app.log({ body: { service: 'starfleet-dispatch', level: 'warn', message: msg } }).catch(() => {})
    tickLog(`LOG-MONITOR: ${msg}`)
    toast('warning', 'starfleet-dispatch', msg, 8000)

    // Delegate policy to starfleetctl.
    const r = bus({
      cmd: 'error-handle', detail: errDetail, source: 'log-monitor',
      ship: aid(), pid: process.pid, current_model: currentModel.model || '',
      session_id: currentSessionID, has_fallback: hasSwitchedToFallback.v,
    })
    if (r.ok && r.action) {
      await executeAction(r.action, r.target_model || '', errDetail, client, currentSessionID, hasSwitchedToFallback)
    }
  }, LOG_POLL_MS)

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
    clearInterval(retryPollTimer)
    clearInterval(logPollTimer)
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
        hasSwitchedToFallback.v = false
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
        hasSwitchedToFallback.v = false
        bus({ cmd: 'health', model_last_action: new Date().toISOString(), state: 'working', pid: process.pid })
      }
      if (event.type === 'message.updated') {
        const info = event.properties?.info as any
        if (info?.role === 'assistant' && info?.modelID) {
          currentModel = { model: info.modelID, server: info.providerID }
          if (hasSwitchedToFallback.v) {
            tickLog(`MODEL RECOVERY: session recovered on ${info.modelID} — fallback worked`)
            hasSwitchedToFallback.v = false
          }
          bus({ cmd: 'health', state: 'working', error_tag: undefined, plugin_last_run: new Date().toISOString() })
        }
      }
      if (event.type === 'session.error') {
        const detail =
          (event.properties?.error as { message?: string; code?: string })?.message ||
          (event.properties?.error as { code?: string })?.code ||
          'unknown error'

        // Delegate policy to starfleetctl — plugin just executes.
        const r = bus({
          cmd: 'error-handle', detail, source: 'session.error',
          ship: aid(), pid: process.pid, current_model: currentModel.model || '',
          session_id: currentSessionID, has_fallback: hasSwitchedToFallback.v,
        })
        if (r.ok && r.action) {
          await executeAction(r.action, r.target_model || '', detail, client, currentSessionID, hasSwitchedToFallback)
        }
      }
    },
  }
}
