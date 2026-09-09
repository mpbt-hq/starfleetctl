---
name: starfleet
description: "Fleet coordination core — comms, board status, concurrency/isolation, and pointers to the themed starfleet-* skills. Load when handling inter-ship messages, checking the board, or unsure which starfleet skill applies."
---

# starfleet — fleet coordination core

Fleet-wide coordination for concurrent AI-agent sessions ("ships"). This is the **core**
skill: comms, the board, and concurrency/isolation. Thematic sub-skills cover the rest —
see [Routing](#routing) below for which one to load when.

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
5. **Never call `comms --help`.** The full interface lives in this skill and the
   `starfleetctl` reference.

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

## Routing

| Skill | Load when |
|---|---|
| **`starfleet-tasks`** | capturing/working/managing tasks, dashboard topics, reports |
| **`starfleet-github`** | PR status/CI, PR claims, submitting/repairing PRs, backports |
| **`starfleet-timer`** | setting/listing/canceling timers (polling instead of watch-loops) |
| **`starfleet-sessions`** | spawning/managing ships, git worktrees, web console |

## Starfleetctl CLI

A Go CLI for fleet coordination. Bootstrap: `./starfleet-bootstrap` (updates `.starfleet-ai/`).
Full command reference: **`reference.md`** in this skill's directory.