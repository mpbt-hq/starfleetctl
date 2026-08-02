---
slug: starfleet-instructions/comms-opencode-polling
title: "Comms opencode polling"
order: 215
owner: "starfleetctl"
---

<!-- Auto-installed by `starfleetctl sop install-starfleet` into sop.d/starfleet-instructions/comms-opencode-polling.md — do not hand-edit the installed copy; edit this source fragment in the starfleetctl repo instead. -->

## Comms opencode polling

Messages arrive automatically via system prompt injection — no manual inbox
check needed. Handle them as you would any incoming directive: ack, act, or
defer.

### Rules for the assistant

1. **Never manually call `starfleetctl comms inbox`.** The poller already injects
   new messages into the next turn's context. Doing so wastes a turn and
   is redundant.

2. **Never call `starfleetctl comms --help`.** The full interface (tell, ack, ask,
   board, status, etc.) is documented in the `starfleet` skill. Load it when
   needed — don't call `--help`.

3. **On model-API errors, switch models.** If you receive a `zen-ratelimit` (usage
   cap) or `nim-overload` error, do **not** retry the same model — switch to a
   different provider/model and continue. The fleet console already sees the error.

4. **model directive.** Ships can switch models at runtime via comms:
   `starfleetctl comms cmd <ship> "model <model-name>"`. If you receive
   this directive, execute it and confirm the switch.
