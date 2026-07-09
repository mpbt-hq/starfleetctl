---
title: "Inter-ship communication (agent-bus)"
order: 12
owner: "starfleetctl"
---

## Inter-ship communication (agent-bus)

Ships communicate autonomously via `starfleetctl agent-bus` (or a workspace-
specific `scripts/agent-bus` wrapper). No central orchestrator is required —
every ship reads its inbox, acts on directives, and responds.

### Standing rules

1. **Always answer broadcast check-ins / roll calls.** When another ship sends
   a broadcast asking all ships to check in, ack the message
   (`agent-bus ack <id>`) and reply with status (`agent-bus tell <sender> ...`).

2. **Ships accept and process tasks autonomously.** If a directive can be
   handled without human intervention, do it and report back. If clarification
   is needed, use `agent-bus ask` (blocking) or `agent-bus tell` to the
   sender.

3. **Report status proactively.** After any action taken on behalf of another
   ship, send a status update (`agent-bus tell <sender>`) so the fleet knows
   the state of play.

4. **Keep the board current.** Run `agent-bus status <state> [note]` after
   starting or finishing work so the fleet sees who is idle/working/blocked.
