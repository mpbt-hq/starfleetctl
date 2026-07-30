---
name: starfleet
description: "Fleet coordination — comms, concurrency, task capture, and starfleetctl CLI. Load when handling inter-ship messages, setting up parallel work, capturing tasks, or running starfleetctl commands."
---

# starfleet — fleet coordination

Fleet-wide coordination for concurrent AI-agent sessions ("ships"). Covers inter-ship
communication, concurrency/isolation, task capture, and the starfleetctl CLI.

## Inter-ship communication (comms)

Ships communicate via `starfleetctl comms`. Messages arrive automatically via system prompt
injection — **never call `comms inbox` manually** (the poller already injects unseen messages).

### Comms rules

1. **Always answer.** When a `tell` or `ask` arrives, reply via `starfleetctl comms tell <sender>`.
   Silence means the message was lost.
2. **Ack after responding.** `starfleetctl comms ack <id>` to clear from inbox.
3. **Comms questions → answer in two places:** via `comms tell` (so sender gets it) AND on the
   local console (so the human can see it).
4. **Keep the board current.** Set status after starting or finishing work.
5. **Never call `comms --help`.** The full reference is in the `starfleetctl` skill or this document.

### Key comms subcommands

| Command | Purpose |
|---|---|
| `comms tell <ship> "<msg>"` | Send a directive to a ship |
| `comms tell <ship> --stdin` | Send a large payload via stdin |
| `comms broadcast "<msg>"` | Send to all ships |
| `comms ask "<question>"` | Ask a question (async reply expected) |
| `comms ack <id>` | Acknowledge/remove a message from inbox |
| `comms board` | Show fleet status board |
| `comms board --json` | Machine-readable board (for scripts) |
| `comms status <status>` | Set own status (idle/working/blocked) |

## Concurrency / isolation

Ships must not work in the same git working tree simultaneously. Use separate clones
or worktrees. PR-branch ownership: every clone pushes to the **same GitHub PR branch** —
use `starfleetctl github pr claim` before mutating a PR.

### Key rules

- **Different working trees cannot clobber each other.** Parallelize across independent workspaces.
- **The hazard:** two actors mutating the **same** clone at once.
- **PR-branch ownership:** Use `starfleetctl github pr claim` before mutating a PR.

### Quick reference

```bash
starfleetctl github pr make <commit> [<commit> ...]   # submit PR from incubator
starfleetctl github pr mk-agent-clone <branch> [name] # agent-owned clone
starfleetctl github pr claim <pr#> "what"              # claim PR branch
starfleetctl github pr claim --release <pr#>           # release claim
starfleetctl github backport commit <release> <commit> # one-shot backport
.starfleet-ai/bin/starfleetctl worktree add <repo> [name] # per-task worktree
starfleetctl with-clone-lock <cmd...>                  # serialize mutating work
```

## Task capture

Record a task in the dashboard, optionally commission a ship. **This only commands —
never execute the task yourself.** No direct file access to `DASHBOARD.md` or
`dashboard/topics/*.md`.

### The one-liner

```sh
starfleetctl task capture --title "<title>" \
    [--desc "<what needs doing>"] \
    [--slug "<override>"] \
    [--assign [<ship>]] \
    [--no-push]
```

- `--assign` (no name) → picks first idle, non-stale ship
- `--assign <ship>` → that specific ship
- Without `--assign` → recorded as open, no ship

### Managing existing tasks

```sh
starfleetctl task assign <slug> [<ship>] [--no-push]   # re-assign
starfleetctl task unassign <slug> [--no-push]          # clear assignment
starfleetctl task status <slug> <status> [--no-push]   # set status
starfleetctl task rm <slug> [--no-push]                # delete a task topic
starfleetctl task purge [--no-push]                    # delete ALL done tasks
```

## Dashboard

The dashboard is the cross-session "what's in flight" index. All access via CLI —
**never** `Read`/`Edit`/`Write`/`Glob`/`Grep` on `DASHBOARD.md` or `dashboard/topics/*.md`.

### Topic file format (frontmatter)

Topic files in `dashboard/topics/<slug>.md` use YAML-ish frontmatter:

```yaml
---
title: "My Task"
category: active       # "active" or "parked"; defaults to "active" when missing
kind: "task"           # optional; marks it as a schedulable task
status: "open"         # active only: open/assigned/done/...
assigned-to: "—"       # ship name or "—"
tags: "starfleet"      # optional, comma-separated
---
```

Only `title` is required — `category` defaults to `active`, all other fields
are optional. This is intentional for hand-written topics: you can drop in a
minimal file and it shows up in the active list automatically.

| Command | Purpose |
|---|---|
| `dashboard list` | Show active topics |
| `dashboard topic new <slug>` | Create a topic |
| `dashboard topic write <slug> <file>` | Write topic content |
| `dashboard topic commit <slug>` | Commit + push topic |
| `dashboard reindex` | Refresh DASHBOARD.md index |
| `dashboard commit` | Commit + push index |

## Reports

Submit and query structured fleet reports (test results, build summaries, CI
status, etc.) via CLI or web UI. Each report has a title, optional subtitle,
Markdown body, tags, a dashboard task reference, and file attachments.

### Key commands

```sh
starfleetctl reports submit "Title" \
    --subtitle "one-liner" \
    --body "Markdown body text" \
    --body-file path/to/log \
    --tags "ci,build" \
    --task-ref xlibre/some-task \
    --attachment path/to/file

starfleetctl reports list                          # newest first
starfleetctl reports list --ship Enterprise        # by ship
starfleetctl reports list --tag ci --json          # filter + JSON
starfleetctl reports show <id>
starfleetctl reports delete <id>
```

Attachments are uploaded to the filestore (`file put` → `/api/store/<name>`).
See `doc/reports.md` for full reference and web UI walkthrough.

## Starfleetctl CLI

A Go CLI for fleet coordination. Bootstrap: `./starfleet-bootstrap` (updates `.starfleet-ai/`).
Full reference: **`reference.md`** in this skill's directory.

### Subcommand overview

| Category | Commands |
|---|---|
| **Fleet** | `comms`, `dashboard`, `ws-commit`, `ship-names`, `with-clone-lock`, `worktree` |
| **Session** | `run`, `session list/attach/stop` |
| **Task** | `task capture/assign/unassign/status/rm/purge` |
| **Timer** | `timer set/list/cancel` |
| **Web** | `web start/stop/restart/autostart` |
| **Reports** | `reports submit/list/show/delete` |
| **Setup** | `genesis-init`, `self-install`, `agents install-starfleet` |
| **GitHub (read)** | `github pr view/ci/file-on-branch/wait-green/show-branch-file`, `github backport applies` |
| **GitHub (write)** | `github pr comment/label/set-body/checkout/amend-push/make`, `github backport commit` |
