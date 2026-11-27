// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Canonical source in fragments/opencode-plugins/.
// `starfleetctl bootstrap --fix` installs this file as
// .opencode/plugins/starfleet-dispatch.ts. Edit this fragment, never the
// deployed copy under .opencode/plugins.

const PLUGIN_VERSION = '2.5.20'

// Plugin→opencode app-logging switch (writes into opencode.log via
// client.app.log). The per-poll diagnostics (retry-status dumps, inbox
// listings, log-monitor results) bloat opencode.log heavily during normal
// operation, so they are off by default. Flip to true to debug the plugin.
const APP_LOG_ENABLED = false

import { execSync } from 'node:child_process'
import { classifyAssistantUpdate, classifyRecoveredTurn, completeEventBatch, createTurnCorrelationQueue, deterministicMessageID, resolveLifecycleSessionID } from './event-lifecycle.mjs'
import { resolveLatestShipSession } from './session-resolution.mjs'
import { normalizeInboxResponse } from './inbox-response.mjs'
import { legacyInboxToEvents, preparePromptEvents } from './prompt-preparation.mjs'
import { createEventCoordinator, isSupportedEvent, normalizeEventResponse } from './inbox-coordinator.mjs'
import { createFirstCommsReporter, createRuntimeIdentity, formatRuntimeIdentity } from './runtime-identity.mjs'

const ROOT = process.cwd()

// Captured at module evaluation, not at the first model turn. OpenCode may
// reuse an old session after restart without issuing an initial prompt.
const RUNTIME_IDENTITY = createRuntimeIdentity(
  PLUGIN_VERSION,
  process.pid,
  new Date().toISOString(),
)
const reportFirstComms = createFirstCommsReporter(RUNTIME_IDENTITY)

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
// Safe defaults keep inbox polling alive when config lookup fails.
let HEARTBEAT_MS = 300000
let POLL_MS = 3000
let FALLBACK_MODEL = ''
let RETRY_POLL_MS = 2000
let RETRY_COOLDOWN_MS = 10000
let LOG_POLL_MS = 10000
let LOG_COOLDOWN_MS = 10000
function loadConfig(): void {
  const r = bus({ cmd: 'config' })
  if (r.ok) {
    HEARTBEAT_MS = r.heartbeat_ms || 300000
    POLL_MS = r.poll_ms || 3000
    FALLBACK_MODEL = r.fallback_model || ''
    RETRY_POLL_MS = r.retry_poll_ms || 2000
    RETRY_COOLDOWN_MS = r.retry_cooldown_ms || 10000
    LOG_POLL_MS = r.log_poll_ms || 10000
    LOG_COOLDOWN_MS = r.log_cooldown_ms || 10000
  }
}

let lastSessionErrorTime = 0;
let sessionErrorBuffer: string[] = [];
let sessionErrorTimer: any = null;
const SESSION_ERROR_COOLDOWN_MS = 5 * 60 * 1000; // 5 minutes

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

function checkLogForErrors(sessionID: string): string | null {
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
      // Filter: only process errors for our session
      if (!match[0].includes(`session.id=${sessionID}`)) continue
      latest = match[1]
    }
    if (!latest) {
      while ((match = streamingFailedRe.exec(out)) !== null) {
        if (!match[0].includes(`session.id=${sessionID}`)) continue
        latest = match[1]
      }
    }
    if (!latest && streamingFailedSimpleRe.test(out)) {
      // Check if any of the matches are for our session
      const lines = out.split('\n')
      for (const line of lines) {
        if (streamingFailedSimpleRe.test(line) && line.includes(`session.id=${sessionID}`)) {
          latest = 'Streaming response failed'
          break
        }
      }
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

export const plugin = async ({ client, $ }: any) => {
  loadConfig()

  // Startup status is refreshed only after comms init has pruned stale records.

  let tuiReady = false
  let sessionNeedsIdentity = true
  const eventCoordinator = createEventCoordinator()
  const consumerID = `${aid()}:${process.pid}`
  const turnCorrelationQueue = createTurnCorrelationQueue()
  const pendingByMessage = new Map<string, { sessionID: string; eventIDs: string[]; submitted?: boolean; startSeen?: boolean; completionSeen?: boolean }>()
  const syntheticMessageIDs = new Set<string>()
  const lastReconcileAt = new Map<string, number>()
  const unsupportedEventDiagnostics = new Set<string>()

  const transitionEvent = (id: string, state: string, sessionID = '', messageID = '', detail = ''): boolean => {
    const response = bus({ cmd: 'events.transition', id, consumer: consumerID, lifecycle_state: state,
      session_ref: sessionID, message_ref: messageID, lifecycle_error: detail })
    if (!response?.ok) {
      tickLog(`event-lifecycle-fail ship=${aid()} id=${id} state=${state} error=${String(response?.error || 'unknown').slice(0, 160)}`)
      return false
    }
    return true
  }
  const releaseEvents = (ids: string[]): void => {
    if (!ids.length) return
    const response = bus({ cmd: 'events.release', consumer: consumerID, ids })
    if (!response?.ok) tickLog(`event-release-fail ship=${aid()} ids=[${ids.join(',')}] error=${String(response?.error || 'unknown').slice(0, 160)}`)
  }
  const markTurnStarted = (messageID: string, correlation: { sessionID: string; eventIDs: string[]; submitted?: boolean; startSeen?: boolean; completionSeen?: boolean }): boolean => {
    if (!correlation.submitted) { correlation.startSeen = true; return true }
    let accepted = true
    for (const id of correlation.eventIDs) {
      // Reassert submission before start: current-turn injection may already
      // have reached OpenCode even if its first durable submitted write failed.
      if (!transitionEvent(id, 'submitted', correlation.sessionID, messageID)) {
        accepted = false
        continue
      }
      if (!transitionEvent(id, 'turn_started', correlation.sessionID, messageID)) accepted = false
    }
    return accepted
  }
  const completeCorrelation = (messageID: string, correlation: { sessionID: string; eventIDs: string[]; submitted?: boolean; startSeen?: boolean; completionSeen?: boolean }): boolean => {
    if (!correlation.submitted) { correlation.startSeen = true; correlation.completionSeen = true; return false }
    // Completion implies start, but each durable transition can fail transiently.
    // Keep the correlation until every event is durably completed so polling can
    // retry after a CLI/IO error instead of leaving an unacknowledged event stuck.
    const accepted = completeEventBatch(correlation.eventIDs, (id, state) =>
      transitionEvent(id, state, correlation.sessionID, messageID))
    if (accepted) {
      pendingByMessage.delete(messageID)
      lastReconcileAt.delete(messageID)
      // Keep the coordinator's submitted tombstone: a poll already in flight
      // may still return its pre-ack snapshot after this completion callback.
    }
    return accepted
  }
  const reconcileCorrelation = async (sessionID: string, messageID: string, eventIDs: string[]): Promise<'active' | 'completed' | 'failed' | 'unknown'> => {
    if (typeof client.session.messages !== 'function') return 'unknown'
    const now = Date.now()
    if (now - (lastReconcileAt.get(messageID) || 0) < 10000) return 'active'
    lastReconcileAt.set(messageID, now)
    try {
      const response = await client.session.messages({ path: { id: sessionID } })
      const body = response?.body ?? response
      const messages = Array.isArray(body) ? body : Array.isArray(body?.data) ? body.data : []
      const assistant = messages.map((entry: any) => entry?.info ?? entry)
        .find((info: any) => info?.role === 'assistant' && info?.parentID === messageID)
      const correlation = pendingByMessage.get(messageID) || { sessionID, eventIDs, submitted: true }
      // A restart may leave a submitted user message without an active request.
      // Busy is positive turn-start evidence; idle only triggers retry and never
      // completion. The deterministic message ID makes that retry idempotent.
      const statusResponse = await client.session.status()
      const statusBody = statusResponse?.body ?? statusResponse
      const statusData = statusBody?.data ?? statusBody
      const sessionStatus = statusData?.[sessionID]
      const recoveryState = classifyRecoveredTurn(assistant, sessionStatus, sessionID, messageID)
      if (recoveryState === 'completed') {
        return completeCorrelation(messageID, correlation) ? 'completed' : 'unknown'
      }
      if (recoveryState === 'failed') {
        const detail = assistant?.error
          ? String(assistant.error).slice(0, 512)
          : 'submitted turn recovered with idle session and no correlated assistant completion'
        for (const id of eventIDs) transitionEvent(id, 'failed', sessionID, messageID, detail)
        pendingByMessage.delete(messageID)
        lastReconcileAt.delete(messageID)
        eventCoordinator.forget(eventIDs)
        return 'failed'
      }
      if (recoveryState === 'active') {
        markTurnStarted(messageID, correlation)
        return 'active'
      }
      return 'unknown'
    } catch (e) {
      tickLog(`event-reconcile-fail ship=${aid()} sid=${sessionID} message=${messageID} error=${String(e).slice(0, 160)}`)
      return 'unknown'
    }
  }
  let turnCount = 0
  let currentModel: { model?: string; server?: string } = {}
  let currentSessionID = ''
  let sessionResolveDiagnosticLogged = false
  const hasSwitchedToFallback = { v: false }

  // Model-switch helpers.
  // opencode's session.update only accepts {title, time.archived} — a `model`
  // field there is silently dropped (the switch looked successful but did
  // nothing). Real model switches go through session.switchModel with a
  // Model.Ref object {id, providerID, variant?}, matching the DB shape
  // {"id":"...","providerID":"...","variant":"default"}.
  // Strategy names (meta-model virtuals) are NOT special here — they are just
  // another provider/model pair (e.g. "meta-model/heavy-model") from opencode's
  // point of view, so plain provider/model parsing suffices.
  const toModelRef = (target: string): { id: string; providerID: string } => {
    const slash = target.indexOf('/')
    if (slash > 0) return { id: target.slice(slash + 1), providerID: target.slice(0, slash) }
    // Bare model name: same provider as the current session.
    return { id: target, providerID: currentModel.server || 'opencode' }
  }
  const switchSessionModel = async (c: any, sid: string, target: string): Promise<void> => {
    const ref = toModelRef(target)
    if (typeof c.session.switchModel === 'function') {
      await c.session.switchModel({ path: { id: sid }, body: { model: ref } })
      return
    }
    // Fallback for SDKs without switchModel: at least store a well-formed ref.
    await c.session.update({ path: { id: sid }, body: { model: ref } })
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

  // App-log writer (opencode.log): no-op unless APP_LOG_ENABLED is true.
  // Central gate for all diagnostic logging so normal operation stays quiet.
  const appLog = (level: string, message: string): void => {
    if (!APP_LOG_ENABLED) return
    try {
      client.app.log({ body: { service: 'starfleet-dispatch', level, message } }).catch(() => {})
    } catch { /* ignore */ }
  }

  // Parse and handle fleet messages by type.
  // Returns true if the message was handled (should NOT be injected as system prompt).
  // Lives in the plugin closure because it needs client, currentModel,
  // currentSessionID, resolveSessionId and toastBus.
  async function handleMessage(
    msg: { id: string; from: string; text: string; type?: string },
    client: any, sessionID: string,
  ): Promise<boolean> {
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
        .then(() => switchSessionModel(client, sid, targetModel))
        .then(() => {
          currentModel.model = targetModel
          tickLog(`${src}: ok → ${targetModel}`)
          toastBus('success', 'starfleet-dispatch', `Model switched to ${targetModel}`, 5000)
          bus({ cmd: 'health', model_last_action: new Date().toISOString(), ...currentModel })
        })
        .catch((e: any) => {
          const emsg = `${src}: failed: ${String(e).slice(0, 120)}`
          tickLog(emsg)
          toastBus('error', 'starfleet-dispatch', emsg, 8000)
        })
    }

    // Check for model-switch directives in ship/user/control messages.
    // "model <model>" is accepted as a command-style shorthand that
    // some senders (e.g. McKinley/Enterprise) use via `comms tell` with
    // type="ship" — without this, those messages fall through to the
    // case 'ship' → return false branch and get injected as a directive
    // instead of being executed as a model-switch command.
    if (msg.type === 'ship' || msg.type === 'user' || msg.type === 'control') {
      const lower = text.toLowerCase()
      if (lower.startsWith('model ')) {
        const targetModel = text.slice(6).trim()
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
      // External/wormhole imports arrive as type "message" and must be treated
      // as normal injectable directives (never as unknown/already-handled).
      case 'ship':
      case 'user':
      case 'control':
      case 'message': {
        return false
      }

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
          case 'task': {
            // `task <slug>` — report what we're working on; `task clear` /
            // `task done` (or bare `task`) — report back to idle.
            const src = `[command task from=${msg.from}]`
            if (!args || args === 'clear' || args === 'done') {
              bus({ cmd: 'task', task: '', task_clear: true })
              tickLog(`${src}: cleared`)
              toastBus('info', 'starfleet-dispatch', 'Task cleared', 3000)
            } else {
              bus({ cmd: 'task', task: args })
              tickLog(`${src}: → ${args}`)
              toastBus('info', 'starfleet-dispatch', `Task set: ${args}`, 3000)
            }
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
                    bus({ cmd: 'health', model_last_action: new Date().toISOString() })
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
                .then(() => switchSessionModel(client, sid, target))
                .then(() => {
                  tickLog(`${src}: ok → ${target}`)
                  toastBus('success', 'starfleet-dispatch', `Abort-retry + switch to ${target} done`, 5000)
                  bus({ cmd: 'health', model_last_action: new Date().toISOString() })
                })
                .catch((e: any) => {
                  const emsg = `${src}: failed: ${String(e).slice(0, 120)}`
                  tickLog(emsg)
                  toastBus('error', 'starfleet-dispatch', emsg, 8000)
                })
            })()
            return true
          }
          case 'toast': {
            // toast <variant> <title> <message> [duration]
            const parts = args.split(/\s+/)
            if (parts.length < 3) { tickLog(`command toast from=${msg.from}: need variant, title, message`); return true }
            const variant = parts[0]
            const title = parts[1]
            const message = parts.slice(2).join(' ')
            const duration = parts[3] ? parseInt(parts[3], 10) : 5000
            const src = `[command toast from=${msg.from}]`
            tickLog(`${src}: ${variant} "${title}"`)
            bus({ cmd: 'toast', variant, title, message, duration })
            return true
          }
          case 'sop-reload': {
            // Clear session so the ship restarts and reloads the SOP index on next startup
            const src = `[command sop-reload from=${msg.from}]`
            tickLog(`${src}: clearing session for SOP reload`)
            toastBus('info', 'starfleet-dispatch', 'SOP reload requested — session will restart', 5000)
            const sid = await resolveSid()
            if (!sid) { tickLog(`${src}: no session ID`); return true }
            const clearMethod = client.session.clear || client.session.reset
            if (clearMethod) {
              clearMethod({ path: { id: sid } })
                .then(() => {
                  tickLog(`${src}: ok`)
                  toastBus('success', 'starfleet-dispatch', 'Session cleared — SOP index will reload on restart', 5000)
                  bus({ cmd: 'health', model_last_action: new Date().toISOString() })
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
            return true
          }
          default: {
            tickLog(`unknown command from=${msg.from}: ${verb}`)
            return true
          }
        }
      }

      // --- unknown type: do NOT claim as delivered; leave retry-fähig ---
      default: {
        tickLog(`unknown message type=${msg.type} from=${msg.from} id=${msg.id} (not delivered)`)
        return false
      }
    }
  }

  // One event dispatcher handles typed command events and prompt events for
  // both adapters. Prompt text is already composed by Go; this layer only
  // routes commands and returns prompt.inject events to the adapters.
  async function dispatchEvents(
    events: any[],
    sessionIDProvider: () => Promise<string>,
    mode: 'poll' | 'transform',
  ): Promise<any[]> {
    const promptEvents: any[] = []
    for (const event of events) {
      if (!isSupportedEvent(event)) {
        const diagnosticKey = `${String(event?.id)}:${String(event?.type)}`
        if (!unsupportedEventDiagnostics.has(diagnosticKey)) {
          unsupportedEventDiagnostics.add(diagnosticKey)
          tickLog(`unknown event type=${String(event?.type)} id=${event?.id} (left pending)`)
        }
        continue
      }

      if (event.type === 'prompt.inject') {
        promptEvents.push(event)
        continue
      }

      const command = event.payload?.command
      if (typeof command !== 'string') {
        tickLog(`handoff-${mode}-command-skip ship=${aid()} id=${event.id} reason=invalid-command-payload`)
        continue
      }
      const message = {
        id: event.id,
        from: typeof event.source?.from === 'string' ? event.source.from : 'unknown',
        text: command,
        type: typeof event.source?.message_type === 'string' && event.source.message_type
          ? event.source.message_type
          : 'command',
      }
      const sid = await sessionIDProvider()
      let handled = false
      try {
        handled = await handleMessage(message, client, sid)
      } catch (e) {
        tickLog(`handoff-${mode}-dispatch-error ship=${aid()} id=${event.id} error=${String(e).slice(0, 180)}`)
        continue
      }
      if (handled) {
        // Local command execution is not an assistant model turn; retain its
        // handled/seen semantics until typed actions are a separate extension.
        eventCoordinator.markSubmitted([event.id])
        bus({ cmd: 'seen_mark', id: event.id })
        tickLog(`handoff-${mode}-command ship=${aid()} sid=${sid || '-'} id=${event.id} handled=true`)
      } else {
        tickLog(`handoff-${mode}-command-unhandled ship=${aid()} sid=${sid || '-'} id=${event.id} (left pending)`)
      }
    }
    return promptEvents
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
          body: { parts: [{ type: 'text', text: `[synthetic restart after transient error] Transient API failure (${detail.slice(0,50)}). Session cleared/restarted. Please proceed.`, synthetic: true }] },
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
            body: { parts: [{ type: 'text', text: `[synthetic restart] Model switch failed (${targetModel || 'unknown'}). Already switched / no target. Transient error: ${detail.slice(0,50)}. Please proceed.`, synthetic: true }] },
          })
        } catch { /* ignore */ }
        return
      }
      hasSwitched.v = true
      const msg = `ERROR-HANDLE ${src}: switching to ${targetModel} (was: ${detail})`
      appLog('warn', msg)
      tickLog(msg)
      toastBus('warning', 'starfleet-dispatch', msg, 8000)
      // Use .then() chain like the working 'model' command, not await
      const clearMethod = client.session.clear || client.session.reset
      const promise = clearMethod
        ? clearMethod({ path: { id: sessionID } }).then(() => new Promise(r => setTimeout(r, 500)))
        : Promise.resolve()
      promise
        .then(() => switchSessionModel(client, sessionID, targetModel))
        .then(() => {
          tickLog(`ERROR-HANDLE ${src}: update ok → ${targetModel}`)
          return client.session.promptAsync({
            path: { id: sessionID },
            body: { parts: [{ type: 'text', text: `[synthetic restart after switch-model] Switched to ${targetModel}. Transient error resolved: ${detail.slice(0,50)}. Please proceed.`, synthetic: true }] },
          })
        })
        .then(() => tickLog(`ERROR-HANDLE ${src}: promptAsync sent`))
        .catch((e: any) => {
          const emsg = `ERROR-HANDLE ${src}: failed: ${String(e).slice(0, 120)}`
          appLog('error', emsg)
          tickLog(emsg)
          hasSwitched.v = false
        })
      return
    }
    tickLog(`ERROR-HANDLE ${src}: unknown action "${action}" — ignoring`)
  }

  // Heartbeats refresh liveness only; they never imply model activity or task progress.
  const heartbeatTimer = setInterval(() => {
    bus({
      cmd: 'health',
      touch: true,
      plugin_last_run: new Date().toISOString(),
      plugin_version: PLUGIN_VERSION,
      heartbeat_ms: HEARTBEAT_MS,
      pid: process.pid,
      ...currentModel,
    })
  }, HEARTBEAT_MS)

  // Model-error retry detection: opencode does NOT surface quota/rate-limit
  // failures as a `session.error` event — it parks the session in a `retry`
  // status with a human-readable message instead. Poll that status so the
  // fleet can see and react to transient model-API faults.
  let lastRetryDetail = ''
  let retryCooldownUntil = 0

  const pollRetryStatus = async () => {
    tickLog(`retry-poll tick sid=${currentSessionID || '(empty)'}`)
    appLog('info', `retry-poll tick: sid=${currentSessionID || '(empty)'} hasStatus=${typeof client?.session?.status}`)
    if (!currentSessionID) return
    let status: any
    try {
      status = await client.session.status()
    } catch (e) {
      appLog('warn', `retry-poll status() threw: ${String(e).slice(0, 120)}`)
      return
    }
    const body = status?.body ?? status
    appLog('info', `retry-poll raw: sid=${currentSessionID} keys=${body && typeof body === 'object' ? Object.keys(body).join(',') : typeof body} sample=${JSON.stringify(body).slice(0, 200)}`)
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
    appLog('warn', `session retry status: ${detail}`)
    tickLog(`MODEL RETRY (quota/zen): ${detail}`)

    // Delegate policy to starfleetctl FIRST, then decide on toast.
    const r = bus({
      cmd: 'error-handle', detail, source: 'retry-status',
      ship: aid(), pid: process.pid, current_model: currentModel.model || '',
      session_id: currentSessionID, has_fallback: hasSwitchedToFallback.v,
    })
    tickLog(`RETRY-STATUS bus: ok=${r.ok} action=${r.action || 'none'} tag=${r.tag || 'none'} err=${r.error || 'none'}`)

    // Only toast for non-auto-restart errors.
    const isAutoRestart = r.ok && r.action === 'retry' && r.tag &&
      ['resource-exhausted', 'nim-overload', 'streaming-response-failed', 'no-provider'].includes(r.tag)
    if (!isAutoRestart) {
      toastBus('warning', 'starfleet-dispatch', `model retry: ${detail}`, 6000)
    } else {
      tickLog(`RETRY-STATUS: auto-restart error (${r.tag}), skipping toast`)
    }

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
  const errDetail = checkLogForErrors(currentSessionID)
  if (!errDetail) return
  const msg = `LOG ERROR detected: ${errDetail}`
  appLog('warn', msg)
  tickLog(`LOG-MONITOR: ${msg}`)

  // Delegate policy to starfleetctl FIRST, then decide on toast.
  const r = bus({
    cmd: 'error-handle', detail: errDetail, source: 'log-monitor',
    ship: aid(), pid: process.pid, current_model: currentModel.model || '',
    session_id: currentSessionID, has_fallback: hasSwitchedToFallback.v,
  })
  tickLog(`LOG-MONITOR bus: ok=${r.ok} action=${r.action || 'none'} tag=${r.tag || 'none'} err=${r.error || 'none'} detail=${errDetail.slice(0, 60)}`)
  appLog('warn', `log-monitor bus result: ${JSON.stringify(r).slice(0, 200)}`)

  // Only toast for non-auto-restart errors (action != 'retry' or tag not auto-restart)
  // Transient errors (resource-exhausted, nim-overload, etc.) are absorbed by
  // the proxy and retried — no need to notify the user/fleet with a toast.
  const isAutoRestart = r.ok && r.action === 'retry' && r.tag &&
    ['resource-exhausted', 'nim-overload', 'streaming-response-failed', 'no-provider'].includes(r.tag)
  if (!isAutoRestart) {
    toastBus('warning', 'starfleet-dispatch', msg, 8000)
  } else {
    tickLog(`LOG-MONITOR: auto-restart error (${r.tag}), skipping toast`)
  }

  if (r.ok && r.action) {
    if (r.action === 'retry') {
      logMonitorCooldownUntil = Date.now() + LOG_COOLDOWN_MS
      tickLog(`LOG-MONITOR: retry cooldown until ${new Date(logMonitorCooldownUntil).toISOString()}`)
    }
    await executeAction(r.action, r.target_model || '', errDetail, client, currentSessionID, hasSwitchedToFallback)
  }
}, LOG_POLL_MS)

  // Init snapshots unread events without acknowledging them. New binaries
  // return the versioned event envelope; older binaries fall back to the
  // legacy inbox and are adapted locally during the compatibility window.
  const rawInit = bus({ cmd: 'init', note: 'opencode ship' })
  const init = normalizeEventResponse(rawInit)
  let startupEvents: any[] = []
  if (init.ok) {
    startupEvents = init.events
  } else if (rawInit?.ok && rawInit.protocol_version == null && Array.isArray(rawInit.messages)) {
    startupEvents = legacyInboxToEvents(rawInit.messages)
    tickLog(`comms-init-legacy ${formatRuntimeIdentity(RUNTIME_IDENTITY)} ship=${aid()} events=${startupEvents.length}`)
  } else {
    tickLog(`comms-init-error ${formatRuntimeIdentity(RUNTIME_IDENTITY)} ship=${aid()} error=${String(init.error || 'invalid event envelope').slice(0, 180)}`)
  }
  eventCoordinator.seed(startupEvents)

  // DoInit must prune stale records before this process refreshes its PID/liveness.
  bus({
    cmd: 'health',
    plugin_last_run: new Date().toISOString(),
    heartbeat_ms: HEARTBEAT_MS,
    pid: process.pid,
    plugin_version: PLUGIN_VERSION,
  })

  setTimeout(() => {
    if (!tuiReady) {
      tuiReady = true
      appLog('info', 'active (fallback)')
      if (currentSessionID && currentModel.model) {
        bus({ cmd: 'health', ...currentModel })
      }
    }
  }, 3000)

  // Hooks are authoritative during an active turn. After an OpenCode/plugin
  // restart, however, no hook may fire until another user turn. Recover only
  // the unique session titled for this ship in this workspace; never guess.
  const resolveSessionId = async (): Promise<string> => {
    if (currentSessionID) return currentSessionID
    const ship = aid()
    try {
      const response = await client.session.list({ query: { directory: ROOT } })
      const recoveredSessionID = resolveLatestShipSession(response, ship, ROOT)
      if (recoveredSessionID) {
        currentSessionID = recoveredSessionID
        tickLog(`session-resolve ship=${ship}: recovered latest workspace session ${currentSessionID}`)
        return currentSessionID
      }
      if (!sessionResolveDiagnosticLogged) {
        sessionResolveDiagnosticLogged = true
        const body = response?.body ?? response
        const data = body?.data ?? body
        const sessions = Array.isArray(data)
          ? data
          : Array.isArray(data?.sessions)
            ? data.sessions
            : data && typeof data === 'object'
              ? Object.values(data)
              : []
        const keys = (value: any): string[] =>
          value && typeof value === 'object' ? Object.keys(value).slice(0, 12) : []
        const titled = sessions.filter((session: any) => session?.title === ship)
        const eligible = titled.filter((session: any) =>
          session?.id && (!session.directory || session.directory === ROOT)
        )
        tickLog(`session-resolve-debug ship=${ship}: ${JSON.stringify({
          responseType: Array.isArray(response) ? 'array' : typeof response,
          responseKeys: keys(response),
          bodyType: Array.isArray(body) ? 'array' : typeof body,
          bodyKeys: keys(body),
          dataType: Array.isArray(data) ? 'array' : typeof data,
          dataKeys: keys(data),
          sessionCount: sessions.length,
          titleMatches: titled.length,
          eligibleMatches: eligible.length,
          sessions: sessions.slice(0, 50).map((session: any) => ({
            title: typeof session?.title === 'string' ? session.title : null,
            hasId: Boolean(session?.id),
            directoryPresent: Boolean(session?.directory),
            directoryMatchesRoot: !session?.directory || session.directory === ROOT,
            updated: Number.isFinite(session?.updated) ? session.updated : null,
            keys: keys(session),
          })),
        })}`)
      }
      tickLog(`session-resolve ship=${ship}: no latest matching workspace session; refusing handoff`)
    } catch (e) {
      tickLog(`session-resolve ship=${ship}: session.list failed: ${String(e).slice(0, 180)}`)
    }
    return ''
  }

  // All adapters go through this one event poller. Concurrent calls share
  // one in-flight CLI read; the coordinator retains unsubmitted events and
  // leases each event to only one adapter. The raw inbox path is only a
  // compatibility fallback for older starfleetctl binaries.
  const pollEvents = () => eventCoordinator.poll(() => {
    try {
      const raw = bus({ cmd: 'events.poll', consumer: consumerID })
      const normalized = normalizeEventResponse(raw)
      if (normalized.ok) return normalized

      if (raw?.ok === false && /unknown cmd:\s*events\.poll/i.test(String(raw.error || ''))) {
        const legacy = normalizeInboxResponse(bus({ cmd: 'inbox' }))
        if (legacy.ok) {
          return {
            ok: true,
            error: '',
            protocol_version: 1,
            events: legacyInboxToEvents(legacy.messages),
          }
        }
        return { ok: false, error: legacy.error, protocol_version: 1, events: [] }
      }
      return normalized
    } catch (e) {
      return { ok: false, error: String(e), protocol_version: 1, events: [] }
    }
  })

  let pollRunning = false
  let lastInboxPollDiagnosticAt = 0
  let lastInboxPollDiagnosticState = ''
  const logInboxPollDiagnostic = (state: string, detail: string) => {
    const now = Date.now()
    if (state === lastInboxPollDiagnosticState && now - lastInboxPollDiagnosticAt < 60000) return
    lastInboxPollDiagnosticState = state
    lastInboxPollDiagnosticAt = now
    tickLog(`inbox-poll-${state} ${formatRuntimeIdentity(RUNTIME_IDENTITY)} ship=${aid()} pid=${process.pid} tui_ready=${tuiReady} sid=${currentSessionID || '-'} ${detail}`)
  }

  const poll = async () => {
    if (pollRunning) return
    pollRunning = true
    let claimedIds: string[] = []
    const durableSubmitted = new Set<string>()
    try {
      // Keep outstanding correlations observable even after the in-memory
      // coordinator has marked their event submitted. This recovers missing
      // completion events and idle-after-restart cases without replaying prompts.
      for (const [messageID, correlation] of pendingByMessage) {
        if (correlation.submitted) await reconcileCorrelation(correlation.sessionID, messageID, correlation.eventIDs)
      }
      const { response: eventResponse, events: available } = await pollEvents()
      if (!eventResponse.ok) logInboxPollDiagnostic('error', `error=${String(eventResponse.error || 'unknown').replace(/\s+/g, ' ').slice(0, 180)}`)
      const liveEvents = eventResponse.ok ? eventResponse.events : []
      const events = eventCoordinator.claim(available)
      claimedIds = events.map((event: any) => event.id)
      const liveIDs = liveEvents.map((event: any) => event.id).join(',')
      if (liveEvents.length || events.length) tickLog(`event-poll ${formatRuntimeIdentity(RUNTIME_IDENTITY)} ship=${aid()} sid=${currentSessionID || '-'} live=[${liveIDs}] pending=[${claimedIds.join(',')}]`)
      if (!events.length) { logInboxPollDiagnostic('idle', 'events=0 pending=0'); return }
      lastInboxPollDiagnosticState = 'messages'

      const firstCommsReport = reportFirstComms({ ship: aid(), sessionID: currentSessionID, messageIDs: events.map((event: any) => event.id) })
      if (firstCommsReport) tickLog(firstCommsReport)

      const promptEvents = await dispatchEvents(events, () => resolveSessionId(), 'poll')
      if (!promptEvents.length) return
      const targetSid = await resolveSessionId()

      // Persisted lifecycle references take precedence over the currently
      // active session. A ship may have switched/recreated sessions since the
      // event was submitted; recovery must inspect the original OpenCode turn.
      const batches = new Map<string, { sessionID: string; messageID: string; resume: boolean; events: any[] }>()
      for (const event of promptEvents) {
        const lifecycle = event.lifecycle
        const resume = Boolean(lifecycle?.message_ref && (lifecycle.state === 'submitted' || lifecycle.state === 'turn_started'))
        const sessionID = resolveLifecycleSessionID(lifecycle, targetSid)
        const messageID = resume ? lifecycle.message_ref : ''
        const key = resume ? `resume:${sessionID}:${messageID}` : `new:${sessionID}`
        const batch: { sessionID: string; messageID: string; resume: boolean; events: any[] } = batches.get(key) || { sessionID, messageID, resume, events: [] as any[] }
        batch.events.push(event)
        batches.set(key, batch)
      }

      for (const batch of batches.values()) {
        const { sessionID, messageID: recoveredMessageID, resume, events: batchEvents } = batch
        if (!sessionID) {
          tickLog(`handoff-skip ${formatRuntimeIdentity(RUNTIME_IDENTITY)} ship=${aid()} sid=<missing|unresolved> ids=[${batchEvents.map((e: any) => e.id).join(',')}] reason=no-persisted-or-current-session`)
          continue
        }
        const prepared = preparePromptEvents(batchEvents)
        if (!prepared || !prepared.startTurn) {
          tickLog(`handoff-skip ship=${aid()} sid=${sessionID} ids=[${batchEvents.map((e: any) => e.id).join(',')}] reason=invalid-or-non-starting-prompt`)
          continue
        }
        const currentMessageID = resume ? recoveredMessageID : deterministicMessageID(sessionID, prepared.eventIds)
        pendingByMessage.set(currentMessageID, { sessionID, eventIDs: prepared.eventIds, submitted: resume })

        if (resume) {
          const recovery = await reconcileCorrelation(sessionID, currentMessageID, prepared.eventIds)
          if (recovery === 'failed') {
            tickLog(`handoff-recovery-retry ship=${aid()} sid=${sessionID} message=${currentMessageID} ids=[${prepared.eventIds.join(',')}]`)
          } else {
            // Unknown status is not evidence of failure; keep the durable
            // submission leased and let correlated events or later polling
            // establish completion instead of replaying the prompt blindly.
            eventCoordinator.markSubmitted(prepared.eventIds)
            for (const id of prepared.eventIds) durableSubmitted.add(id)
          }
          tickLog(`handoff-recovered ship=${aid()} sid=${sessionID} message=${currentMessageID} ids=[${prepared.eventIds.join(',')}]`)
          continue
        }

        let preparedOK = true
        for (const id of prepared.eventIds) {
          const event = batchEvents.find((entry: any) => entry.id === id)
          if (event?.lifecycle && !transitionEvent(id, 'prepared', sessionID, currentMessageID)) { preparedOK = false; break }
        }
        if (!preparedOK) continue

        tickLog(`handoff-prompt ${formatRuntimeIdentity(RUNTIME_IDENTITY)} ship=${aid()} sid=${sessionID} message=${currentMessageID} ids=[${prepared.eventIds.join(',')}] calling=promptAsync`)
        syntheticMessageIDs.add(currentMessageID)
        try {
          const res = await client.session.promptAsync({
            path: { id: sessionID },
            body: { messageID: currentMessageID, parts: [{ type: 'text', synthetic: true, text: prepared.parts.join('') }] },
          })
          const resErr = res && (res as any).error
          if (!resErr) {
            let accepted = true
            for (const id of prepared.eventIds) {
              const event = batchEvents.find((entry: any) => entry.id === id)
              if (event?.lifecycle && !transitionEvent(id, 'submitted', sessionID, currentMessageID)) accepted = false
              if (!event?.lifecycle) bus({ cmd: 'seen_mark', id })
            }
            if (accepted) {
              eventCoordinator.markSubmitted(prepared.eventIds)
              for (const id of prepared.eventIds) durableSubmitted.add(id)
              const pending = pendingByMessage.get(currentMessageID)
              if (pending) {
                pending.submitted = true
                if (pending.startSeen) markTurnStarted(currentMessageID, pending)
                if (pending.completionSeen) completeCorrelation(currentMessageID, pending)
              }
              tickLog(`handoff-submitted ship=${aid()} sid=${sessionID} message=${currentMessageID} ids=[${prepared.eventIds.join(',')}]`)
            }
          } else {
            syntheticMessageIDs.delete(currentMessageID)
            for (const id of prepared.eventIds) {
              const event = batchEvents.find((entry: any) => entry.id === id)
              if (event?.lifecycle) transitionEvent(id, 'failed', sessionID, currentMessageID, String(resErr).slice(0, 512))
            }
            pendingByMessage.delete(currentMessageID)
            eventCoordinator.forget(prepared.eventIds)
            tickLog(`handoff-fail ship=${aid()} sid=${sessionID} ids=[${prepared.eventIds.join(',')}] promptError=${JSON.stringify(resErr).slice(0, 180)}`)
          }
        } catch (e) {
          syntheticMessageIDs.delete(currentMessageID)
          for (const id of prepared.eventIds) {
            const event = batchEvents.find((entry: any) => entry.id === id)
            if (event?.lifecycle) transitionEvent(id, 'failed', sessionID, currentMessageID, String(e).slice(0, 512))
          }
          pendingByMessage.delete(currentMessageID)
          eventCoordinator.forget(prepared.eventIds)
          tickLog(`handoff-exception ship=${aid()} sid=${sessionID} ids=[${prepared.eventIds.join(',')}] error=${String(e).slice(0, 200)}`)
        }
      }
    } finally {
      eventCoordinator.release(claimedIds)
      releaseEvents(claimedIds.filter((id) => !durableSubmitted.has(id)))
      pollRunning = false
    }
  }

  const pollTimer = setInterval(poll, POLL_MS)
  // Run once immediately; do not wait for a TUI event or the first interval.
  void poll()

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
    // Authoritative session id for the current turn — opencode passes it to
    // every chat hook. session.status() (used by resolveSessionId) can return
    // stale/foreign ids, so prefer this whenever it is available.
    'chat.message': async (input: any, output: { message: any; parts: any[] }) => {
      const sessionID = typeof input?.sessionID === 'string' ? input.sessionID : ''
      const messageID = typeof output?.message?.id === 'string' ? output.message.id : ''
      if (!sessionID || !messageID) return
      currentSessionID = sessionID
      if (syntheticMessageIDs.has(messageID)) {
        syntheticMessageIDs.delete(messageID)
        const correlation = pendingByMessage.get(messageID)
        if (correlation) turnCorrelationQueue.capture(sessionID, messageID, { kind: 'synthetic', eventIDs: correlation.eventIDs })
        return
      }
      turnCorrelationQueue.capture(sessionID, messageID, { kind: 'current' })
    },
    'chat.params': async (input: any) => {
      if (input?.sessionID && input.sessionID !== currentSessionID) {
        currentSessionID = input.sessionID
      }
    },
    'experimental.chat.system.transform': async (
      input: any,
      output: { system: string[] },
    ) => {
      turnCount++
      if (input?.sessionID && input.sessionID !== currentSessionID) currentSessionID = input.sessionID
      const turnStartedAt = new Date().toISOString()
      bus({
        cmd: 'health',
        plugin_last_run: turnStartedAt,
        model_last_action: turnStartedAt,
        heartbeat_ms: HEARTBEAT_MS,
        pid: process.pid,
      })

      const hasIdentity = output.system.some(l => l.includes('--- fleet identity ---'))
      if (sessionNeedsIdentity || !hasIdentity) {
        sessionNeedsIdentity = false
        const shipId = process.env.STARFLEET_SHIP_ID || 'unknown'
        const role = process.env.STARFLEET_ROLE || 'ship'
        const target = process.env.STARFLEET_TARGET || ''
        const parts = [`You are ${role} ${shipId}.`]
        if (target) parts.push(`Report to ${target}.`)
        parts.push('Re-read and follow the agent instructions in sop.d/index.md.')
        output.system.push('', '--- fleet identity ---', ...parts, '--- end fleet identity ---')
      }

      const handoffSid = currentSessionID
      if (!handoffSid) return
      const turnCorrelation = turnCorrelationQueue.consume(handoffSid)
      if (!turnCorrelation) {
        tickLog(`event-transform-skip ship=${aid()} sid=${handoffSid} reason=no-awaited-chat-message-correlation`)
        return
      }
      const correlation = turnCorrelation.metadata || {}
      if (correlation.kind === 'synthetic') {
        const eventIDs = Array.isArray(correlation.eventIDs) ? correlation.eventIDs : []
        const pending = pendingByMessage.get(turnCorrelation.messageID)
        if (pending) markTurnStarted(turnCorrelation.messageID, pending)
        tickLog(`event-turn-started ship=${aid()} sid=${handoffSid} message=${turnCorrelation.messageID} ids=[${eventIDs.join(',')}]`)
        return
      }

      const { response: eventResponse, events: available } = await pollEvents()
      if (!eventResponse.ok) tickLog(`event-transform-error ${formatRuntimeIdentity(RUNTIME_IDENTITY)} ship=${aid()} error=${String(eventResponse.error || 'unknown').slice(0, 180)}`)
      const claimed = eventCoordinator.claim(available)
      const claimedIds = claimed.map((event: any) => event.id)
      const durableSubmitted = new Set<string>()
      try {
        const promptEvents = await dispatchEvents(claimed, async () => handoffSid, 'transform')
        if (!promptEvents.length) return
        const prepared = preparePromptEvents(promptEvents)
        if (!prepared) {
          tickLog(`handoff-transform-skip ship=${aid()} sid=${handoffSid} reason=invalid-event-prompt`)
          return
        }
        let preparedOK = true
        for (const id of prepared.eventIds) {
          const event = promptEvents.find((entry: any) => entry.id === id)
          if (event?.lifecycle && !transitionEvent(id, 'prepared', handoffSid, turnCorrelation.messageID)) { preparedOK = false; break }
        }
        if (!preparedOK) return

        output.system.push(prepared.parts.join(''))
        pendingByMessage.set(turnCorrelation.messageID, { sessionID: handoffSid, eventIDs: prepared.eventIds, submitted: true })
        // The system prompt is now part of this active turn. Keep a local
        // tombstone and the durable lease even if a lifecycle write fails;
        // the correlated turn callbacks/poller will retry persistence. Releasing
        // here could let another adapter inject the same directive twice.
        eventCoordinator.markSubmitted(prepared.eventIds)
        for (const id of prepared.eventIds) durableSubmitted.add(id)
        let accepted = true
        for (const id of prepared.eventIds) {
          const event = promptEvents.find((entry: any) => entry.id === id)
          if (event?.lifecycle && !transitionEvent(id, 'submitted', handoffSid, turnCorrelation.messageID)) accepted = false
          if (!event?.lifecycle) bus({ cmd: 'seen_mark', id })
        }
        if (accepted) {
          tickLog(`handoff-transform-submitted ship=${aid()} sid=${handoffSid} message=${turnCorrelation.messageID} ids=[${prepared.eventIds.join(',')}]`)
        } else {
          tickLog(`handoff-transform-submit-pending ship=${aid()} sid=${handoffSid} message=${turnCorrelation.messageID} ids=[${prepared.eventIds.join(',')}]`)
        }
      } finally {
        eventCoordinator.release(claimedIds)
        releaseEvents(claimedIds.filter((id) => !durableSubmitted.has(id)))
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

        bus({
          cmd: 'health',
          plugin_last_run: new Date().toISOString(),
          heartbeat_ms: HEARTBEAT_MS,
          pid: process.pid,
          ...currentModel,
        })
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
        bus({
          cmd: 'health',
          plugin_last_run: new Date().toISOString(),
          heartbeat_ms: HEARTBEAT_MS,
          pid: process.pid,
        })
      }
      if (event.type === 'message.updated') {
        const info = event.properties?.info as any
        if (info?.role === 'assistant') {
          const correlation = pendingByMessage.get(info.parentID)
          if (correlation) {
            const updateState = classifyAssistantUpdate(info, correlation.sessionID, info.parentID)
            if (updateState === 'failed') {
              for (const id of correlation.eventIDs) transitionEvent(id, 'failed', correlation.sessionID, info.parentID, String(info.error).slice(0, 512))
              pendingByMessage.delete(info.parentID)
              lastReconcileAt.delete(info.parentID)
              eventCoordinator.forget(correlation.eventIDs)
            } else if (updateState === 'completed') {
              if (completeCorrelation(info.parentID, correlation)) {
                tickLog(`event-turn-completed ship=${aid()} sid=${correlation.sessionID} message=${info.parentID} ids=[${correlation.eventIDs.join(',')}]`)
              } else {
                tickLog(`event-turn-completion-pending ship=${aid()} sid=${correlation.sessionID} message=${info.parentID} ids=[${correlation.eventIDs.join(',')}]`)
              }
            } else if (updateState === 'started') {
              markTurnStarted(info.parentID, correlation)
            }
          }
          if (info.modelID) {
            currentModel = { model: info.modelID, server: info.providerID }
            if (hasSwitchedToFallback.v) {
              tickLog(`MODEL RECOVERY: session recovered on ${info.modelID} — fallback worked`)
              hasSwitchedToFallback.v = false
            }
            bus({ cmd: 'health', error_tag: undefined, plugin_last_run: new Date().toISOString() })
          }
        }
      }
      if (event.type === 'session.status') {
        const props = event.properties as any
        if (props?.status?.type === 'busy') {
          for (const [messageID, correlation] of pendingByMessage) {
            if (correlation.sessionID !== props.sessionID) continue
            markTurnStarted(messageID, correlation)
            break // session busy identifies the oldest pending turn in this session
          }
        }
      }
if (event.type === 'session.error') {
         const failedSessionID = String((event.properties as any)?.sessionID || currentSessionID)
         const correlatedError = (event.properties?.error as any)?.message ||
           (event.properties?.error as any)?.code || 'correlated session.error'
         for (const [messageID, correlation] of pendingByMessage) {
           if (correlation.sessionID !== failedSessionID) continue
           for (const id of correlation.eventIDs) transitionEvent(id, 'failed', correlation.sessionID, messageID, String(correlatedError).slice(0, 512))
           pendingByMessage.delete(messageID)
           lastReconcileAt.delete(messageID)
           eventCoordinator.forget(correlation.eventIDs)
           break // session.error applies to the active (oldest) correlated turn
         }
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

         // Rate-limit/coalesce session.error reports: buffer unique errors and
         // send a single aggregated report after a cooldown period of no new errors.
         const now = Date.now()
         if (!sessionErrorBuffer.includes(candidate)) {
           sessionErrorBuffer.push(candidate)
         }
         // Reset the timer: if there is an existing timer, clear it and start a new one.
         if (sessionErrorTimer !== null) {
           clearTimeout(sessionErrorTimer)
         }
         sessionErrorTimer = setTimeout(() => {
           // Time to send the aggregated report.
           if (sessionErrorBuffer.length > 0) {
             const aggregated = sessionErrorBuffer.join('; ')
             bus({
               cmd: 'error-handle',
               detail: `Multiple session errors (count: ${sessionErrorBuffer.length}): ${aggregated}`,
               source: 'session.error',
               ship: aid(),
               pid: process.pid,
               current_model: currentModel.model || '',
               session_id: currentSessionID,
               has_fallback: hasSwitchedToFallback.v,
             })
             sessionErrorBuffer = []
             sessionErrorTimer = null
           }
         }, SESSION_ERROR_COOLDOWN_MS)
         return
       }
    },
  }
}
