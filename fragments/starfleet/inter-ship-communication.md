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
and responds. See the `starfleetctl` skill for the full command reference.

### Standing rules

1. **Always answer broadcast check-ins / roll calls.** When another ship sends
   a broadcast asking all ships to check in, ack the message and reply
   with status.

2. **Ships accept and process tasks autonomously.** If a directive can be
   handled without human intervention, do it and report back. If clarification
   is needed, ask via comms.

3. **Report status proactively.** After any action taken on behalf of another
   ship, send a status update so the fleet knows the state of play.

4. **Keep the board current.** Set your status after starting or finishing
   work so the fleet sees who is idle/working/blocked.
