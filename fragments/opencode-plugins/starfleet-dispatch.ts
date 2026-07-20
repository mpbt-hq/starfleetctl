// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Auto-installed by `starfleetctl bootstrap --fix` from
// github.com/mpbt-hq/starfleetctl (fragments/opencode-plugins/).
// Do NOT hand-edit — changes are overwritten on the next bootstrap.
// Edit the canonical copy in the starfleetctl repo instead.

import { readFileSync, appendFileSync } from 'node:fs'
import { join } from 'node:path'
import { execSync } from 'node:child_process'

const ROOT = process.cwd()
const SEEN_DIR = join(ROOT, '.starfleet-ai', 'var', 'agent-bus', 'monitor-seen')
const SHIPS_DIR = join(ROOT, '.starfleet-ai', 'var', 'agent-bus', 'ships')

// Plugin tuning knobs — fetched from starfleetctl config at startup,
// falling back to hardcoded defaults if the CLI is unavailable.
let HEARTBEAT_MS = 300_000
let POLL_MS = 3_000

function loadConfig(): void {
  try {
    const raw = execSync(`.starfleet-ai/bin/starfleetctl agent-bus config`, { cwd: ROOT, timeout: 3000, stdio: ['pipe', 'pipe', 'ignore'] }).toString().trim()
    const cfg = JSON.parse(raw)
    if (cfg.heartbeat_ms) HEARTBEAT_MS = cfg.heartbeat_ms
    if (cfg.poll_ms) POLL_MS = cfg.poll_ms
  } catch { /* use defaults */ }
}

function aid(): string {
  return process.env.STARFLEET_SHIP_ID || 'default'
}

function seenFile(): string {
  return join(SEEN_DIR, aid())
}

function loadSeenShip(): Set<string> {
  const s = new Set<string>()
  try {
    const content = readFileSync(seenFile(), 'utf-8')
    for (const line of content.split('\n')) {
      const id = line.trim()
      if (id) s.add(id)
    }
  } catch { /* ignore missing file */ }
  return s
}

function logEvent(msg: string): void {
  try {
    appendFileSync(join(ROOT, '.starfleet-ai', 'var', 'agent-bus', 'events.log'),
      `${new Date().toISOString()}\tplugin\t${aid()}\t${msg}\n`)
  } catch { /* ignore */ }
}

// shell-escape for safe interpolation into $`...` template literals.
function esc(s: string): string {
  return s.replace(/'/g, "'\\''")
}

// Write health data via starfleetctl Go CLI (read-modify-write in Go,
// so we only pass the fields that changed).
async function healthUpdate($: any, patch: Record<string, string | undefined>): Promise<void> {
  const args: string[] = []
  for (const [k, v] of Object.entries(patch)) {
    if (v === undefined || v === '') continue
    const flag = k.replace(/([A-Z])/g, '-$1').toLowerCase()
    args.push(`--${flag} '${esc(v)}'`)
  }
  if (args.length === 0) return
  try { await $`.starfleet-ai/bin/starfleetctl agent-bus health update ${args.join(' ')}`.quiet() } catch { /* ignore */ }
}

// agent-bus inbox uses fixed-width columns:
//   %-6s  %-7s  %-18s  %-4s  %s
//   0-5    8-14   17-34   37-40  41+
function parseInboxLine(line: string): { id: string; from: string; text: string } | null {
  if (!line || line.startsWith('ID') || line.startsWith('---') || line.startsWith('(inbox')) return null
  const id = line.slice(0, 6).trim()
  if (!id.startsWith('m')) return null
  const from = line.slice(17, 35).trim()
  const text = line.slice(41).trim()
  if (!text) return null
  return { id, from, text }
}

async function getInbox($: any): Promise<{ id: string; from: string; text: string }[]> {
    try {
      const output = await $`.starfleet-ai/bin/starfleetctl agent-bus inbox`.text()
      const msgs: { id: string; from: string; text: string }[] = []
      for (const line of output.split('\n')) {
        const msg = parseInboxLine(line)
        if (msg) msgs.push(msg)
      }
      return msgs
    } catch { return [] }
  }

async function autoPong($: any, id: string, from: string, text: string): Promise<void> {
  if (from === 'Enterprise' && /ping/i.test(text)) {
    try {
      await $`.starfleet-ai/bin/starfleetctl agent-bus ack ${id} auto-pong`.quiet()
      await $`.starfleet-ai/bin/starfleetctl agent-bus tell Enterprise --reply ${id} Pong! (auto-reply to ${id})`.quiet()
      logEvent(`auto-pong ${id} → Enterprise`)
    } catch { /* ignore */ }
  }
}

export const plugin = async ({ client, $ }: any) => {
  // Fetch tuning knobs from starfleetctl config (heartbeat, poll intervals).
  loadConfig()

  // Write initial health marker via Go CLI (delete-then-write =
  // --delete on stale file, then fresh update). The Go CLI does
  // read-modify-write internally, so we just supply current fields.
  try { await $`.starfleet-ai/bin/starfleetctl agent-bus health update --delete`.quiet() } catch { /* ignore */ }
  await healthUpdate($, { state: 'working', pluginLastRun: new Date().toISOString(), pid: String(process.pid) })

  const heartbeatTimer = setInterval(async () => {
    try {
      await healthUpdate($, { pluginLastRun: new Date().toISOString(), ...currentModel })
      $`.starfleet-ai/bin/starfleetctl agent-bus touch`.quiet()
    } catch { /* ignore */ }
  }, HEARTBEAT_MS)

  let tuiReady = false
  let sessionNeedsIdentity = true // first turn after session creation
  let submitted = new Set<string>() // in-memory dedup für Polling
  let turnCount = 0 // track turns for model_last_action
  let currentModel: { model?: string; server?: string } = {} // tracked via message.updated events

  // seed: ack all current inbox messages so they don't keep showing up
  // as "unread" after a restart. IMPORTANT: do NOT markSeen() them here —
  // writing them into monitor-seen would poison brand-new directives that
  // arrived just before/at session start, so the poll loop and the
  // system.transform injection would both skip them and an idle ship would
  // never wake. Only messages this session *actually* injects get marked
  // seen (see poll()/transform), which is the correct cross-restart dedup.
  const inbox = await getInbox($)
  for (const msg of inbox) {
    try { await $`.starfleet-ai/bin/starfleetctl agent-bus ack ${msg.id} init-seen`.quiet() } catch { /* ignore */ }
  }
  // seed submitted set with THIS ship's seen messages only —
  // using all ships' IDs caused submitted.size > 100 guard to
  // kill the poll loop permanently on busy repos.
  for (const id of loadSeenShip()) {
    submitted.add(id)
  }

  try { await $`.starfleet-ai/bin/starfleetctl agent-bus prune`.quiet() } catch { /* ignore */ }
  try { await $`.starfleet-ai/bin/starfleetctl agent-bus status idle opencode ship`.quiet() } catch { /* ignore */ }

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

  const poll = async () => {
    if (!tuiReady) return
    try {
      const msgs = await getInbox($)
      for (const msg of msgs) {
        if (submitted.has(msg.id)) continue
        submitted.add(msg.id)
        client.app.log({ body: { service: 'starfleet-dispatch', level: 'info', message: `inbox: [${msg.id}] from ${msg.from}: ${msg.text.slice(0, 80)}` } }).catch(() => {})
        await autoPong($, msg.id, msg.from, msg.text)

        const ok = await submit(`[${msg.id}] from ${msg.from}: ${msg.text}`)
        if (!ok) {
          // submit failed → retry next poll
          submitted.delete(msg.id)
        }
      }
    } catch { /* ignore */ }
  }

  const pollTimer = setInterval(poll, POLL_MS)

  // SIGTERM/SIGINT get async cleanup via beforeExit; 'exit' is a
  // synchronous last-resort that can't await — write health directly.
  process.on('exit', () => {
    clearInterval(heartbeatTimer)
    clearInterval(pollTimer)
    // Best-effort sync write via child_process.execSync (block but don't hang).
    try {
      const { execSync } = require('node:child_process')
      execSync(`.starfleet-ai/bin/starfleetctl agent-bus health update --state idle --pid ${process.pid}`,
        { cwd: ROOT, timeout: 2000, stdio: 'ignore' })
    } catch { /* ignore */ }
    try { execSync(`.starfleet-ai/bin/starfleetctl agent-bus clear`, { cwd: ROOT, timeout: 2000, stdio: 'ignore' }) } catch { /* ignore */ }
  })

  return {
    'experimental.chat.system.transform': async (
      _input: any,
      output: { system: string[] },
    ) => {
      turnCount++
      // Health: every system.transform = plugin ran. If turnCount > 1,
      // the model just finished an action (tool call / response) since
      // the last transform → update model_last_action.
      await healthUpdate($, {
        pluginLastRun: new Date().toISOString(),
        modelLastAction: turnCount > 1 ? new Date().toISOString() : undefined,
        state: 'working',
        pid: String(process.pid),
      })

      try { await $`.starfleet-ai/bin/starfleetctl agent-bus status working opencode ship`.quiet() } catch { /* ignore */ }

      // Fleet identity: injected on session.created / session.cleared /
      // session.reset (via sessionNeedsIdentity flag).  Safety-net: also
      // re-inject if the marker is absent from the system context (covers
      // edge cases where /clear fires no recognizable event).
      const hasIdentity = output.system.some(l => l.includes('--- fleet identity ---'))
      if (sessionNeedsIdentity || !hasIdentity) {
        sessionNeedsIdentity = false
        const shipId = process.env.STARFLEET_SHIP_ID || 'unknown'
        const role = process.env.STARFLEET_ROLE || 'ship'
        const target = process.env.STARFLEET_TARGET || ''
        const parts = [`You are ${role} ${shipId}.`]
        if (target) {
          parts.push(`Report to ${target}.`)
        }
        parts.push('Re-read and follow the agent instructions in agents.d/index.md.')
        output.system.push('', '--- fleet identity ---', ...parts, '--- end fleet identity ---')
      }

      const lines: string[] = []
      const msgs = await getInbox($)

      for (const msg of msgs) {
        // Use the per-ship in-memory dedup (this session's actual handling),
        // not loadSeenAll(): a directive addressed to THIS ship must be
        // injected even if another ship coincidentally marked it seen, and a
        // message skipped by the startup seed must still wake an idle ship.
        if (submitted.has(msg.id)) continue
        try { await $`.starfleet-ai/bin/starfleetctl agent-bus monitor-seen mark '${esc(msg.id)}'`.quiet() } catch { /* ignore */ }
        submitted.add(msg.id) // sync with polling dedup
        lines.push(`[${msg.id}] from ${msg.from}: ${msg.text}`)
        await autoPong($, msg.id, msg.from, msg.text)
      }

      if (lines.length > 0) {
        output.system.push(
          '',
          '--- agent-bus inbox ---',
          ...lines,
          '--- end agent-bus inbox ---',
        )
      }
    },
    event: async ({ event }: { event: { type: string; properties?: Record<string, unknown> } }) => {
      if (event.type === 'session.created') {
        tuiReady = true
        sessionNeedsIdentity = true
        turnCount = 0
        await healthUpdate($, { modelLastAction: new Date().toISOString(), state: 'working', pid: String(process.pid) })
        // Label the TUI session/tab title with the ship name (formerly
        // the standalone session-title.ts plugin).
        const shipName = process.env.STARFLEET_SHIP_ID
        if (shipName) {
          try {
            const sessionId = (event.properties?.info as { id?: string })?.id
            if (sessionId) {
              await client.session.update({ path: { id: sessionId }, body: { title: shipName } })
            }
          } catch { /* ignore */ }
        }
      }
      // /clear, /new, /compact may emit session.cleared or session.reset
      // instead of session.created — re-inject fleet identity in all cases.
      if (event.type === 'session.cleared' || event.type === 'session.reset') {
        sessionNeedsIdentity = true
        turnCount = 0
        currentModel = {} // model state is stale after clear/reset
        await healthUpdate($, { modelLastAction: new Date().toISOString(), state: 'working', pid: String(process.pid) })
      }
      // Track model changes in-memory: every assistant message carries
      // the exact providerID/modelID that processed it. This fires on
      // every turn, so currentModel is always current — no polling needed.
      if (event.type === 'message.updated') {
        const info = event.properties?.info as any
        if (info?.role === 'assistant' && info?.modelID) {
          currentModel = { model: info.modelID, server: info.providerID }
          // Auto-recovery: a successful assistant turn means the model API
          // is responsive again — clear any prior error_tag / blocked state.
          await healthUpdate($, { state: 'working', errorTag: undefined, pluginLastRun: new Date().toISOString() })
        }
      }
      if (event.type === 'session.error') {
        const detail =
          (event.properties?.error as { message?: string; code?: string })?.message ||
          (event.properties?.error as { code?: string })?.code ||
          'unknown error'
        // User-initiated aborts (Ctrl-C / SIGINT / context cancelled) surface as
        // a session.error with an empty or generic detail. They are expected, not
        // actionable fleet events — and crucially, broadcasting them back to
        // ALL ships (including the one that errored) makes that ship's own
        // plugin re-pick its own error as a new inbox directive, react, and
        // chatter the bus in a loop. Suppress those: log locally only.
        try {
          const abortOut = await $`.starfleet-ai/bin/starfleetctl agent-bus error is-abort '${esc(detail)}'`.text()
          if (abortOut.trim() === 'true') {
            logEvent(`error (user abort, suppressed): ${detail}`)
            return
          }
        } catch { /* fall through to classify */ }
        let tag = ''
        try {
          const classifyOut = await $`.starfleet-ai/bin/starfleetctl agent-bus error classify '${esc(detail)}'`.text()
          tag = classifyOut.trim()
          if (tag === '(none)') tag = ''
        } catch { /* ignore */ }
        // Report a model-API failure class in the health record so the fleet
        // console can distinguish a throttled ship from a hard crash.
        await healthUpdate($, { state: 'blocked', errorTag: tag || undefined, pid: String(process.pid) })
        const label = tag ? ` [${tag}]` : ''
        logEvent(`error${label}: ${detail}`)
        // Tell the CONTROL agent (flagship) only, never broadcast to all —
        // a broadcast would land in the errored ship's own inbox and the
        // self-loop would restart even for genuine (non-abort) errors.
        try { await $`.starfleet-ai/bin/starfleetctl agent-bus tell Enterprise ⚠️ ${aid()} session.error${label}: ${detail}`.quiet() } catch { /* ignore */ }
      }
    },
  }
}
