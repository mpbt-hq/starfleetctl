// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Auto-installed by `starfleetctl bootstrap --fix` from
// github.com/mpbt-hq/starfleetctl (fragments/opencode-plugins/).
// Do NOT hand-edit — changes are overwritten on the next bootstrap.
// Edit the canonical copy in the starfleetctl repo instead.

const PLUGIN_VERSION = '2.4.1'

import { execSync } from 'node:child_process'

const ROOT = process.cwd()

// Generic JSON-RPC to starfleetctl comms dispatch.
// JSON in via stdin → JSON out. No shell escaping, no text parsing.
function bus(cmd: Record<string, unknown>): any {
  try {
    const raw = execSync(
      `.starfleet-ai/bin/starfleetctl comms dispatch --stdin`,
      { input: JSON.stringify(cmd), cwd: ROOT, timeout: 5000, stdio: ['pipe', 'pipe', 'ignore'] }
    ).toString().trim()
    // starfleetctl may emit "comms: directive ..." lines before the JSON response.
    // Strip non-JSON lines to avoid parse errors.
    const jsonStart = raw.indexOf('{')
    const jsonStr = jsonStart >= 0 ? raw.slice(jsonStart) : raw
    return JSON.parse(jsonStr)
  } catch (e) { return { ok: false, error: `cli failed: ${String(e).slice(0, 200)}` } }
}

// Fetch tuning knobs from starfleetctl config.
let HEARTBEAT_MS = 0
let POLL_MS = 0
let FALLBACK_MODEL = ''
let RETRY_POLL_MS = 2000
let RETRY_COOLDOWN_MS = 10000
let LOG_POLL_MS = 10000
let LOG_COOLDOWN_MS = 10000
function loadConfig(): void {
  const r = bus({ cmd: 'config' })
  if (r.ok) {
    HEARTBEAT_MS = r.heartbeat_ms
    POLL_MS = r.poll_ms
    FALLBACK_MODEL = r.fallback_model || ''
    RETRY_POLL_MS = r.retry_poll_ms || 2000
    RETRY_COOLDOWN_MS = r.retry_cooldown_ms || 10000
    LOG_POLL_MS = r.log_poll_ms || 10000
    LOG_COOLDOWN_MS = r.log_cooldown_ms || 10000
  }
}

// Log-monitoring: detect errors that opencode doesn't surface via session.error
// or retry status (e.g. ResourceExhausted stream errors). Reads the tail of
// opencode.log and checks for error patterns.
const LOG_PATH = (typeof process !== 'undefined' && process.env.HOME || '/root') +
  '/.local/share/opencode/log/opencode.log'
let lastLogErrorSeen = ''

// True quota/rate-limit patterns — NOT transient ResourceExhausted
const QUOTA_PATTERNS = [
  /quota.{0,20}exceed/i,
  /usage.{0,10}limit/i,
  /rate.{0,10}limit/i,
  /out.of.quota/i,
  /429.{0,20}(quota|limit)/i,
  /exceeded.{0,10}(quota|limit|rate)/i,
  /daily.{0,10}limit/i,
  /monthly.{0,10}limit/i,
  /billing.{0,10}limit/i,
]

function checkLogForErrors(): string | null {
  try {
    const out = execSync(
      `tail -80 "${LOG_PATH}" 2>/dev/null`,
      { cwd: ROOT, timeout: 3000, stdio: ['pipe', 'pipe', 'ignore'] }
    ).toString()
    // Match "stream error" lines with error details
    const streamErrRe = /level=ERROR.*stream error.*error\.error="([^"]+)"/g
    // Also match generic "Streaming response failed" or similar stream disconnects
    // First try with the error.error="..." format
    const streamingFailedRe = /level=ERROR.*(?:Streaming response failed|stream interrupted|response stream|connection closed|broken pipe|unexpected eof|stream closed).*error\.error="([^"]+)"/gi
    // Fallback: match the message even without error.error="..."
    const streamingFailedSimpleRe = /level=ERROR.*(?:Streaming response failed|stream interrupted|response stream|connection closed|broken pipe|unexpected eof|stream closed)/gi
    let match: RegExpExecArray | null
    let latest = ''
    while ((match = streamErrRe.exec(out)) !== null) {
      latest = match[1]
    }
    if (!latest) {
      while ((match = streamingFailedRe.exec(out)) !== null) {
        latest = match[1]
      }
    }
    if (!latest && streamingFailedSimpleRe.test(out)) {
      // No detailed error captured, but we know the error type
      latest = 'Streaming response failed'
    }
    if (latest && latest !== lastLogErrorSeen) {
      // Only classify as quota if it matches actual quota patterns
      // Transient ResourceExhausted / stream errors are NOT quota
      const isQuota = QUOTA_PATTERNS.some(re => re.test(latest))
      const tag = isQuota ? 'quota' : 'transient'
      lastLogErrorSeen = latest + '|' + tag
      return latest + '|' + tag
    }
  } catch { /* ignore */ }
  return null
}

function aid(): string {
  return process.env.STARFLEET_SHIP_ID || 'default'
}

// Reliable, TUI-independent tick log: sends to starfleetctl comms dispatch
// so logs are centralized in the events file (can `starfleetctl events` to view).
function tickLog(line: string): void {
  try { bus({ cmd: 'tick', note: line }) } catch { /* ignore */ }
}

// Toast factories (need client & bus from closure)
const toast = (variant: string, title: string, message: string, duration = 2500): void => {
  try {
    const t: any = (client as any).tui
    t.showToast({ body: { variant: variant as any, title, message, duration } })
  } catch { /* tui not ready / unavailable */ }
}

const toastBus = (variant: string, title: string, message: string, duration = 5000): void => {
  toast(variant, title, message, duration)
  try { bus({ cmd: 'toast', variant, title, message, duration }) } catch { /* ignore */ }
}

// Parse and handle fleet messages by type.
// Returns true if the message was handled (should NOT be injected as system prompt).
function handleMessage(
  msg: { id: string; from: string; text: string; type?: string },
  client: any, sessionID: string,
): boolean {
  const text = msg.text.trim()

  // Helper: resolve session ID, trying cache, closure, then fresh discovery
  const resolveSid = async (): Promise<string> =>
    sessionID || currentSessionID || await resolveSessionId() || ''

  // Helper: execute model switch with current model update + bus feedback
  const doSwitchModel = async (sid: string, targetModel: string, src: string) => {
    if (!sid) { tickLog(`${src}: no session ID, can't switch model`); return }
    const clearMethod = client.session.clear || client.session.reset
    const promise = clearMethod
      ? clearMethod({ path: { id: sid } }).then(() => new Promise(r => setTimeout(r, 500)))
      : Promise.resolve()
    promise
      .then(() => client.session.update({ path: { id: sid }, body: { model: targetModel } }))
      .then(() => {
        currentModel.model = targetModel
        tickLog(`${src}: ok → ${targetModel}`)
        toastBus('success', 'starfleet-dispatch', `Model switched to ${targetModel}`, 5000)
        bus({ cmd: 'health', state: 'working', model_last_action: new Date().toISOString(), ...currentModel })
      })
      .catch((e: any) => {
        const emsg = `${src}: failed: ${String(e).slice(0, 120)}`
        tickLog(emsg)
        toastBus('error', 'starfleet-dispatch', emsg, 8000)
      })
  }

  // Check for model-switch directives in ship/user/control messages.
  // Both "setmodel <model>" and "model <model>" are accepted: the former is
  // an explicit directive, the latter is a command-style shorthand that
  // some senders (e.g. McKinley/Enterprise) use via `comms tell` with
  // type="ship" — without this, those messages fall through to the
  // case 'ship' → return false branch and get injected as a directive
  // instead of being executed as a model-switch command.
  if (msg.type === 'ship' || msg.type === 'user' || msg.type === 'control') {
    const lower = text.toLowerCase()
    let prefix = ''
    if (lower.startsWith('setmodel ')) {
      prefix = 'setmodel '
    } else if (lower.startsWith('model ')) {
      prefix = 'model '
    }
    if (prefix) {
      const targetModel = text.slice(prefix.length).trim()
      if (!targetModel) { tickLog(`model-switch from=${msg.from}: missing model name`); return true }
      const src = `[model-switch from=${msg.from}]`
      tickLog(`${src}: switching to ${targetModel}`)
      toastBus('info', 'starfleet-dispatch', `Model switch requested by ${msg.from}: ${targetModel}`, 5000)
      ;(async () => {
        try {
          await doSwitchModel(await resolveSid(), targetModel, src)
        } catch (e) {
          const emsg = `${src}: handler crashed: ${String(e).slice(0, 120)}`
          tickLog(emsg)
          toastBus('error', 'starfleet-dispatch', emsg, 8000)
        }
      })()
      return true
    }
  }

  switch (msg.type || 'ship') {
    // --- directives: inject as system prompt ---
    case 'ship':
    case 'user':
    case 'control':
      return false

    // --- commands: execute locally, never inject ---
    case 'command': {
      const verb = text.split(/\s+/)[0].toLowerCase()
      const args = text.slice(verb.length).trim()

      switch (verb) {
        case 'model': {
          if (!args) { tickLog(`command model from=${msg.from}: missing model name`); return true }
          const src = `[command model from=${msg.from}]`
          tickLog(`${src}: switching to ${args}`)
          toastBus('info', 'starfleet-dispatch', `Model switch requested by ${msg.from}: ${args}`, 5000)
          ;(async () => {
            try {
              await doSwitchModel(await resolveSid(), args, src)
            } catch (e) {
              const emsg = `${src}: handler crashed: ${String(e).slice(0, 120)}`
              tickLog(emsg)
              toastBus('error', 'starfleet-dispatch', emsg, 8000)
            }
          })()
          return true
        }
        case 'quit': {
          const src = `[command quit from=${msg.from}]`
          tickLog(`${src}: shutting down`)
          toastBus('info', 'starfleet-dispatch', `Quit requested by ${msg.from}`, 3000)
          bus({ cmd: 'status', state: 'done', note: `quit requested by ${msg.from}` })
          setTimeout(() => process.exit(0), 500)
          return true
        }
        case 'reset': {
          const src = `[command reset from=${msg.from}]`
          tickLog(`${src}: clearing session`)
          toastBus('info', 'starfleet-dispatch', `Session reset requested by ${msg.from}`, 3000)
          ;(async () => {
            const sid = await resolveSid()
            if (!sid) { tickLog(`${src}: no session ID`); return }
            const clearMethod = client.session.clear || client.session.reset
            if (clearMethod) {
              clearMethod({ path: { id: sid } })
                .then(() => {
                  tickLog(`${src}: ok`)
                  toastBus('success', 'starfleet-dispatch', 'Session cleared', 3000)
                  bus({ cmd: 'health', state: 'working', model_last_action: new Date().toISOString() })
                })
                .catch((e: any) => {
                  const emsg = `${src}: failed: ${String(e).slice(0, 120)}`
                  tickLog(emsg)
                  toastBus('error', 'starfleet-dispatch', emsg, 8000)
                })
            } else {
              tickLog(`${src}: no clear/reset method available`)
              toastBus('error', 'starfleet-dispatch', 'No session clear/reset method', 5000)
            }
          })()
          return true
        }
        case 'status': {
          const src = `[command status from=${msg.from}]`
          tickLog(`${src}: reporting status`)
          bus({ cmd: 'tell', to: msg.from, text: `status: alive, session=${sessionID}, model=${currentModel.model || 'unknown'}` })
          toastBus('info', 'starfleet-dispatch', `Status reported to ${msg.from}`, 3000)
          return true
        }
        case 'abort-retry': {
          // Clear session state and switch model — hard reset for stuck retries
          const target = args || FALLBACK_MODEL
          if (!target) { tickLog(`command abort-retry from=${msg.from}: no model specified, no fallback`); return true }
          const src = `[command abort-retry from=${msg.from}]`
          tickLog(`${src}: clearing session + switching to ${target}`)
          toastBus('warning', 'starfleet-dispatch', `Abort-retry + model switch to ${target}`, 5000)
          ;(async () => {
            const sid = await resolveSid()
            if (!sid) { tickLog(`${src}: no session ID`); return }
            const clearMethod = client.session.clear || client.session.reset
            const promise = clearMethod
              ? clearMethod({ path: { id: sid } }).then(() => new Promise(r => setTimeout(r, 500)))
              : Promise.resolve()
            promise
              .then(() => client.session.update({ path: { id: sid }, body: { model: target } }))
              .then(() => {
                tickLog(`${src}: ok → ${target}`)
                toastBus('success', 'starfleet-dispatch', `Abort-retry + switch to ${target} done`, 5000)
                bus({ cmd: 'health', state: 'working', model_last_action: new Date().toISOString() })
              })
              .catch((e: any) => {
                const emsg = `${src}: failed: ${String(e).slice(0, 120)}`
                tickLog(emsg)
                toastBus('error', 'starfleet-dispatch', emsg, 8000)
              })
          })()
          return true
        }
        default: {
          tickLog(`unknown command from=${msg.from}: ${verb}`)
          return true
        }
      }
    }

    // --- unknown type: log and don't inject ---
    default: {
      tickLog(`unknown message type=${msg.type} from=${msg.from}`)
      return true
    }
  }
}

// Execute a policy action returned by starfleetctl error-handle.
// This is the ONLY place recovery actions are performed — the plugin is a
// thin detector + executor, all policy logic lives in the Go binary.
async function executeAction(
  action: string, targetModel: string, detail: string,
  client: any, sessionID: string, hasSwitched: { v: boolean },
): Promise<void> {
  const src = `[action=${action}]`
  if (!sessionID) { tickLog(`ERROR-HANDLE ${src}: no session ID, skipping`); return }
  if (action === 'ignore') {
    tickLog(`ERROR-HANDLE ${src}: ignoring "${detail}"`)
    return
  }
  if (action === 'retry') {
    // For transient stream/resource errors, clear the session first to
    // properly restart it. Just re-prompting doesn't fix broken streams.
    const isStreamError =
      detail.includes('ResourceExhausted') ||
      detail.includes('Streaming response failed') ||
      detail.includes('stream interrupted') ||
      detail.includes('response stream') ||
      detail.includes('connection closed') ||
      detail.includes('broken pipe') ||
      detail.includes('unexpected eof') ||
      detail.includes('stream closed')

    const promise = Promise.resolve()
    if (isStreamError) {
      tickLog(`ERROR-HANDLE ${src}: clearing session for stream error (detail: ${detail})`)
      const clearMethod = client.session.clear || client.session.reset
      if (clearMethod) {
        promise.then(() => clearMethod({ path: { id: sessionID } }))
          .then(() => tickLog(`ERROR-HANDLE ${src}: session cleared, will re-prompt`))
          .then(() => new Promise(r => setTimeout(r, 500)))
          .catch((e: any) => tickLog(`ERROR-HANDLE ${src}: session.clear failed: ${String(e).slice(0, 120)}`))
      }
    }

    tickLog(`ERROR-HANDLE ${src}: re-prompting (detail: ${detail})`)
    promise
      .then(() => client.session.promptAsync({
        path: { id: sessionID },
        body: { parts: [{ type: 'text', text: 'Please continue.', synthetic: true }] },
      }))
      .then(() => tickLog(`ERROR-HANDLE ${src}: promptAsync sent`))
      .catch((e: any) => tickLog(`ERROR-HANDLE ${src}: promptAsync failed: ${String(e).slice(0, 120)}`))
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
    toastBus('warning', 'starfleet-dispatch', msg, 8000)
    // Use .then() chain like the working 'model' command, not await
    const clearMethod = client.session.clear || client.session.reset
    const promise = clearMethod
      ? clearMethod({ path: { id: sessionID } }).then(() => new Promise(r => setTimeout(r, 500)))
      : Promise.resolve()
    promise
      .then(() => client.session.update({ path: { id: sessionID }, body: { model: targetModel } }))
      .then(() => {
        tickLog(`ERROR-HANDLE ${src}: update ok → ${targetModel}`)
        return client.session.promptAsync({
          path: { id: sessionID },
          body: { parts: [{ type: 'text', text: 'Please continue.', synthetic: true }] },
        })
      })
      .then(() => tickLog(`ERROR-HANDLE ${src}: promptAsync sent`))
      .catch((e: any) => {
        const emsg = `ERROR-HANDLE ${src}: failed: ${String(e).slice(0, 120)}`
        client.app.log({ body: { service: 'starfleet-dispatch', level: 'error', message: emsg } }).catch(() => {})
        tickLog(emsg)
        hasSwitched.v = false
      })
    return
  }
  tickLog(`ERROR-HANDLE ${src}: unknown action "${action}" — ignoring`)
}

export const plugin = async ({ client, $ }: any) => {
  loadConfig()

  // Initial health: reset stale + fresh write.
  bus({ cmd: 'health', reset: true, state: 'working', plugin_last_run: new Date().toISOString(), pid: process.pid, plugin_version: PLUGIN_VERSION })

  const heartbeatTimer = setInterval(() => {
    bus({ cmd: 'health', touch: true, plugin_last_run: new Date().toISOString(), plugin_version: PLUGIN_VERSION, ...currentModel })
  }, HEARTBEAT_MS)

  let tuiReady = false
  let sessionNeedsIdentity = true
  let submitted = new Set<string>()
  let turnCount = 0
  let currentModel: { model?: string; server?: string } = {}
  let currentSessionID = ''
  const hasSwitchedToFallback = { v: false }

  // Toast factories (need client & bus from closure)
  const toast = (variant: string, title: string, message: string, duration = 2500): void => {
    try {
      const t: any = (client as any).tui
      t.showToast({ body: { variant: variant as any, title, message, duration } })
    } catch { /* tui not ready / unavailable */ }
  // Model-error retry detection: opencode does NOT surface quota/rate-limit
  // failures as a `session.error` event — it parks the session in a `retry`
  // status with a human-readable message instead. Poll that status so the
  // fleet can see and react to transient model-API faults.
  let lastRetryDetail = ''
  let retryCooldownUntil = 0

  const pollRetryStatus = async () => {
    tickLog(`retry-poll tick sid=${currentSessionID || '(empty)'}`)
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
    toastBus('warning', 'starfleet-dispatch', `model retry: ${detail}`, 6000)

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
  // doesn't surface via session.error or retry status.
  // Cooldown prevents retry storms when the rate limit is still saturated.
  let logMonitorCooldownUntil = 0
  const logPollTimer = setInterval(async () => {
    if (!currentSessionID) return
    if (Date.now() < logMonitorCooldownUntil) return
    const errDetail = checkLogForErrors()
    if (!errDetail) return
    const msg = `LOG ERROR detected: ${errDetail}`
    client.app.log({ body: { service: 'starfleet-dispatch', level: 'warn', message: msg } }).catch(() => {})
    tickLog(`LOG-MONITOR: ${msg}`)
    toastBus('warning', 'starfleet-dispatch', msg, 8000)

    // Delegate policy to starfleetctl.
    const r = bus({
      cmd: 'error-handle', detail: errDetail, source: 'log-monitor',
      ship: aid(), pid: process.pid, current_model: currentModel.model || '',
      session_id: currentSessionID, has_fallback: hasSwitchedToFallback.v,
    })
    tickLog(`LOG-MONITOR bus: ok=${r.ok} action=${r.action || 'none'} tag=${r.tag || 'none'} err=${r.error || 'none'} detail=${errDetail.slice(0, 60)}`)
    client.app.log({ body: { service: 'starfleet-dispatch', level: 'warn', message: `log-monitor bus result: ${JSON.stringify(r).slice(0, 200)}` } }).catch(() => {})
    if (r.ok && r.action) {
      if (r.action === 'retry') {
        logMonitorCooldownUntil = Date.now() + LOG_COOLDOWN_MS
        tickLog(`LOG-MONITOR: retry cooldown until ${new Date(logMonitorCooldownUntil).toISOString()}`)
      }
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
      resolveSessionId().then(() => {
        if (currentModel.model) {
          bus({ cmd: 'health', state: 'working', ...currentModel })
        }
      })
    }
  }, 3000)

  // Resolve session ID: cache or discover from status
  const resolveSessionId = async (): Promise<string> => {
    if (currentSessionID) return currentSessionID
    try {
      const st = await client.session.status()
      const stBody = st?.body ?? st
      if (stBody && typeof stBody === 'object') {
        const stData = (stBody as any).data ?? stBody
        const keys = Object.keys(stData)
        if (keys.length > 0) {
          currentSessionID = keys[0]
          const stEntry = stData[keys[0]]
          if (!currentModel.model) {
            if (stEntry?.model) currentModel.model = stEntry.model
            if (stEntry?.modelID) currentModel.model = stEntry.modelID
            if (stEntry?.providerID) currentModel.server = stEntry.providerID
          }
          return currentSessionID
        }
      }
    } catch { /* ignore */ }
    return ''
  }

  const poll = async () => {
    if (!tuiReady) return
    if (!currentSessionID) tickLog(`poll: no session yet (will retry)`)
    await resolveSessionId()
    const r = bus({ cmd: 'inbox' })
    const msgs = (r.messages || []).filter((m: any) => !submitted.has(m.id))
    if (msgs.length === 0) return
    const injectable: any[] = []
    for (const msg of msgs) {
      submitted.add(msg.id)
      client.app.log({ body: { service: 'starfleet-dispatch', level: 'info', message: `inbox: [${msg.id}] from=${msg.from} type=${msg.type || 'ship'}: ${msg.text.slice(0, 80)}` } }).catch(() => {})
      // handleMessage: type=command → execute, type=ship/user/control → false (inject)
      if (handleMessage(msg, client, currentSessionID)) continue
      injectable.push(msg)
    }
    // Inject remaining directives mid-turn as synthetic prompt
    if (injectable.length > 0) {
      const lines = injectable.map((m: any) =>
        `Directive ${m.id} from ${m.from}:\n${m.text}`)
      try {
        await client.session.promptAsync({
          path: { id: currentSessionID },
          body: {
            parts: [{
              type: 'text', synthetic: true,
              text: [
                '--- fleet directives (from other ships via comms) ---',
                'Process each directive and carry out the requested action.',
                '',
                ...lines,
                '--- end fleet directives ---',
              ].join('\n'),
            }],
          },
        })
      } catch { /* ignore */ }
    }
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
      execSync(`.starfleet-ai/bin/starfleetctl comms dispatch --stdin`,
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

      // Fetch inbox and inject new directives (skip commands — handled in poll).
      const lines: string[] = []
      const r = bus({ cmd: 'inbox' })
      for (const msg of (r.messages || [])) {
        if (submitted.has(msg.id)) continue
        bus({ cmd: 'seen_mark', id: msg.id })
        submitted.add(msg.id)
        // handleMessage: type=command → execute, type=ship/user/control → inject
        if (currentSessionID && handleMessage(msg, client, currentSessionID)) continue
        lines.push(`Directive ${msg.id} from ${msg.from}:`, msg.text, '')
      }
      if (lines.length > 0) {
        output.system.push(
          '', '--- fleet directives (from other ships via comms) ---',
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

        // Try to read the current model from the session config.
        try {
          const st = await client.session.status()
          const stBody = st?.body ?? st
          if (stBody && typeof stBody === 'object') {
            // opencode session status may contain model/modelID at various depths
            const stData = (stBody as any).data ?? stBody
            const stEntry = sessionId ? (stData[sessionId] ?? Object.values(stData)[0]) : Object.values(stData)[0]
            if (stEntry?.model) currentModel.model = stEntry.model
            if (stEntry?.modelID) currentModel.model = stEntry.modelID
            if (stEntry?.providerID) currentModel.server = stEntry.providerID
          }
        } catch { /* ignore */ }

        bus({ cmd: 'health', model_last_action: new Date().toISOString(), state: 'working', pid: process.pid, ...currentModel })
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
        // opencode's session.error often surfaces a generic "unknown error" for
        // stream/API errors like ResourceExhausted — the real detail is only in
        // the opencode.log (handled by LOG-MONITOR). Only dispatch if we have
        // something real.
        const err = event.properties?.error as any
        const candidate =
          err?.message || err?.code || err?.error ||
          (typeof err === 'string' ? err : '') || ''
        if (!candidate || candidate === 'unknown error') {
          tickLog(`session.error: "${candidate}" — skipping, LOG-MONITOR will handle`)
          return
        }

        // Delegate policy to starfleetctl — plugin just executes.
        const r = bus({
          cmd: 'error-handle', detail: candidate, source: 'session.error',
          ship: aid(), pid: process.pid, current_model: currentModel.model || '',
          session_id: currentSessionID, has_fallback: hasSwitchedToFallback.v,
        })
        if (r.ok && r.action) {
          await executeAction(r.action, r.target_model || '', candidate, client, currentSessionID, hasSwitchedToFallback)
        }
      }
    },
  }
}
