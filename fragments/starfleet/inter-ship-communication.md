---
title: "Inter-ship communication (agent-bus)"
order: 12
owner: "starfleetctl"
---

<!-- Auto-installed by `starfleetctl agents install-starfleet` into agents.d/starfleet/inter-ship-communication.md — do not hand-edit the installed copy; edit this source fragment in the starfleetctl repo instead. -->

## Inter-ship communication (agent-bus)

Ships communicate autonomously via `starfleetctl agent-bus` (or a workspace-
specific `.bin/starfleetctl agent-bus` wrapper). No central orchestrator is required —
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
   starting or finishing work so the fleet sees who is idle/working/blocked.

### Large payloads — use `--stdin`, not argv

`agent-bus tell` / `broadcast` deliver the message body either as command-line
arguments (`tell <agent> <text…>`) or, to bypass the OS `ARG_MAX` limit
(~128 KB–2 MB, varies per distro) that constrains argv-based delivery, read it
from **stdin**:

```sh
# small message — argv is fine
starfleetctl agent-bus tell Voyager "status report: build green"

# large message (logs, diffs, long briefings) — pipe via stdin
cat briefing.txt | starfleetctl agent-bus tell Voyager --stdin
tar -tzf artifacts.tar | starfleetctl agent-bus broadcast --stdin
```

The storage layer itself has **no** size limit (verified at 20 MB+); only the
argv path is bounded by the kernel. Prefer `--stdin` for anything bigger than
~100 KB so the send can't fail with `E2BIG`.

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
