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
7. [Reports](#7-reports)
8. [Troubleshooting](#8-troubleshooting)

---

## 1. Installation

### Option A: Build from Source (Recommended)

```sh
git clone https://github.com/metux/starfleetctl
cd starfleetctl
make build              # produces ./starfleetctl binary
./starfleetctl --version  # verify
```

**Requirements:** Go 1.21+ (stdlib only, no external dependencies). The
optional `check-plugin` step of `make all` additionally needs `esbuild` and —
for the type check — `typescript` + `@types/node` (`npm i -g typescript
@types/node`); both steps skip gracefully when absent.
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
- `STARFLEET_BUS_DIR` — state directory (default: `.starfleet-ai/var/comms`)

### Comms — Cross-Session Communication

File-based pub/sub system in `.starfleet-ai/var/comms/`:

- **Heartbeats** — each ship writes `status/<ship>.tsv` every few seconds
- **Messages** — TSV files in `msgs/` (auto-moved to `seen/` on ack)
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

Claims are **cooperative** — they don't block git at filesystem level, but all participating ships check claims before pushing.

### Dashboard — Project Status Tracking

Markdown-based topic tracking in `.starfleet-ai/var/DASHBOARD.md` + `.starfleet-ai/dashboard/topics/*.md`. Managed via `starfleetctl dashboard topic <cmd>`.

**⚠️ Correct command:** `starfleetctl dashboard topic list` — NOT `starfleetctl dashboard list` (does not exist).

### SOP Instruction Fragments

Per-topic Markdown files that become the agent's system prompt. Two sources:
user-maintained (`sop.d/`) and auto-installed starfleet-owned
(`.starfleet-ai/var/sop.d/starfleet-instructions/`). At reindex time,
all fragment bodies are inlined into `CLAUDE.md` and `index.md`.

```sh
starfleetctl sop new project/my-topic --title "My Topic"    # create
starfleetctl sop list                                        # list all
starfleetctl sop reindex                                     # regenerate derived files
```

See [doc/sop.md](doc/sop.md) for the full guide.

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
| `sop` | Manage SOP instruction fragments ([docs](doc/sop.md)) |
| `json` | JSON validate/pretty/get |
| `models` | Sync models.yaml from opencode catalog |
| `web` | Fleet web UI (start/stop/autostart/restart) |

---

## 6. Web UI

The Web UI is a browser-based fleet console for monitoring and controlling your agent fleet.

### Starting the Web UI

```sh
starfleetctl web start [--addr :8080]
```

Open `http://localhost:8080` — single-page app with tabs:

- **Flotte** — live status board, launch new ships
- **Tasks** — create/assign/track project tasks
- **Bus** — threaded messages, inbox, questions
- **Funk** — send messages via dropdown
- **Log** — real-time event feed
- **Sitzungen** — opencode session monitor (read-only from SQLite DB)
- **Timer** — fleet scheduling (ship/command/system timers)
- **Berichte** — fleet report system
- **Files** — workspace file browser

### Flotte (Status Board)

Shows every agent that has posted a heartbeat. Each card displays:

- **Agent name** (monospace)
- **State** pill: `idle` (dim), `working`/`building` (warn), `done` (ok), `blocked` (bad)
- **Project** and **age** (time since last heartbeat)
- **Inbox count** (unacked directives)
- **Task**, **Branch**, **Blocker**, **ETA**, **Progress bar** (if reported)
- **STALE** pill if the heartbeat is older than `STARFLEET_STARFLEET_BUS_TTL` (default 15 min)
- **Model** pill: shows the model ID or provider when reported

Click a ship card to open the **ship detail panel** (slide-in from the right):

- Full status details (project, task, progress, branch, blocker, ETA, note)
- Model and provider information
- Conversation history with that ship
- **Verlauf** tab: the ship's opencode session history (list of sessions whose title matches the ship name, clickable into the transcript modal)
- Compose and send a message directly to the ship

#### Launching a new ship

The "Neues Schiff" form at the top of the Flotte tab lets you launch a background ship directly from the browser:

- **Name** (optional): ship name; auto-assigned if left blank
- **Model** (dropdown): select from available models, grouped by provider. The dropdown is populated from `/api/models` (backed by `models.yaml`). The last-used model is remembered via `localStorage`.
- **Provider** (dropdown): auto-set when a model is selected; can be overridden manually. Options: openai, anthropic, google, nvidia, mistral, meta.
- **Parent** (optional): parent ship for hierarchical ordering

Model registry is generated from `opencode models --verbose` via the `gen-models-yaml` script (see [Model Registry](#model-registry) below).

### Tasks

Project task tracking (backed by `dashboard/topics/*.md`).

- **Create** a new task with title, description, and optional assignment
- **Change status** (open → assigned → in-progress → done → parked)
- **Unassign** a task to return it to the pool

### Bus

Cross-agent messaging. Three sub-tabs:

| Sub-tab | Description |
|---|---|
| **Directives** | All messages on the bus (newest first) |
| **Inbox** | Messages addressed to your ship (or broadcasts) |
| **Questions** | Unanswered `[ask]` questions addressed to your ship |

Features:
- **Thread view** toggle: groups messages by `reply_to` parent
- **Target pills**: shows who each message is addressed to
- **Ack indicators**: `✓` (acked) or `…` (pending) for questions
- Click a message ID to jump to it (in thread view)

### Funk

Send a message to any agent or broadcast to the entire fleet.

- Select target from dropdown (populated from live board)
- Type message and click "Senden"
- Uses the real comms (`comms tell` / `comms broadcast`)

### Log

Live event feed from the comms audit log. Shows the last N events (default 20, configurable via `?n=`). Auto-refreshes every 15 seconds.

### Sitzungen (Sessions)

Monitors opencode sessions **regardless of how they were launched** — the table is read directly (read-only) from the opencode SQLite database (`~/.local/share/opencode/opencode.db`, resolved via `opencode db path`), so it also covers sessions started outside a termctl terminal.

- **Session list**: most recently updated first; each card shows ship name, mode (`build`/`plan`/`explore`/`general`), model, last-update time, token usage and cost. A `▶ running` badge flags sessions whose title matches a live board entry.
- **Filter**: by ship name (`?title=`) and mode (`?agent=`), limit 100.
- **Detail modal** (click a session): transcript meta plus the last 50/200 messages in chronological order. User/assistant bubbles, tool calls and reasoning shown as collapsible `<details>`; oversized text/tool payloads are capped at 8 KiB per field with a "(truncated)" marker.
- **opencode.log**: tail of the opencode client log (default 200 lines, selectable 100/200/1000), rendered in the same page.

Requirements: the `sqlite3` CLI must be on PATH (read-only mode is used, WAL safe). Without it the tab shows an error instead of failing the server.

### Timer

Fleet scheduling with three timer types:

| Type | Purpose |
|---|---|
| **ship** | Send a directive to a specific agent or fleet |
| **command** | Send a structured command (e.g. `model`) to an agent |
| **system** | Execute workspace-level commands directly in the worker |

System commands (executed directly in the timer worker, no agent needed):

| Command | Description |
|---|---|
| `reindex` | Refresh agent instructions index + dashboard index |
| `web` | Start web server (idempotent — skips if already running) |
| `web restart` | Force web server restart |

The Timer tab also provides quick access to:
- **Timer Worker** status (start/stop)
- **Web Server** restart button

### Berichte (Reports)

Fleet report system. Each report is a structured document with title, subtitle, Markdown body, tags, optional task reference, and file attachments.

- **List view**: shows title, subtitle, ship, relative time, tags — click any card to open the detail modal
- **Detail modal**: full body rendered as Markdown, clickable task reference link, clickable attachment links (served via filestore)
- **Submit form**: title (required), subtitle, Markdown body, tags, task slug, file upload via filestore
- **Filter**: by ship name or tag

See [Reports](#7-reports) for full CLI and API reference.

### Files

Workspace file browser for viewing and downloading files.

- Navigate directories with breadcrumb trail
- View text files inline (up to 2 MB)
- Download any file
- Path traversal protection (stays within workspace root)

### API Endpoints

All endpoints return JSON. The web UI consumes these, but they're also usable from scripts/CI.

| Endpoint | Method | Description |
|---|---|---|
| `/api/reports` | GET | List all reports (JSON). Optional `?ship=` and `?tag=` filters. |
| `/api/reports` | POST | Create a report (JSON body) |
| `/api/reports/<id>` | GET | Get a single report (JSON) |
| `/api/reports/<id>` | DELETE | Delete a report |
| `/api/board` | GET | Fleet status board (all ships with status, progress, etc.) |
| `/api/msgs` | GET | All bus messages (newest first). Optional `?ship=<name>` for per-ship conversation |
| `/api/inbox` | GET | Messages addressed to the viewing ship |
| `/api/asks` | GET | Unanswered `[ask]` questions for the viewing ship |
| `/api/events?n=50` | GET | Last N audit log entries |
| `/api/tasks` | GET | All dashboard topics (project tasks) |
| `/api/task` | POST | Create or update a task (JSON body: `{title, desc, assign}` or `{slug, status}`) |
| `/api/tell` | POST | Send a message (JSON body: `{target, text}` or form: `target` + `text`) |
| `/api/cmd` | POST | Post a command verb to a ship (JSON body: `{target, verb, args}`). Used for `model`, etc. |
| `/api/identity` | GET | Viewing ship's identity (`{ship_id, handle, project}`) |
| `/api/models` | GET | Available models for ship launch (from `models.yaml`) |
| `/api/ship` | POST | Launch a new ship (JSON body: `{name, model, provider, parent}`) |
| `/api/timers` | GET | List all timers. Optional `?all=1` for all ships |
| `/api/timer` | POST | Create a timer (JSON body: `{schedule_type, target_type, text/cmd, ...}`) |
| `/api/timer/{id}` | DELETE | Delete a timer |
| `/api/timer/{id}/pause` | POST | Pause a timer |
| `/api/timer/{id}/resume` | POST | Resume a timer |
| `/api/timer/worker` | GET/POST | Timer worker status / start/stop/restart |
| `/api/files?path=<path>` | GET | List directory contents (JSON: `{path, entries}`) |
| `/api/files/raw?path=<path>` | GET | Serve raw file content. Optional `?download=1` for attachment |
| `/api/web/restart` | POST | Restart the web server daemon |
| `/api/sessions` | GET | List opencode sessions, newest first. Optional `?title=`, `?agent=` (mode), `?limit=` (max 500). Each entry carries `running` (title matches a live board ship) |
| `/api/sessions/<id>` | GET | Session meta + transcript window. Optional `?limit=` (max 500), `?offset=` |
| `/api/oclog?n=200` | GET | Tail of the opencode client log (max 5000 lines) |

### Auto-Refresh

The frontend polls every 15 seconds for the Flotte, Tasks, Log, Bus, Funk, Timer, Berichte, Files, and Sitzungen views. The ship detail panel also refreshes the conversation history (and the Verlauf list, when active) on the same interval.

### Model Registry

The ship launch dropdown is populated from `.starfleet-ai/conf/models.yaml`, which is auto-generated from `opencode models --verbose`:

```sh
# Regenerate the model list (filters for text models with tool-call support)
.starfleet-ai/bin/gen-models-yaml
```

The script outputs YAML with entries like:

```yaml
models:
  - id: "opencode/big-pickle"
    provider: "opencode"
    label: "Big Pickle"
    context: 200000
```

Only models with `toolcall: true` and `context > 0` are included (required for agent use). The web UI fetches this list via `GET /api/models` and groups models by provider in the dropdown.

### Web Server Management

```sh
starfleetctl web                    # show help
starfleetctl web start              # start in foreground
starfleetctl web start --addr :9090 # custom listen address
starfleetctl web autostart          # start as daemon (if not running)
starfleetctl web stop               # stop daemon
starfleetctl web restart            # stop + autostart (background)
```

---

## 7. Reports

Reports are structured documents submitted by ships (agents) to share status updates, test results, build summaries, or any other information with the rest of the fleet.

Each report has:

| Field | Required | Description |
|---|---|---|
| **Title** | ✅ | Short summary |
| **Subtitle** | — | Optional one-line subtitle (shown in list view) |
| **Body** | — | Markdown-formatted body text |
| **Ship** | auto | Name of the submitting ship |
| **Tags** | — | Comma-separated tags for filtering |
| **TaskRef** | — | Dashboard task slug (clickable link in web UI) |
| **Attachments** | — | Uploaded file references (via filestore) |
| **Created** | auto | Unix timestamp |

### CLI Usage

#### Submit a report

```sh
# Minimal
starfleetctl reports submit "Title here"

# With subtitle, body, and tags
starfleetctl reports submit "Build #42" \
  --subtitle "CI Status" \
  --body "all tests passed" \
  --tags "ci,build"

# Long body from a file
starfleetctl reports submit "Test Results" \
  --body-file test-output.log

# With task reference and file attachments
starfleetctl reports submit "Release Ready" \
  --subtitle "v25.2-rc1" \
  --body-file CHANGELOG.md \
  --task-ref xlibre/release-25-2 \
  --attachment build.log \
  --attachment test-report.xml
```

Attachments are uploaded to the filestore (`.starfleet-ai/var/files/`) with a default TTL of 60 minutes. They can be viewed/downloaded via the web UI at `/api/store/<name>`.

#### List reports

```sh
starfleetctl reports list              # text table (newest first)
starfleetctl reports list --json       # full JSON
starfleetctl reports list --ship Nebula
starfleetctl reports list --tag ci
```

#### Show a report

```sh
starfleetctl reports show r-1700000000123456789
```

#### Delete a report

```sh
starfleetctl reports delete r-1700000000123456789
```

### Web UI

Reports appear in the **Berichte** tab of the fleet web console.

#### List view

Each card shows:
- **Title** with badge for task ref (`📋`) and attachments (`📎N`)
- **Ship** pill (filterable)
- **Time** (relative, e.g. "5m ago")
- **Subtitle** (if set)
- **ID** and **tags**

Click any card to open the **detail modal**.

#### Detail modal

The modal shows the full report with:
- Title and subtitle
- Ship, relative time, and tags (meta bar)
- **Body** rendered as **Markdown** (headings, bold, lists, code blocks, etc.)
- **Task reference** as a clickable link that switches to the Tasks tab
- **Attachments** as clickable links (open in new tab, served via filestore)

#### Submitting from the browser

1. Fill in title (required), subtitle, body (Markdown), tags, and task slug
2. Use the **Attach** button to upload files via the filestore
3. Click **Submit Report**

#### Filtering

Use the **Filter** fields to narrow the list by ship name or tag.

### Storage

Reports are stored as individual JSON files on disk (persisted to git like dashboard topics):

```
.starfleet-ai/reports/<id>.json
```

Attachments are stored separately in the filestore (ephemeral, not git-tracked):

```
.starfleet-ai/var/files/<name>
.starfleet-ai/var/files/<name>.meta   (TTL expiry)
```

### API

| Endpoint | Method | Description |
|---|---|---|
| `/api/reports` | GET | List all reports (JSON). Optional `?ship=` and `?tag=` filters. |
| `/api/reports` | POST | Create a report (JSON body: `{title, subtitle, body, tags, task_ref, attachments}`) |
| `/api/reports/<id>` | GET | Get a single report (JSON) |
| `/api/reports/<id>` | DELETE | Delete a report |
| `/api/store/<name>` | GET | Serve an attachment file |
| `/api/store/<name>` | POST | Upload an attachment file |

---

## 8. Troubleshooting

### 8.1 "No workspace found" / "Cannot find .starfleet-ai"

```sh
# Ensure you're in a bootstrapped workspace
ls -la .starfleet-ai/     # should exist

# Or run bootstrap to fix
./starfleet-bootstrap
```

### 8.2 Ships Not Appearing on Board

```sh
# Check heartbeat directory
ls -la .starfleet-ai/var/comms/status/

# Verify STARFLEET_BUS_DIR is consistent across sessions
echo $STARFLEET_BUS_DIR

# Prune stale entries
starfleetctl comms prune
```

### 8.3 "Ship name already in use"

```sh
starfleetctl ship-names list        # see assigned names
starfleetctl ship-names release <name>  # force-release
starfleetctl ship-names gc          # auto-clean stale (> STARFLEET_STARFLEET_BUS_TTL)
```

### 8.4 PR Claim Conflicts

```sh
starfleetctl pr-claim --list        # see all claims
starfleetctl pr-claim --steal 3162  # take over (with reason)
```

### 8.5 Web UI Not Loading / Port in Use

```sh
starfleetctl web start --addr :8081   # different port
starfleetctl web stop                  # kill daemon
```

### 8.6 opencode Plugin Not Delivering Messages

- Ensure `.opencode/plugin/starfleet-dispatch.ts` exists (installed by bootstrap)
- Check opencode plugin logs in opencode UI
- Verify `STARFLEET_BUS_DIR` matches between CLI and plugin

### 8.7 Go vs Bash Interoperability Issues

- Both **must** use same `STARFLEET_BUS_DIR` (default `.starfleet-ai/var/comms`)
- Both use same `flock` on `.lock` — don't mix custom lock paths
- Run `starfleetctl comms prune` periodically

### 8.8 Comms Monitor Loop Not Seeing New Messages

**Known limitation:** The Go `monitor-loop`/`fleet-watch` commands don't detect messages arriving while running under certain terminal monitoring tools.

**Workarounds:**
- Use Go commands: `starfleetctl comms monitor-loop`, `starfleetctl comms fleet-watch`
- Use opencode's plugin-based polling (auto-installed by bootstrap)

---

## Appendix: Environment Variables

| Variable | Default | Description |
|----------|---------|-------------|
| `STARFLEET_SHIP_ID` | `user@hostname` | Unique ship identifier |
| `STARFLEET_ROLE` | — | `flagship` or `ship` |
| `STARFLEET_TARGET` | — | Flagship ship ID (for ships) |
| `STARFLEET_BUS_DIR` | `.starfleet-ai/var/comms` | Comms state directory |
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
│   │   ├── comms/
│   │   │   ├── .lock            # flock domain
│   │   │   ├── .seq             # message counter
│   │   │   ├── status/          # heartbeats (Enterprise.tsv, Voyager.tsv)
│   │   │   ├── msgs/            # messages
│   │   │   │   ├── m0001.tsv
│   │   │   │   └── m0002.tsv
│   │   │   ├── acks/            # acknowledgment markers
│   │   │   ├── attachments/     # large payloads
│   │   │   └── events.log       # audit trail
│   │   ├── agent-claims/        # PR claims (pr-3162.tsv, ...)
│   │   ├── sop.d/               # fleet coordination fragments (under var/)
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
| [doc/comms.md](doc/comms.md) | Comms command reference |
| [doc/session.md](doc/session.md) | Session & worktree management |
| [doc/pr-claim.md](doc/pr-claim.md) | PR locking details |
| [doc/sop.md](doc/sop.md) | SOP fragment system (creating, editing, reindex) |
| [doc/web-ui.md](doc/web-ui.md) | Web UI deep dive (architecture, API) |
| [doc/known-limitations.md](doc/known-limitations.md) | Current caveats and workarounds |
| [doc/config.md](doc/config.md) | Configuration reference |
| [doc/github.md](doc/github.md) | GitHub integration |

---

**License:** AGPL-3.0-or-later — see [LICENSE](LICENSE)