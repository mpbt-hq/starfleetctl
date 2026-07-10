import { readFileSync, mkdirSync, writeFileSync, appendFileSync } from 'node:fs'
import { join } from 'node:path'

const ROOT = process.cwd()
const SEEN_DIR = join(ROOT, '_WORK_', 'agent-bus', 'monitor-seen')
const HEARTBEAT_MS = 300_000
const POLL_MS = 3_000

function aid(): string {
  return process.env.AGENT_ID || 'default'
}

function seenFile(): string {
  return join(SEEN_DIR, aid())
}

function loadSeen(): Set<string> {
  const s = new Set<string>()
  try {
    const content = readFileSync(seenFile(), 'utf-8')
    for (const line of content.split('\n')) {
      const id = line.trim()
      if (id) s.add(id)
    }
  } catch { /* ignore */ }
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
      const output = await $`.bin/starfleetctl agent-bus inbox`.text()
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
      await $`scripts/starfleetctl agent-bus ack ${id} auto-pong`.quiet()
      await $`scripts/starfleetctl agent-bus tell Enterprise Pong! (auto-reply to ${id})`.quiet()
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

  // seed: alte Messages als gesehen markieren = kein Flood beim Startup
  const known = loadSeen()
  const inbox = await getInbox($)
  for (const msg of inbox) {
    if (!known.has(msg.id)) {
      markSeen(msg.id)
      known.add(msg.id)
    }
  }

  try { await $`.bin/starfleetctl agent-bus prune`.quiet() } catch { /* ignore */ }
  try { await $`.bin/starfleetctl agent-bus status idle opencode ship`.quiet() } catch { /* ignore */ }

  const heartbeatTimer = setInterval(() => {
    try { $`.bin/starfleetctl agent-bus touch`.quiet() } catch { /* ignore */ }
  }, HEARTBEAT_MS)

  let tuiReady = false
  let submitted = new Set<string>() // in-memory dedup für Polling

  setTimeout(() => {
    if (!tuiReady) {
      tuiReady = true
      client.app.log({ body: { service: 'bus-poller', level: 'info', message: 'active (fallback)' } }).catch(() => {})
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
    if (!tuiReady || submitted.size > 100) return
    try {
      const msgs = await getInbox($)
      for (const msg of msgs) {
        if (submitted.has(msg.id)) continue
        submitted.add(msg.id)
        client.app.log({ body: { service: 'bus-poller', level: 'info', message: `inbox: [${msg.id}] from ${msg.from}: ${msg.text.slice(0, 80)}` } }).catch(() => {})
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
    try { $`.bin/starfleetctl agent-bus clear`.quiet() } catch { /* ignore */ }
  }

  process.on('exit', cleanup)

  return {
    'experimental.chat.system.transform': async (
      _input: any,
      output: { system: string[] },
    ) => {
      try { await $`.bin/starfleetctl agent-bus status working opencode ship`.quiet() } catch { /* ignore */ }

      const seen = loadSeen()
      const lines: string[] = []
      const msgs = await getInbox($)

      for (const msg of msgs) {
        if (seen.has(msg.id)) continue
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
        if (isUserAbort(detail)) {
          logEvent(`error (user abort, suppressed): ${detail}`)
          return
        }
        logEvent(`error: ${detail}`)
        // Tell the CONTROL agent (flagship) only, never broadcast to all —
        // a broadcast would land in the errored ship's own inbox and the
        // self-loop would restart even for genuine (non-abort) errors.
        try { await $`scripts/starfleetctl agent-bus tell Enterprise ⚠️ ${aid()} session.error: ${detail}`.quiet() } catch { /* ignore */ }
      }
    },
  }
}
