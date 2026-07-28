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
starfleetctl github pr mk-agent-clone <branch> [name]  # agent-owned clone
starfleetctl github pr claim <pr#> "what you're doing"  # claim PR branch
starfleetctl github pr claim --release <pr#>            # release claim
.starfleet-ai/bin/starfleetctl worktree add <repo> [name]  # per-task worktree
starfleetctl with-clone-lock <cmd...>                   # serialize mutating work
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
```

## Dashboard

The dashboard is the cross-session "what's in flight" index. All access via CLI —
**never** `Read`/`Edit`/`Write`/`Glob`/`Grep` on `DASHBOARD.md` or `dashboard/topics/*.md`.

| Command | Purpose |
|---|---|
| `dashboard list` | Show active topics |
| `dashboard topic new <slug>` | Create a topic |
| `dashboard topic write <slug> <file>` | Write topic content |
| `dashboard topic commit <slug>` | Commit + push topic |
| `dashboard reindex` | Refresh DASHBOARD.md index |
| `dashboard commit` | Commit + push index |

## Starfleetctl CLI

A Go CLI for fleet coordination. Bootstrap: `./starfleet-bootstrap` (updates `.starfleet-ai/`).
Full reference: **`reference.md`** in this skill's directory.

### Subcommand overview

| Category | Commands |
|---|---|
| **Fleet** | `comms`, `dashboard`, `ws-commit`, `ship-names`, `with-clone-lock`, `worktree` |
| **Session** | `run`, `session list/attach/stop` |
| **Task** | `task capture/assign/unassign/status` |
| **Timer** | `timer set/list/cancel` |
| **Web** | `web start/stop/restart/autostart` |
| **Setup** | `genesis-init`, `self-install`, `agents install-starfleet` |
| **GitHub (read)** | `github pr view/ci/show-branch-file`, `github backport applies` |
| **GitHub (write)** | `github pr comment/label/set-body/checkout/amend-push/make`, `github backport commit` |
