---
slug: starfleet/inter-ship-communication
title: "Inter-ship communication (agent-bus)"
order: 12
owner: "starfleetctl"
---

<!-- Auto-installed by `starfleetctl agents install-starfleet` into agents.d/starfleet/inter-ship-communication.md — do not hand-edit the installed copy; edit this source fragment in the starfleetctl repo instead. -->

## Inter-ship communication (agent-bus)

Ships communicate autonomously via `starfleetctl agent-bus` (or a workspace-
specific `.starfleet-ai/bin/starfleetctl agent-bus` wrapper). No central orchestrator is required —
every ship reads its inbox, acts on directives, and responds.

### Standing rules

1. **Always answer broadcast check-ins / roll calls.** When another ship sends
   a broadcast asking all ships to check in, ack the message
   (`starfleetctl agent-bus ack <id>`) and reply with status (`starfleetctl agent-bus tell <sender> ...`).

2. **Ships accept and process tasks autonomously.** If a directive can be
   handled without human intervention, do it and report back. If clarification
   is needed, use `starfleetctl agent-bus ask` (blocking) or `starfleetctl agent-bus tell` to the
   sender.

3. **Report status proactively.** After any action taken on behalf of another
   ship, send a status update (`starfleetctl agent-bus tell <sender>`) so the fleet knows
   the state of play.

4. **Keep the board current.** Run `starfleetctl agent-bus status <state> [note]` after
   starting or finishing work so the fleet sees who is idle/working/blocked. When you
   start a substantive task, also report structured detail so the web console and
   `board --json` show progress at a glance:
   `starfleetctl agent-bus status working --task "<what>" --progress <0-100> --branch <b> --eta <dur> --blocker "<why, if any>"`.
   The detail is written to `status/<ship>.json` alongside the legacy heartbeat; omit
   flags you have no value for. Update `--progress`/`--blocker` as the task evolves.

### Command reference

All commands prefixed with `starfleetctl agent-bus`. Use `--json` on `board`/`inbox`/`msgs`/`asks` for machine-readable output.

| Command | Purpose |
|---------|---------|
| `cmd <agent> <verb> [args]` | Send a command (type=command) — executed by plugin, not injected as text |
| `status <state> ["note"]` | Set own heartbeat (idle/working/blocked + optional note) |
| `status <state> --task T --progress N --branch B --eta D --blocker X` | Set heartbeat plus structured detail (written to `status/<ship>.json`) |
| `board` | Show all ships and their status |
| `inbox` | List own unread directives (poller auto-injects these; manual call redundant in opencode) |
| `ack <id>` | Mark a message as handled |
| `tell <agent> <text…>` | Send a directive to one ship (type=ship) |
| `tell <agent> --reply <id> <text…>` | Reply to a specific message (sets In-Reply-To marker) |
| `broadcast <text…>` | Send a directive to all ships (type=ship) |
| `broadcast --reply <id> <text…>` | Broadcast a reply to a specific message |
| `ask "<question>"` | Ask the control agent a question (blocks until answered) |
| `reply <qid> <answer>` | Answer a pending question (control side) |
| `asks` | List pending questions (control side) |
| `msgs` | List all messages (control side) |
| `events [N]` | Show recent bus events |
| `clear` | Remove own heartbeat on exit |
| `prune` | Garbage-collect stale entries |

### Message format

Messages are JSON files stored in `msgs/<target>/unseen/`. Each file contains a single JSON object:

```json
{
  "id": "msg-abc123",
  "epoch": 1753190400,
  "iso": "2026-07-21T12:00:00Z",
  "from": "Enterprise",
  "target": "Voyager",
  "text": "model gpt-4o",
  "type": "command"
}
```

**Fields:**

| Field | Required | Description |
|-------|----------|-------------|
| `id` | yes | Unique message ID (`msg-<random>`) |
| `epoch` | yes | Unix timestamp |
| `iso` | yes | ISO 8601 timestamp |
| `from` | yes | Sender ship ID |
| `target` | yes | Recipient ship ID or `all` for broadcast |
| `text` | yes | Message body |
| `type` | yes | Message type (see below) |
| `reply_to` | no | ID of message being replied to |
| `attach` | no | Attachment filename if present |

**Message types (`type` field):**

| Type | Plugin behavior | CLI command |
|------|-----------------|-------------|
| `ship` | Injected as system prompt (directive) | `tell`, `broadcast` |
| `user` | Injected as system prompt (directive) | `tell`, `broadcast` |
| `control` | Injected as system prompt (directive) | `tell`, `broadcast` |
| `command` | Executed locally by plugin, NOT injected | `cmd` |

**Commands** (`type=command`) are executed by the opencode plugin's `handleMessage()` function. The `text` field contains the verb and optional arguments:

| Verb | Args | Effect |
|------|------|--------|
| `model` | `<model-name>` | Switch session model (e.g. `model gpt-4o`) |
| `quit` | — | Gracefully shut down the session |
| `reset` | — | Clear session conversation |
| `status` | — | Report status back to sender |

**Examples:**

```sh
# Send a command (executed by plugin, not injected as text)
starfleetctl agent-bus cmd Voyager model gpt-4o
starfleetctl agent-bus cmd Voyager quit
starfleetctl agent-bus cmd Voyager reset

# Send a directive (injected as system prompt)
starfleetctl agent-bus tell Voyager "please refactor the auth module"
starfleetctl agent-bus broadcast "roll call — all ships report status"
```

### Large payloads — use `--stdin`, not argv

`agent-bus tell` / `broadcast` deliver the message body either as command-line
arguments (`tell <agent> <text…>`) or, to bypass the OS `ARG_MAX` limit
(~128 KB–2 MB, varies per distro) that constrains argv-based delivery, read it
from **stdin**:

```sh
# short one-liner — argv is fine
starfleetctl agent-bus tell Voyager "status report: build green"

# reply to a specific message (id from `msgs`/`inbox --json` or the stdout of tell)
starfleetctl agent-bus tell Voyager --reply m0137 "danke, merged"
# the web console shows threaded views; each message links its referenced id (↩).

# multi-line or payloads with special characters — pipe via stdin
cat <<'EOF' | starfleetctl agent-bus tell Yamato --stdin
Einsatzbefehl: PR-Review-Batch. Führe bot-review für diese PRs durch:

1. #3323 (xfree86: remove xf86validateConfig license from Files.c)
2. #3322 (xfree86: remove obsolete keywords and license from Files.c)

Nutze den /bot-review Skill. Poste Review-Kommentare + Labels.
Melde Status nach Abschluss.
EOF
```

The storage layer itself has **no** size limit (verified at 20 MB+); only the
argv path is bounded by the kernel. **Prefer `--stdin` for anything beyond a
single short one-liner** — argv truncation can silently cut multi-line messages
even well below the theoretical `ARG_MAX` due to shell quoting overhead and
encoding expansion.

### Control agent ("1st officer") model

The default peer-to-peer model works for autonomous ship-to-ship communication.
When a **human** needs to centrally steer workers and approve their tool calls,
use the **control agent** model:

- **Control agent** — a human-attended session, conventionally
  `STARFLEET_SHIP_ID=control` (overridable via `$AGENT_CONTROLLER`).
  Runs `.starfleet-ai/bin/starfleetctl agent-bus board` to watch the fleet and
  `.starfleet-ai/bin/starfleetctl agent-bus asks` to see pending questions.
- **Workers** — every other session. They route questions and tool-permission
  prompts to the control agent and block locally for the answer.

**Quickstart** — in the session you want to man as controller:

```sh
export STARFLEET_SHIP_ID=control
.starfleet-ai/bin/starfleetctl agent-bus board   # who's online
.starfleet-ai/bin/starfleetctl agent-bus asks    # pending questions
```

When a worker asks something, answer it:

```sh
.starfleet-ai/bin/starfleetctl agent-bus reply <qid> "your answer"
.starfleet-ai/bin/starfleetctl agent-bus reply <qid> allow   # [perm] request
.starfleet-ai/bin/starfleetctl agent-bus reply <qid> deny    # [perm] request
```

Any session can ask the controller:

```sh
.starfleet-ai/bin/starfleetctl agent-bus ask "should I force-push?"
```

**Tool-permission forwarding** — wire a worker's `PreToolUse` hook so every
`Bash` permission prompt routes to the controller instead of blocking the
worker. Add to that worker's `.claude/settings.local.json` (never to the
shared `settings.json` — an absent controller would gate every session):

```json
"hooks": { "PreToolUse": [ { "matcher": "Bash",
  "hooks": [ { "type": "command", "timeout": 120,
    "command": "\"$CLAUDE_PROJECT_DIR\"/.claude/hooks/agent-permission-hook" } ] } ] }
```

Fail-safe: Claude Code's own hook timeout **fails open** (tool proceeds), so
the hook enforces its **own shorter** timeout and returns first:
- `$AGENT_PERM_TIMEOUT` (default 60s; keep below the hook's `timeout`),
- `$AGENT_PERM_TIMEOUT_DECISION` = `deny` (default, fail-closed) | `ask`,
- `$AGENT_CONTROLLER` (default `control`).

The hook cannot override a `deny`/`ask` permission *rule* (most-restrictive
wins), so it widens nothing — it only answers what would otherwise be a prompt.
