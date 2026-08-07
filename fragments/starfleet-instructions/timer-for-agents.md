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

### How it works

- `timer set` creates a timer (one-time `--at`, interval `--every`, or `--cron`).
- With `--type ship` and **no** `--target`, the directive is posted to **your own
  ship** at fire time — the poller then re-arms you. This is the core "remind me
  later" pattern.
- The timer worker runs independently of any session (daemon managed by
  `timer worker autostart`/`restart`), so the fire still happens even if your
  current turn/session has ended.

### Quick reference

```bash
# one-time reminder today at 18:00 UTC (self-reminder)
starfleetctl timer set --at "18:00" --type ship \
    --text "check CI status of PR #3491" --desc "PR #3491 CI check"

# or absolute / "tomorrow HH:MM" (also accepts ISO "2006-01-02T15:04:00Z")
starfleetctl timer set --at "tomorrow 17:30" --type ship \
    --text "re-check CI status of PR #3491"

# recurring poll every 5 minutes until the run finishes
starfleetctl timer set --every 5m --type ship \
    --text "check CI run 31120345223 (PR #3491) and report status" \
    --desc "CI poll PR #3491"

# one-time at a wall-clock time (see --at formats above)
starfleetctl timer set --at "18:00" --type ship --text "status?"

# command timer (e.g. auto model-switch at a later point)
starfleetctl timer set --every 5m --type command --text "model <model-name>"

# management
starfleetctl timer list                  # your timers
starfleetctl timer list --all            # all timers (fleet-wide)
starfleetctl timer list --json           # machine-readable
starfleetctl timer pause <id>            # disable without deleting
starfleetctl timer resume <id>           # re-enable
starfleetctl timer cancel <id>           # remove
starfleetctl timer clear                 # remove all of your timers
```

### Storage

- Default (`--every`/`--at`): **ephemeral** under `.starfleet-ai/var/` — cleaned up
  on workspace reset.
- `--persistent`: stored under `.starfleet-ai/` — survives reset.
- Pick `--persistent` for standing infrastructure checks, `--ephemeral` for
  task-scoped polling.

### Patterns & rules of thumb

1. **Prefer `--every <interval>` + self-target over a `sleep` loop.** The fire
   posts a directive; the poller injects it; you just process the status. No
   `watch`, no `sleep 300 && curl …`.
2. **Cancel the timer when the job is done.** A recurring poll left running spams
   your own inbox forever (`timer cancel <id>` after success).
3. **One timer per in-flight thing.** Keep `--desc` descriptive so `timer list`
   is self-explanatory; you can have several polls in parallel.
4. **Fleet-wide monitoring:** `--target fleet` / `--target fleet-all` broadcasts
   a directive to other ships — use for cross-ship status checks, not for your
   own private polling.
5. **System timers** (`--type system --cmd reindex|web|web restart`) are
   operator/deployment tooling — don't set them casually; they run in the worker
   and affect the whole fleet infrastructure.
6. **`--tz`** affects only display; the worker computes fire times in UTC.
