# starfleetctl

**One binary to coordinate AI-agent sessions working on the same codebase.**

starfleetctl manages concurrent agent sessions ("ships") — status heartbeats, cross-session directives, PR-branch locking, and GitHub PR access — so multiple agents can work in parallel without stepping on each other.

## Why?

When several AI agents share one repository, you need:

- **A status board** — who's working on what, who's idle, who's blocked
- **A message bus** — agents can send tasks and questions to each other
- **PR locking** — prevent two agents from editing the same branch
- **Safe commits** — concurrent sessions don't race the same working tree
- **GitHub integration** — view, comment, and label PRs without `jq`/`sed`

starfleetctl provides all of this as a single Go binary with no third-party dependencies.

## Quick Start (5 minutes)

### 1. Build

```sh
git clone https://github.com/metux/starfleetctl
cd starfleetctl
make build    # -> ./starfleetctl
```

### 2. Bootstrap a workspace

**One-time setup.** `genesis-init` writes the `starfleet-bootstrap` script
into your workspace and runs `bootstrap --fix` to set up everything:

```sh
cd /path/to/your/workspace
starfleetctl genesis-init .
```

After this, `starfleet-bootstrap` is the only file you need to **commit** to
your repo — it's the self-contained entry point that re-installs starfleetctl
(builds from source, symlinks the binary, sets up configs) on any fresh clone.

### 3. Re-bootstrap (updates)

Once `starfleet-bootstrap` exists in your repo, anyone can re-run it to
update starfleetctl and re-apply all workspace configuration:

```sh
./starfleet-bootstrap
```

This is idempotent — safe to run multiple times.

### 4. What bootstrap installs

`bootstrap --fix` (called by both `genesis-init` and `starfleet-bootstrap`)
sets up:

- **`.claude/settings.json`** — allowlist entries for `starfleetctl` commands
- **Agent fragments** — fleet coordination instructions (`.starfleet-ai/agents.d/`)
- **Skills** — on-demand reference docs (`.claude/skills/`)
- **opencode plugins** — agent-bus polling, inbox injection
- **Launcher scripts** — `run-opencode.ship`, `run-opencode.flagship`, `run-claude.*`
- **Claude hooks** — agent-permission-hook for tool gating
- **DASHBOARD.md** — project status tracking

The launcher scripts (`run-opencode.ship`, `run-opencode.flagship`) set up
`STARFLEET_SHIP_ID`, `STARFLEET_ROLE`, and `OPENCODE_CONFIG_CONTENT` so
ships automatically participate in the fleet via starfleetctl.

### 5. Use it

```sh
# Post your status
starfleetctl agent-bus status working "building feature X"

# See who's online
starfleetctl agent-bus board

# Send a task to another agent
starfleetctl agent-bus tell Voyager "run tests on branch feature-x"

# Check your inbox
starfleetctl agent-bus inbox
```

## Roles: Flagship vs Ship

A fleet has one **flagship** (the control agent) and zero or more **ships**
(worker agents).

| Role | Script | Identity | Purpose |
|---|---|---|---|
| Flagship | `run-opencode.flagship` | Fixed name (e.g. `Enterprise`) | Top of hierarchy — receives questions, approves tool calls, steers the fleet |
| Ship | `run-opencode.ship` | Auto-assigned from pool (e.g. `Voyager`) | Executes tasks, reports back to flagship |

**Flagship:**
- Runs the "1st officer" control loop — watches the agent-bus board, answers `ask` directives
- Has a fixed identity (`STARFLEET_SHIP_ID=Enterprise`)
- Does not report to another agent (`STARFLEET_TARGET` unset)

**Ship:**
- Auto-assigned a unique name from the ship-name pool
- Reports to the flagship (`STARFLEET_TARGET=Enterprise`)
- Can delegate tasks to other ships or ask the flagship for decisions

**Background Ships:**
- Launched via the Web UI ("Neues Schiff" form) or `session ship-run`
- Run as detached termctl terminals in the background
- Visible on the fleet board with model/provider info
- Start/stop/attach via `session ship-run`/`session stop`/`session attach`
- Survive the launching session — managed independently

**Automatic communication:** Once started via the launcher scripts, all
ships share the same agent-bus and can communicate autonomously — sending
directives (`tell`), broadcasting to all (`broadcast`), asking questions
(`ask`), and responding (`reply`) — without manual coordination. The
opencode plugin polls the bus and injects incoming messages into each
ship's context automatically.

```sh
# Start the flagship
./run-opencode.flagship

# Start a worker ship (auto-assigns a name)
./run-opencode.ship

# Start a worker ship with a specific name
./run-opencode.ship --name Voyager
```

## Documentation

| Document | Description |
|---|---|
| [Architecture](doc/architecture.md) | How it works — data flow, file formats, locking |
| [Agent Bus](doc/agent-bus.md) | Cross-session messaging reference |
| [Dashboard](doc/dashboard.md) | Project status tracking |
| [PR Claim](doc/pr-claim.md) | PR-branch locking |
| [Session](doc/session.md) | Agent session management (termctl) |
| [GitHub](doc/github.md) | PR viewing, commenting, labeling |
| [Hooks](doc/hooks.md) | Claude Code / opencode integration |
| [Web UI](doc/web-ui.md) | Browser-based fleet dashboard |
| [Known Limitations](doc/known-limitations.md) | Current caveats and workarounds |

## Web UI

A built-in browser dashboard for monitoring and controlling your fleet — no npm, no framework, just open `http://localhost:8080`.

```sh
starfleetctl web [--addr :8080]
```

**Features:**
- **Flotte** — live status board with progress bars, blockers, stale detection, model/provider pills
- **Neues Schiff** — launch background ships with model selection dropdown (grouped by provider)
- **Bus** — cross-agent messages with thread view, inbox, questions
- **Tasks** — create, assign, and track project tasks
- **Funk** — send messages to any agent or broadcast to the fleet
- **Log** — real-time event feed

Click any ship card to see its details and conversation history. See [doc/web-ui.md](doc/web-ui.md) for the full reference.

## Subcommand Overview

### Fleet Coordination

| Command | Purpose |
|---|---|
| `agent-bus` | Status board + cross-session directives |
| `dashboard` | Project topic tracking |
| `pr-claim` | Advisory PR-branch locks |
| `ws-commit` | Atomic commit+push under lock |
| `ship-names` | Session identity registry |
| `session` | Agent session lifecycle |
| `with-clone-lock` | Serialize git mutations |

### GitHub (read-only)

| Command | Purpose |
|---|---|
| `pr-view` | PR metadata |
| `pr-ci` | CI status (failure-classified) |
| `show-branch-file` | File at any branch ref |
| `backport-applies` | Cross-branch applicability check |

### GitHub (mutating)

| Command | Purpose |
|---|---|
| `pr-comment` | Post PR comment |
| `pr-label` | Add/remove labels |
| `pr-checkout` | Isolated PR clone |
| `xx-make-pr` | Create PR with conventions |

### Utilities

| Command | Purpose |
|---|---|
| `bootstrap` | Verify/fix workspace structure |
| `genesis-init` | Bootstrap from nothing |
| `self-install` | Clone/build/install updates |
| `json` | JSON validate/pretty/get |
| `with-clone-lock` | Serialize git operations |

## Build

Plain Go, stdlib only:

```sh
make build      # -> ./starfleetctl
make test       # go test ./...
make fmt vet    # go fmt / go vet
```

## License

AGPL-3.0-or-later. See [`LICENSE`](LICENSE).
