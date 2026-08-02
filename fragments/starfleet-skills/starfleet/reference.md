---
title: "starfleetctl — fleet management CLI reference"
order: 900
owner: "starfleetctl"
---

# starfleetctl — full CLI reference

## Deployment

```bash
# Phase A: genesis (from an existing binary)
starfleetctl genesis-init .

# Phase B: bootstrap (from the committed script)
./starfleet-bootstrap
```

Everything under `.starfleet-ai/` is gitignored. Re-run `./starfleet-bootstrap` anytime to update.

## Fleet coordination

| Subcommand | Purpose |
|---|---|
| `comms <cmd>` | Status board + directive bus (status/board/tell/broadcast/ack/inbox) |
| `dashboard <cmd>` | DASHBOARD.md read/write/commit cycle |
| `github pr claim <cmd>` | Advisory PR-branch lock + work log |
| `ws-commit -m <msg> <paths>` | Atomic commit+push under clone lock |
| `ship-names <cmd>` | Ship name registry (assign/release/list/gc/shell-env) |
| `with-clone-lock [cmd...]` | Serialize mutating work in a git working tree |
| `worktree add/list/remove/prune` | Git worktree management |

## Session management

| Subcommand | Purpose |
|---|---|
| `run [--flagship\|--name <id>] [--client claude\|opencode]` | Start an AI ship session |
| `session list` | List running detached sessions |
| `session attach <id>` | Attach terminal to a detached session |
| `session stop <id>` | Kill a detached session + release ship name |

## Task scheduling & monitoring

| Subcommand | Purpose |
|---|---|
| `task capture --title "..." --desc "..."` | Capture a task into the dashboard (+ optional ship commission) |
| `task assign <slug> [<ship>]` | Assign a task to a ship |
| `task unassign <slug>` | Clear a task assignment |
| `task status <slug> <status>` | Set a task's status |
| `task rm <slug>` | Delete a task topic from the dashboard |
| `task purge [--no-push]` | Delete ALL tasks with status "done" |
| `timer set --at/--every/--cron ...` | Fleet scheduling (one-time, interval, cron) |
| `timer list/cancel` | List or cancel timers |
| `logs scan [--capture]` | Scan ship logs for recurring failures, extract as tasks |

## Web console & setup

| Subcommand | Purpose |
|---|---|
| `web start/stop/restart/autostart` | Fleet web console (mobile-first) |
| `genesis-init [dir]` | Bootstrap a workspace from nothing |
| `self-install` | Clone/pull + build + symlink starfleetctl |
| `sop install-starfleet` | Install/update SOP fragments and skills |

## GitHub interaction (read-only)

| Subcommand | Purpose |
|---|---|
| `github pr view <pr#>` | PR metadata via gh |
| `github pr ci <pr#\|URL>` | CI status classified by conclusion |
| `github pr file-on-branch <branch> <path>` | Fetch a file from any branch/tag/commit |
| `github pr wait-green <pr#>` | Poll CI checks until all pass/fail/timeout |
| `github pr job-logs <pr#>` | Download CI job logs for failure analysis |
| `github pr show-branch-file <ref> <path>` | Print file at any ref via GitHub API (deprecated, use file-on-branch) |
| `github backport applies <path> <grep-ERE> [release...]` | Check applicability across release lines |

## GitHub interaction (mutating)

| Subcommand | Purpose |
|---|---|
| `github pr comment <pr#> <body-file>` | Post PR comment |
| `github pr label <pr#> add\|remove` | Add/remove labels |
| `github pr set-body <pr#> <body-file>` | Replace PR body |
| `github pr checkout <pr#>` | Isolated clone for PR repair |
| `github pr amend-push <clone-dir>` | Amend + force-push |
| `github backport commit <release> <commit>` | One-shot backport |
| `github pr make <commits>` | Submit PR from commits |

## Known limitations

- `comms monitor-loop`/`fleet-watch` known broken under Claude Code's `Monitor` tool (workaround: bash originals)
- `github backport commit` path-remap uses project config for prefix/behavior
- `github pr make` marker-leak bug fixed 2026-07-07
