---
slug: starfleet/inter-ship-communication
title: "Inter-ship communication (comms)"
order: 12
owner: "starfleetctl"
---

<!-- Auto-installed by `starfleetctl agents install-starfleet` into agents.d/starfleet/inter-ship-communication.md — do not hand-edit the installed copy; edit this source fragment in the starfleetctl repo instead. -->

## Inter-ship communication (comms)

Ships communicate autonomously via `starfleetctl comms`. No central
orchestrator is required — every ship reads its inbox, acts on directives,
and responds.

### Standing rules

1. **Always answer broadcast check-ins / roll calls.** When another ship sends
   a broadcast asking all ships to check in, ack the message
   (`starfleetctl comms ack <id>`) and reply with status (`starfleetctl comms tell <sender> ...`).

2. **Ships accept and process tasks autonomously.** If a directive can be
   handled without human intervention, do it and report back. If clarification
   is needed, use `starfleetctl comms ask` (blocking) or `starfleetctl comms tell` to the
   sender.

3. **Report status proactively.** After any action taken on behalf of another
   ship, send a status update (`starfleetctl comms tell <sender>`) so the fleet knows
   the state of play.

4. **Keep the board current.** Run `starfleetctl comms status <state> [note]` after
   starting or finishing work so the fleet sees who is idle/working/blocked.

### Commands you will use

All commands prefixed with `starfleetctl comms`.

| Command | Purpose |
|---------|---------|
| `status <state> ["note"]` | Set own heartbeat (idle/working/blocked + optional note) |
| `board` | Show all ships and their status |
| `inbox` | List own unread directives |
| `ack <id>` | Mark a message as handled |
| `tell <agent> <text…>` | Send a directive to one ship |
| `tell <agent> --reply <id> <text…>` | Reply to a specific message |
| `broadcast <text…>` | Send a directive to all ships |
| `broadcast --reply <id> <text…>` | Broadcast a reply to a specific message |
| `ask "<question>"` | Ask the control agent a question (blocks until answered) |
| `reply <qid> <answer>` | Answer a pending question (control side) |
| `asks` | List pending questions (control side) |
| `events [N]` | Show recent bus events |

### Large payloads

For multi-line or large messages, pipe via stdin:

```sh
cat <<'EOF' | starfleetctl comms tell Yamato --stdin
multi-line message here
EOF
```

### Control agent model

When a **human** needs to centrally steer workers, use the **control agent** model:

- **Control agent** — `STARFLEET_SHIP_ID=control`, watches fleet via `board` and `asks`
- **Workers** — route questions to control agent via `ask`, block for answer

```sh
# As worker — ask controller
starfleetctl comms ask "should I force-push?"

# As controller — answer
starfleetctl comms reply <qid> "yes, proceed"
starfleetctl comms reply <qid> allow   # permit tool call
starfleetctl comms reply <qid> deny    # deny tool call
```
