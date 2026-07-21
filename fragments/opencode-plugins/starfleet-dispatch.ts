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

// Classify retry errors that need a model switch (not just a restart).
// zen-ratelimit: quota exhausted / rate limited
// nim-overload: provider overloaded
// model-gone: model no longer exists / deprecated / not found
function isRecoverableError(detail: string): boolean {
  return /quota|rate.?limit|429|too many|throttl|Free usage exceeded|subscribe|subscription|free usage/i.test(detail) ||
    /nim.*overload|overload/i.test(detail) ||
    /model.{0,20}(not found|missing|unavailable|deprecated|does not exist)|unknown model|invalid model|no such model/i.test(detail)
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
  let hasSwitchedToFallback = false

  // Model-error retry detection: opencode does NOT surface quota/rate-limit
  // failures as a `session.error` event — it parks the session in a `retry`
  // status with a human-readable message instead. Poll that status so the
  // fleet can see and react to transient model-API faults (zen-ratelimit,
  // nim-overload, etc.).
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
    // status returns { data: { [sessionID]: SessionStatus }, request, response }
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
    bus({ cmd: 'error', detail, ship: aid(), pid: process.pid })

    // Auto-recovery: switch to fallback model on zen-ratelimit / nim-overload.
    if (isRecoverableError(detail) && FALLBACK_MODEL && !hasSwitchedToFallback) {
      hasSwitchedToFallback = true
      const msg = `MODEL RECOVERY: switching to ${FALLBACK_MODEL} (was: ${detail})`
      client.app.log({ body: { service: 'starfleet-dispatch', level: 'warn', message: msg } }).catch(() => {})
      tickLog(msg)
      toast('warning', 'starfleet-dispatch', msg, 8000)
      try {
        await client.session.switchModel({ path: { id: currentSessionID }, body: { model: FALLBACK_MODEL } })
        client.app.log({ body: { service: 'starfleet-dispatch', level: 'info', message: `switchModel to ${FALLBACK_MODEL} succeeded` } }).catch(() => {})
        tickLog(`MODEL RECOVERY: switchModel ok → ${FALLBACK_MODEL}`)
        await client.session.promptAsync({
          path: { id: currentSessionID },
          body: { parts: [{ type: 'text', text: 'Please continue.', synthetic: true }] },
        })
        tickLog(`MODEL RECOVERY: promptAsync sent`)
      } catch (e) {
        const emsg = `MODEL RECOVERY failed: ${String(e).slice(0, 120)}`
        client.app.log({ body: { service: 'starfleet-dispatch', level: 'error', message: emsg } }).catch(() => {})
        tickLog(emsg)
        hasSwitchedToFallback = false
      }
    } else if (isRecoverableError(detail) && hasSwitchedToFallback) {
      tickLog(`MODEL RECOVERY: fallback ${FALLBACK_MODEL} also failed — both models blocked`)
    }
  }

  const retryPollTimer = setInterval(pollRetryStatus, RETRY_POLL_MS)

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
        hasSwitchedToFallback = false
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
        hasSwitchedToFallback = false
        bus({ cmd: 'health', model_last_action: new Date().toISOString(), state: 'working', pid: process.pid })
      }
      if (event.type === 'message.updated') {
        const info = event.properties?.info as any
        if (info?.role === 'assistant' && info?.modelID) {
          currentModel = { model: info.modelID, server: info.providerID }
          if (hasSwitchedToFallback) {
            tickLog(`MODEL RECOVERY: session recovered on ${info.modelID} — fallback worked`)
            hasSwitchedToFallback = false
          }
          bus({ cmd: 'health', state: 'working', error_tag: undefined, plugin_last_run: new Date().toISOString() })
        }
      }
      if (event.type === 'session.error') {
        const detail =
          (event.properties?.error as { message?: string; code?: string })?.message ||
          (event.properties?.error as { code?: string })?.code ||
          'unknown error'
        bus({ cmd: 'error', detail, ship: aid(), pid: process.pid })

        // Recovery: switch to fallback model on model errors (not just retry status).
        // Handles cases where the model doesn't exist at all (deprecated, removed)
        // or returns a hard error instead of parking in retry status.
        if (isRecoverableError(detail) && FALLBACK_MODEL && !hasSwitchedToFallback && currentSessionID) {
          hasSwitchedToFallback = true
          const msg = `MODEL RECOVERY (session.error): switching to ${FALLBACK_MODEL} (was: ${detail})`
          client.app.log({ body: { service: 'starfleet-dispatch', level: 'warn', message: msg } }).catch(() => {})
          tickLog(msg)
          toast('warning', 'starfleet-dispatch', msg, 8000)
          try {
            await client.session.switchModel({ path: { id: currentSessionID }, body: { model: FALLBACK_MODEL } })
            client.app.log({ body: { service: 'starfleet-dispatch', level: 'info', message: `switchModel to ${FALLBACK_MODEL} succeeded` } }).catch(() => {})
            tickLog(`MODEL RECOVERY: switchModel ok → ${FALLBACK_MODEL}`)
            await client.session.promptAsync({
              path: { id: currentSessionID },
              body: { parts: [{ type: 'text', text: 'Please continue.', synthetic: true }] },
            })
            tickLog(`MODEL RECOVERY: promptAsync sent`)
          } catch (e) {
            const emsg = `MODEL RECOVERY failed: ${String(e).slice(0, 120)}`
            client.app.log({ body: { service: 'starfleet-dispatch', level: 'error', message: emsg } }).catch(() => {})
            tickLog(emsg)
            hasSwitchedToFallback = false
          }
        }
      }
    },
  }
}
