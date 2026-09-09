---
slug: starfleet-instructions/timer-for-agents
title: "Timers for agents (polling / watch-loops)"
order: 21
owner: "starfleetctl"
---

<!-- Auto-installed by `starfleetctl sop install-starfleet` into sop.d/starfleet-instructions/timer-for-agents.md — do not hand-edit the installed copy; edit this source fragment in the starfleetctl repo instead. -->

## Timers for agents — poll instead of blocking watch-loops

Don't block a session with `sleep`/watch-loops (e.g. waiting for a GitHub Actions
run). Set a **timer** instead: it fires a comms directive into the fleet at the
scheduled time, and the poller injects it into the owning ship's next turn — no
background process, no blocking, survives a session restart.

Quick patterns:

- `timer set --every <interval> --type ship --text "…"` → **self-reminder** (no
  `--target`); the fired directive is attributed to the owner and carries a
  `[timer] ` prefix.
- `timer set --at "18:00"` / `--at "tomorrow 17:30"` / ISO → one-time.
- `--type command --text "model <x>"` → auto model-switch; `--type system` =
  operator tooling (don't set casually).
- Storage: default **ephemeral** under `.starfleet-ai/var/`; `--persistent`
  survives a reset (use for standing infra checks).
- **Cancel when done** (`timer cancel <id>`) — a recurring poll left running
  spams your own inbox forever. One timer per in-flight thing, keep `--desc`
  self-explanatory.

Full quick-reference, storage rules and patterns: load the **`starfleet-timer`**
skill (`timer set/list/cancel/pause/resume/clear` management included).