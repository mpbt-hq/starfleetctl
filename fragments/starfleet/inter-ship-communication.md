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
