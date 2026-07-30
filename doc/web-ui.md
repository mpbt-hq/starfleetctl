# Web UI — Fleet Console

A browser-based dashboard for monitoring and controlling your agent fleet.

## Starting the Web UI

```sh
starfleetctl web start [--addr :8080]
```

Opens a single-page app at `http://localhost:8080` (default). The server reads the comms state from the workspace and serves both the API and the embedded frontend.

## Screenshots

| View | Screenshot |
|---|---|
| Flotte (Status Board) | ![Flotte](web-ui/Screenshot_20260716_122248_Fennec.jpg) |
| Tasks | ![Tasks](web-ui/Screenshot_20260716_122255_Fennec.jpg) |
| Bus | ![Bus](web-ui/Screenshot_20260716_122302_Fennec.jpg) |
| Funk | ![Funk](web-ui/Screenshot_20260716_122306_Fennec.jpg) |
| Log | ![Log](web-ui/Screenshot_20260716_122309_Fennec.jpg) |

## Tabs

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
- Compose and send a message directly to the ship

#### Launching a new ship

The "Neues Schiff" form at the top of the Flotte tab lets you launch a
background ship directly from the browser:

- **Name** (optional): ship name; auto-assigned if left blank
- **Modell** (dropdown): select from available models, grouped by provider.
  The dropdown is populated from `/api/models` (backed by `models.yaml`).
  The last-used model is remembered via `localStorage`.
- **Provider** (dropdown): auto-set when a model is selected; can be overridden
  manually. Options: openai, anthropic, google, nvidia, mistral, meta.
- **Übergeordnet** (optional): parent ship for hierarchical ordering

Model registry is generated from `opencode models --verbose` via the
`gen-models-yaml` script (see [Model Registry](#model-registry) below).

### Tasks

Project task tracking (backed by `dashboard/topics/*.md`).

- **Create** a new task with title, description, and optional assignment
- **Change status** (open → assigned → in-progress → done → parked)
- **Unassign** a task to return it to the pool

### Bus

Cross-agent messaging. Three sub-tabs:

| Sub-tab | Description |
|---|---|
| **Direktiven** | All messages on the bus (newest first) |
| **Inbox** | Messages addressed to your ship (or broadcasts) |
| **Fragen** | Unanswered `[ask]` questions addressed to your ship |

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

### Timer

Fleet scheduling with three timer types:

| Type | Purpose |
|---|---|
| **ship** | Send a directive to a specific agent or fleet |
| **command** | Send a structured command (e.g. `setModel`) to an agent |
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

### Berichte

Fleet report system. Each report is a structured document with title,
subtitle, Markdown body, tags, optional task reference, and file
attachments.

- **List view**: shows title, subtitle, ship, relative time, tags —
  click any card to open the detail modal
- **Detail modal**: full body rendered as Markdown, clickable task
  reference link, clickable attachment links (served via filestore)
- **Submit form**: title (required), subtitle, Markdown body, tags,
  task slug, file upload via filestore
- **Filter**: by ship name or tag

See [Reports](reports.md) for full CLI and API reference.

### Files

Workspace file browser for viewing and downloading files.

- Navigate directories with breadcrumb trail
- View text files inline (up to 2 MB)
- Download any file
- Path traversal protection (stays within workspace root)

## API Endpoints

All endpoints return JSON. The web UI consumes these, but they're also usable from scripts/CI.

| Endpoint | Method | Description |
|---|---|---|
| `/api/reports` | GET | List all reports (JSON). Optional `?ship=` and `?tag=` filters. |
| `/api/reports` | POST | Create a report (JSON body) |
| `/api/reports/<id>` | GET | Get a single report (JSON) |
| `/api/reports/<id>` | DELETE | Delete a report |
| `/api/board` | GET | Fleet status board (all agents with status, progress, etc.) |
| `/api/msgs` | GET | All bus messages (newest first). Optional `?ship=<name>` for per-ship conversation |
| `/api/inbox` | GET | Messages addressed to the viewing ship |
| `/api/asks` | GET | Unanswered `[ask]` questions for the viewing ship |
| `/api/events?n=50` | GET | Last N audit log entries |
| `/api/tasks` | GET | All dashboard topics (project tasks) |
| `/api/task` | POST | Create or update a task (JSON body: `{title, desc, assign}` or `{slug, status}`) |
| `/api/tell` | POST | Send a message (JSON body: `{target, text}` or form: `target` + `text`) |
| `/api/cmd` | POST | Post a command verb to a ship (JSON body: `{target, verb, args}`). Used for `setModel`, etc. |
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

## Auto-Refresh

The frontend polls every 15 seconds for the Flotte, Log, and Bus views. The ship detail panel also refreshes the conversation history on the same interval.

## Architecture

The web UI is a single embedded HTML file (`internal/web/index.html`) with vanilla JavaScript — no build step, no framework, no npm. The Go server serves it via `go:embed`.

```
Browser  ──HTTP──▶  starfleetctl web start
                       │
                       ├── /api/board     ──▶ comms status/
                       ├── /api/msgs      ──▶ comms msgs/
                       ├── /api/inbox     ──▶ (filtered msgs)
                       ├── /api/asks      ──▶ (filtered msgs)
                       ├── /api/events    ──▶ comms events.log
                       ├── /api/tasks     ──▶ dashboard/topics/*.md
                        ├── /api/tell      ──▶ comms tell/broadcast
                        ├── /api/cmd       ──▶ comms command
                       ├── /api/models    ──▶ models.yaml
                       ├── /api/ship      ──▶ session.LaunchShip()
                       ├── /api/timers    ──▶ timer.Store.List()
                       ├── /api/files     ──▶ os.ReadDir()
                       ├── /api/files/raw ──▶ http.ServeFile()
                       └── /              ──▶ index.html (embedded)
```

## Model Registry

The ship launch dropdown is populated from `.starfleet-ai/conf/models.yaml`,
which is auto-generated from `opencode models --verbose`:

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

Only models with `toolcall: true` and `context > 0` are included (required
for agent use). The web UI fetches this list via `GET /api/models` and groups
models by provider in the dropdown.

## Web Server Management

```sh
starfleetctl web                    # show help
starfleetctl web start              # start in foreground
starfleetctl web start --addr :9090 # custom listen address
starfleetctl web autostart          # start as daemon (if not running)
starfleetctl web stop               # stop daemon
starfleetctl web restart            # stop + autostart (background)
```

## Design Principles

- **Zero dependencies**: no npm, no webpack, no React — plain HTML/CSS/JS
- **Dark theme**: consistent with terminal aesthetics
- **Mobile-friendly**: responsive layout, works in mobile browsers
- **Real-time**: auto-refresh keeps the view current without manual reload
- **Secure by default**: the web server only binds to localhost by default
