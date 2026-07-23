# starfleetctl — User Guide

**One binary to coordinate AI-agent sessions working on the same codebase.**

This guide covers installation, core concepts, daily workflows, and troubleshooting for users running starfleetctl in their workspace.

---

## Table of Contents

1. [Installation](#1-installation)
2. [Core Concepts](#2-core-concepts)
3. [Quick Start](#3-quick-start)
4. [Daily Workflows](#4-daily-workflows)
5. [Subcommand Reference](#5-subcommand-reference)
6. [Web UI](#6-web-ui)
7. [Troubleshooting](#7-troubleshooting)

---

## 1. Installation

### Option A: Build from Source (Recommended)

```sh
git clone https://github.com/metux/starfleetctl
cd starfleetctl
make build              # produces ./starfleetctl binary
./starfleetctl --version  # verify
```

**Requirements:** Go 1.21+ (stdlib only, no external dependencies).
**Build is always via `make` — `go build` and `go install` are not supported.**

### Option B: Use the Bootstrap Script (Workspace Setup)

If you're joining an existing workspace that already has `starfleet-bootstrap`:

```sh
cd /path/to/your/workspace
./starfleet-bootstrap     # installs starfleetctl, sets up configs, symlinks binary
```

This is **idempotent** — safe to run repeatedly for updates.

---

## 2. Core Concepts

### Ships (Agent Sessions)

Each AI agent session = one **ship** with a unique name:

| Role | Script | Identity | Purpose |
|------|--------|----------|---------|
| **Flagship** | `run-opencode.flagship` | Fixed (e.g., `Enterprise`) | Control agent — receives questions, approves tool calls, steers fleet |
| **Ship** | `run-opencode.ship` | Auto-assigned (e.g., `Voyager`) | Worker — executes tasks, reports to flagship |
| **Background Ship** | Web UI / `session ship-run` | Auto-assigned | Detached terminal, survives launching session |

**Environment variables** (set by launcher scripts):

- `STARFLEET_SHIP_ID` — your ship name
- `STARFLEET_ROLE` — `flagship` or `ship`
- `STARFLEET_TARGET` — flagship to report to (unset for flagship)
- `STARFLEET_BUS_DIR` — state directory (default: `.starfleet-ai/var/agent-bus`)

### Agent Bus — Cross-Session Communication

File-based pub/sub system in `.starfleet-ai/var/agent-bus/`:

- **Heartbeats** — each ship writes `status/<ship>.json` every few seconds
- **Messages** — JSON files in `msgs/<target>/unseen/` (auto-moved to `seen/` on ack)
- **Acknowledgments** — messages move from `unseen/` to `seen/<ship>/` on ack
- **Locking** — all writes go through `flock(2)` on `.lock` (bash & Go interoperable)

#### Message Format

Messages are JSON files:

```json
{
  "id": "msg-abc123",
  "epoch": 1753190400,
  "iso": "2026-07-21T12:00:00Z",
  "from": "Enterprise",
  "target": "Voyager",
  "text": "model gpt-4o",
  "type": "command"
}
```

**Message types:**

| Type | Behavior | CLI |
|------|----------|-----|
| `ship` / `user` / `control` | Injected as system prompt (directive) | `tell`, `broadcast` |
| `command` | Executed by plugin, NOT injected | `cmd` |

**Commands** (`type=command`) — executed by opencode plugin:

| Verb | Args | Effect |
|------|------|--------|
| `model` | `<model-name>` | Switch session model |
| `quit` | — | Shut down session |
| `reset` | — | Clear conversation |
| `status` | — | Report status to sender |

```sh
# Commands (executed, not injected)
starfleetctl comms cmd Voyager model gpt-4o
starfleetctl comms cmd Voyager quit

# Directives (injected as system prompt)
starfleetctl comms tell Voyager "run tests"
starfleetctl comms broadcast "roll call"
```

### PR Claims — Advisory Branch Locking

```sh
starfleetctl pr-claim 3162 "fixing CI"
# ... work on PR #3162 ...
starfleetctl pr-claim --release 3162
```

Claims are **cooperative** — they don't block git at filesystem level, but all participating agents check claims before pushing.

### Dashboard — Project Status Tracking

Markdown-based topic tracking in `.starfleet-ai/var/DASHBOARD.md` + `.starfleet-ai/var/dashboard/topics/*.md`. Managed via `starfleetctl dashboard topic <cmd>`.

---

## 3. Quick Start

### 3.1 Bootstrap a New Workspace

```sh
cd /path/to/your/repo
starfleetctl genesis-init .
```

This creates `starfleet-bootstrap` — commit this file. On fresh clones, teammates just run `./starfleet-bootstrap`.

### 3.2 Start the Flagship (Control Agent)

```sh
./run-opencode.flagship
```

### 3.3 Start Worker Ships

```sh
# Auto-assign name
./run-opencode.ship

# Or specific name
./run-opencode.ship --name Voyager
```

### 3.4 Verify Fleet is Running

```sh
starfleetctl comms board
```

You should see `Enterprise` (flagship) and your worker ships.

---

## 4. Daily Workflows

### 4.1 Posting Status

```sh
starfleetctl comms status working "implementing feature X"
starfleetctl comms status blocked "waiting for review on PR #3142"
starfleetctl comms status idle
starfleetctl comms touch      # refresh heartbeat without changing state
starfleetctl comms clear      # call on session exit
```

### 4.2 Sending Messages

```sh
# Direct message
starfleetctl comms tell Voyager "run tests on branch feature-x"

# Ship names with spaces need quotes
starfleetctl comms tell 'Wild Mary' "check status"

# Broadcast to all
starfleetctl comms broadcast "build broken, hold off pushes"

# Large payloads via stdin
cat big-report.txt | starfleetctl comms tell Voyager --stdin
```

### 4.3 Receiving Messages

```sh
# Check inbox
starfleetctl comms inbox

# Acknowledge (removes from inbox)
starfleetctl comms ack m0042
starfleetctl comms ack m0042 "done, tests pass"

# Get large attachment
starfleetctl comms get m0042 --out report.txt
```

### 4.4 Asking Questions (Blocking)

```sh
# Ask the flagship (blocks until reply)
starfleetctl comms ask "force-push to fix history?"

# Custom controller & timeout
starfleetctl comms ask "approve PR?" --to control --timeout 60
```

**Flagship side:**
```sh
starfleetctl comms asks          # list pending questions
starfleetctl comms reply m0042 "yes, proceed"
```

### 4.5 Safe Commits (Serialized Git Operations)

```sh
# Acquires flock, commits, pushes atomically
starfleetctl ws-commit -m "fix: resolve race in parser" src/parser.c
```

### 4.6 PR Branch Locking

```sh
starfleetctl pr-claim 3162 "fixing flaky test"
# work on PR #3162...
starfleetctl pr-claim --release 3162

# Check claims
starfleetctl pr-claim --list
starfleetctl pr-claim --who 3162
```

### 4.7 Launching Background Ships

```sh
# From CLI
starfleetctl session ship-run --name Voyager
starfleetctl session ship-run --name Voyager --model opencode/big-pickle

# Or via Web UI: "Neues Schiff" form on Flotte tab
```

### 4.8 Managing Sessions

```sh
# List running termctl terminals
starfleetctl session attach --list

# Attach to a session (shared read-write)
starfleetctl session attach Voyager

# Stop a session (clears heartbeat, releases name)
starfleetctl session stop Voyager
```

### 4.9 Ship Names

```sh
starfleetctl ship-names assign            # auto-assign
starfleetctl ship-names assign flagship   # claim as flagship
starfleetctl ship-names list
starfleetctl ship-names release Voyager
starfleetctl ship-names gc                # garbage-collect stale
```

### 4.10 Worktrees (Isolated Checkouts)

```sh
starfleetctl worktree add      # create throwaway worktree
starfleetctl worktree list
starfleetctl worktree remove <branch>
```

---

## 5. Subcommand Reference

### Fleet Coordination

| Command | Purpose |
|---------|---------|
| `comms` | Status board + cross-session messaging |
| `dashboard` | Project topic tracking |
| `pr-claim` | Advisory PR-branch locks |
| `ws-commit` | Atomic commit+push under lock |
| `ship-names` | Session identity registry |
| `session` | Agent session lifecycle (termctl) |
| `with-clone-lock` | Serialize git mutations |

### GitHub (Read-Only)

| Command | Purpose |
|---------|---------|
| `pr-view` | PR metadata |
| `pr-ci` | CI status (failure-classified) |
| `show-branch-file` | File at any branch ref |
| `backport-applies` | Cross-branch applicability check |

### GitHub (Mutating)

| Command | Purpose |
|---------|---------|
| `pr-comment` | Post PR comment |
| `pr-label` | Add/remove labels |
| `pr-checkout` | Isolated PR clone |
| `xx-make-pr` | Create PR with conventions |

### Utilities

| Command | Purpose |
|---------|---------|
| `bootstrap` | Verify/fix workspace structure |
| `genesis-init` | Bootstrap from nothing |
| `self-install` | Clone/build/install updates |
| `json` | JSON validate/pretty/get |
| `web` | Fleet web UI (start/stop/autostart/restart) |

---

## 6. Web UI

```sh
starfleetctl web start [--addr :8080]
```

Open `http://localhost:8080` — single-page app with tabs:

- **Flotte** — live status board, launch new ships
- **Tasks** — create/assign/track project tasks
- **Bus** — threaded messages, inbox, questions
- **Funk** — send messages via dropdown
- **Log** — real-time event feed

**Subcommands:**
```sh
starfleetctl web              # show help
starfleetctl web start        # foreground
starfleetctl web stop         # stop daemon
starfleetctl web autostart    # start daemon (if not running)
starfleetctl web restart      # stop + autostart
```

---

## 7. Troubleshooting

### 7.1 "No workspace found" / "Cannot find .starfleet-ai"

```sh
# Ensure you're in a bootstrapped workspace
ls -la .starfleet-ai/     # should exist

# Or run bootstrap to fix
./starfleet-bootstrap
```

### 7.2 Ships Not Appearing on Board

```sh
# Check heartbeat directory
ls -la .starfleet-ai/var/agent-bus/status/

# Verify STARFLEET_BUS_DIR is consistent across sessions
echo $STARFLEET_BUS_DIR

# Prune stale entries
starfleetctl comms prune
```

### 7.3 "Ship name already in use"

```sh
starfleetctl ship-names list        # see assigned names
starfleetctl ship-names release <name>  # force-release
starfleetctl ship-names gc          # auto-clean stale (> STARFLEET_STARFLEET_BUS_TTL)
```

### 7.4 PR Claim Conflicts

```sh
starfleetctl pr-claim --list        # see all claims
starfleetctl pr-claim --steal 3162  # take over (with reason)
```

### 7.5 Web UI Not Loading / Port in Use

```sh
starfleetctl web start --addr :8081   # different port
starfleetctl web stop                  # kill daemon
```

### 7.6 opencode Plugin Not Delivering Messages

- Ensure `.opencode/plugin/starfleet-dispatch.ts` exists (installed by bootstrap)
- Check opencode plugin logs in opencode UI
- Verify `STARFLEET_BUS_DIR` matches between CLI and plugin

### 7.7 Go vs Bash Interoperability Issues

- Both **must** use same `STARFLEET_BUS_DIR` (default `.starfleet-ai/var/agent-bus`)
- Both use same `flock` on `.lock` — don't mix custom lock paths
- Run `starfleetctl comms prune` periodically

### 7.8 Agent Bus Monitor Loop Not Seeing New Messages

**Known limitation:** The Go `monitor-loop`/`fleet-watch` commands don't detect messages arriving while running under Claude Code's `Monitor` tool.

**Workarounds:**
- Use bash originals: `scripts/agent-bus-monitor-loop`, `scripts/agent-bus-fleet-watch`
- Use opencode's plugin-based polling (auto-installed by bootstrap)

---

## Appendix: Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `STARFLEET_SHIP_ID` | `user@hostname` | Unique ship identifier |
| `STARFLEET_ROLE` | — | `flagship` or `ship` |
| `STARFLEET_TARGET` | — | Flagship ship ID (for ships) |
| `STARFLEET_BUS_DIR` | `.starfleet-ai/var/agent-bus` | Agent bus state directory |
| `STARFLEET_STARFLEET_BUS_TTL` | `900` (15 min) | Heartbeat TTL in seconds |
| `PROJECT` | — | Project label on board |
| `AGENT_CONTROLLER` | `control` | Control agent for `ask`/`reply` |
| `MPBT_WORKSPACE_ROOT` | auto-detect | Workspace root override |

---

## Appendix: File Layout

```
workspace/
├── starfleet-bootstrap          # ← commit this
├── .starfleet-ai/
│   ├── var/
│   │   ├── agent-bus/
│   │   │   ├── .lock            # flock domain
│   │   │   ├── .seq             # message counter
│   │   │   ├── status/          # heartbeats (Enterprise.json, Voyager.json)
│   │   │   ├── msgs/            # messages by target
│   │   │   │   ├── Enterprise/unseen/  # incoming for Enterprise
│   │   │   │   ├── Enterprise/seen/    # acked by Enterprise
│   │   │   │   ├── Voyager/unseen/
│   │   │   │   └── all/unseen/         # broadcast inbox
│   │   │   ├── attachments/     # large payloads
│   │   │   └── events.log       # audit trail
│   │   ├── agent-claims/        # PR claims (pr-3162.tsv, ...)
│   │   ├── agents.d/            # fleet coordination fragments
│   │   ├── DASHBOARD.md         # project status index
│   │   ├── dashboard/
│   │   │   └── topics/          # project tasks
│   │   ├── log/                 # centralised logs (web.log, timer-worker.log)
│   │   └── ships/               # session pipes + logs
│   ├── conf/
│   │   ├── models.yaml          # model registry for web UI
│   │   └── timers/              # persistent timers
│   └── web.pid                  # web server PID (when daemonised)
├── run-opencode.flagship        # launcher (flagship)
└── run-opencode.ship            # launcher (worker)
```

---

## Further Reading

| Document | Description |
|----------|-------------|
| [README.md](README.md) | Project overview & quick reference |
| [doc/architecture.md](doc/architecture.md) | Internal architecture & data flow |
| [doc/comms.md](doc/agent-bus.md) | Comms command reference |
| [doc/session.md](doc/session.md) | Session & worktree management |
| [doc/pr-claim.md](doc/pr-claim.md) | PR locking details |
| [doc/web-ui.md](doc/web-ui.md) | Web UI deep dive |
| [doc/known-limitations.md](doc/known-limitations.md) | Current caveats |

---

**License:** AGPL-3.0-or-later — see [LICENSE](LICENSE)