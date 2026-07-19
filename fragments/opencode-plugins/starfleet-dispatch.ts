// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Auto-installed by `starfleetctl bootstrap --fix` from
// github.com/mpbt-hq/starfleetctl (fragments/opencode-plugins/).
// Do NOT hand-edit — changes are overwritten on the next bootstrap.
// Edit the canonical copy in the starfleetctl repo instead.

import { readFileSync, readdirSync, mkdirSync, writeFileSync, appendFileSync } from 'node:fs'
import { join } from 'node:path'

const ROOT = process.cwd()
const SEEN_DIR = join(ROOT, '_WORK_', 'agent-bus', 'monitor-seen')
const SHIPS_DIR = join(ROOT, '_WORK_', 'agent-bus', 'ships')
const HEALTH_DIR = join(ROOT, '_WORK_', 'agent-bus', 'health')
const HEARTBEAT_MS = 300_000
const POLL_MS = 3_000
const BLOCKED_THRESHOLD_MS = 120_000 // 2 min without plugin run → blocked

function aid(): string {
  return process.env.STARFLEET_SHIP_ID || 'default'
}

function seenFile(): string {
  return join(SEEN_DIR, aid())
}

function loadSeenAll(): Set<string> {
  const s = new Set<string>()
  try {
    for (const entry of readdirSync(SEEN_DIR)) {
      try {
        const content = readFileSync(join(SEEN_DIR, entry), 'utf-8')
        for (const line of content.split('\n')) {
          const id = line.trim()
          if (id) s.add(id)
        }
      } catch { /* ignore individual file errors */ }
    }
  } catch { /* ignore missing dir */ }
  return s
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

function markSeen(id: string): void {
  try { appendFileSync(seenFile(), id + '\n') } catch { /* ignore */ }
}

function logEvent(msg: string): void {
  try {
    appendFileSync(join(ROOT, '_WORK_', 'agent-bus', 'events.log'),
      `${new Date().toISOString()}\tplugin\t${aid()}\t${msg}\n`)
  } catch { /* ignore */ }
}

// --- Health tracking ---
// Three timestamps written to _WORK_/agent-bus/health/<SHIP_ID>.json:
//   plugin_last_run  — when system.transform last fired (every turn)
//   model_last_action — when the model last produced output (turn end / event)
//   state            — derived: idle | working | blocked
//
// External watchdogs read this file to detect unresponsive ships.

interface HealthData {
  plugin_last_run: string   // ISO timestamp
  model_last_action: string // ISO timestamp
  state: 'idle' | 'working' | 'blocked'
  pid: number
}

function healthFile(): string {
  return join(HEALTH_DIR, `${aid()}.json`)
}

function writeHealth(patch: Partial<HealthData>): void {
  try {
    mkdirSync(HEALTH_DIR, { recursive: true })
    let prev: Partial<HealthData> = {}
    try { prev = JSON.parse(readFileSync(healthFile(), 'utf-8')) } catch { /* first write */ }
    const now = new Date().toISOString()
    const merged: HealthData = {
      plugin_last_run: patch.plugin_last_run ?? prev.plugin_last_run ?? now,
      model_last_action: patch.model_last_action ?? prev.model_last_action ?? now,
      state: patch.state ?? prev.state ?? 'idle',
      pid: patch.pid ?? prev.pid ?? process.pid,
    }
    writeFileSync(healthFile(), JSON.stringify(merged, null, 2) + '\n')
  } catch { /* ignore */ }
}

function detectState(pluginLastRun: string): 'idle' | 'working' | 'blocked' {
  const age = Date.now() - new Date(pluginLastRun).getTime()
  return age > BLOCKED_THRESHOLD_MS ? 'blocked' : 'working'
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

// isUserAbort reports whether a session.error detail is a user-initiated
// abort (Ctrl-C / SIGINT / context cancelled) rather than a genuine fault.
// opencode surfaces those with an empty or generic detail — there is no
// structured code we can trust, so we match the textual fingerprint of
// the common abort reasons. These are expected, not actionable, and must
// not be broadcast back to the fleet (see the session.error handler).
function isUserAbort(detail: string): boolean {
  const d = detail.toLowerCase()
  if (d === '' || d === 'unknown error') return true
  return /(^|\W)(abort|cancel|interrupt|signal|sigint|econnaborted|context (deadline|canceled))/.test(d)
}

export const plugin = async ({ client, $ }: any) => {
  mkdirSync(SEEN_DIR, { recursive: true })

  const heartbeatTimer = setInterval(() => {
    try { $`.starfleet-ai/bin/starfleetctl agent-bus touch`.quiet() } catch { /* ignore */ }
  }, HEARTBEAT_MS)

  let tuiReady = false
  let sessionNeedsIdentity = true // first turn after session creation
  let submitted = new Set<string>() // in-memory dedup für Polling
  let turnCount = 0 // track turns for model_last_action

  // Write initial health marker
  writeHealth({ state: 'working', plugin_last_run: new Date().toISOString(), pid: process.pid })

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

  const cleanup = () => {
    clearInterval(heartbeatTimer)
    clearInterval(pollTimer)
    writeHealth({ state: 'idle', pid: process.pid })
    try { $`.starfleet-ai/bin/starfleetctl agent-bus clear`.quiet() } catch { /* ignore */ }
  }

  process.on('exit', cleanup)

  return {
    'experimental.chat.system.transform': async (
      _input: any,
      output: { system: string[] },
    ) => {
      turnCount++
      // Health: every system.transform = plugin ran. If turnCount > 1,
      // the model just finished an action (tool call / response) since
      // the last transform → update model_last_action.
      writeHealth({
        plugin_last_run: new Date().toISOString(),
        model_last_action: turnCount > 1 ? new Date().toISOString() : undefined,
        state: 'working',
        pid: process.pid,
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
        markSeen(msg.id)
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
        writeHealth({ model_last_action: new Date().toISOString(), state: 'working', pid: process.pid })
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
        writeHealth({ model_last_action: new Date().toISOString(), state: 'working', pid: process.pid })
      }
      if (event.type === 'session.error') {
        writeHealth({ state: 'blocked', pid: process.pid })
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
        if (isUserAbort(detail)) {
          logEvent(`error (user abort, suppressed): ${detail}`)
          return
        }
        logEvent(`error: ${detail}`)
        // Tell the CONTROL agent (flagship) only, never broadcast to all —
        // a broadcast would land in the errored ship's own inbox and the
        // self-loop would restart even for genuine (non-abort) errors.
        try { await $`.starfleet-ai/bin/starfleetctl agent-bus tell Enterprise ⚠️ ${aid()} session.error: ${detail}`.quiet() } catch { /* ignore */ }
      }
    },
  }
}
