---
title: "starfleetctl — fleet management CLI reference"
order: 900
owner: "starfleetctl"
---

# starfleetctl — full CLI reference

Companion to the `starfleet` core skill. Thematic command tables live in the
sub-skills: **`starfleet-tasks`** (task+reports), **`starfleet-github`**
(github), **`starfleet-timer`** (timers), **`starfleet-sessions`**
(session/worktree/web). This page lists the remaining cross-cutting commands.

## Fleet coordination

| Subcommand | Purpose |
|---|---|
| `comms <cmd>` | Status board + directive bus (status/board/tell/broadcast/ack/inbox) |
| `dashboard <cmd>` | DASHBOARD.md read/write/commit cycle |
| `ws-commit -m <msg> <paths>` | Atomic commit+push under clone lock |
| `ship-names <cmd>` | Ship name registry (assign/release/list/gc/shell-env) |
| `with-clone-lock [cmd...]` | Serialize mutating work in a git working tree |

## Task scheduling & monitoring

| Subcommand | Purpose |
|---|---|
| `task sweep-stale` | Mark tasks of dead/stale ships as interrupted (batch) |
| `logs scan [--capture]` | Scan ship logs for recurring failures, extract as tasks |
| `task capture/assign/.../orphans` | Full task lifecycle — see `starfleet-tasks` skill |
| `timer set --at/--every/--cron ...` | Fleet scheduling — see `starfleet-timer` skill |
| `github pr <cmd>` / `github backport <cmd>` | GitHub interaction — see `starfleet-github` skill |

## Known limitations

- `comms monitor-loop`/`fleet-watch` known broken under Claude Code's `Monitor` tool (workaround: bash originals)
- `github backport commit` path-remap uses project config for prefix/behavior
- `github pr make` marker-leak bug fixed 2026-07-07