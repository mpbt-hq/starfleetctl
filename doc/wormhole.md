# Wormhole Manager Documentation

## Overview

The wormhole manager handles incoming/outgoing messages between fleet ships via
the comms bus. It provides a daemon mode with PID/State files, configuration via
`.starfleet-ai/conf/wormholes.yaml`, and CLI commands for status monitoring,
synchronization, and daemon lifecycle management.

The wormhole is **NOT a parallel messaging system**: received messages are imported
only through Comms Core's external-ingestion API (`Bus.PostExternal`), and the
agent/session plugin only ever reads the local Comms inbox.

## Architecture

The wormhole manager consists of:

- **Config**: `.starfleet-ai/conf/wormholes.yaml` — daemon configuration
- **Package**: `internal/wormhole` — Go package for message handling and daemon
- **Worker Daemon**: Background process with PID and State files (`wormhole run-internal`)
- **Git Transport**: Messages transported via Git Maildir spool (not network sockets)
- **CLI Commands**: `wormhole status/sync/send/start/stop/restart`

### Directory Layout

```
.starfleet-ai/
├── conf/
│   ├── wormholes.yaml           # wormhole configuration
│   └── comms.yaml               # comms routing config
└── var/
    └── wormholes/
        ├── pid                  # daemon PID file
        ├── state                # daemon state file
        └── relay/               # example repository checkout
            └── mail/
                ├── alpha/
                │   ├── new/         # incoming messages (new)
                │   ├── cur/         # claimed messages (cur)
                │   ├── processed/   # successfully imported (processed)
                │   └── failed/      # failed/permanent rejections
                └── beta/
                    ├── new/
                    ├── cur/
                    ├── processed/
                    └── failed/
```

## Configuration

See `.starfleet-ai/conf/wormholes.yaml` for the full configuration reference.

### Key Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `enabled` | bool | `false` | Enable wormhole sync |
| `sync.interval` | duration | `5s` | Sync interval |
| `sync.startup_sync` | bool | `true` | Sync on startup |
| `sync.clone_timeout` | duration | `2m` | Git clone timeout |
| `sync.command_timeout` | duration | `30s` | Git command timeout |
| `sync.push_retries` | int | `3` | Push retries on non-fast-forward |

### Repository Configuration

Each repository entry:

| Field | Type | Required | Description |
|-------|------|----------|-------------|
| `id` | string | yes | Unique repository identifier (e.g., `relay`) |
| `remote` | string | yes | Git remote URL (SSH/HTTPS) |
| `branch` | string | yes | Git branch to track (e.g., `main`) |
| `enabled` | bool | no | Enable/disable repo (default: true) |
| `access` | string | no | `read-only` or `read-write` (default: read-write) |
| `incoming.enabled` | bool | no | Enable incoming message processing (default: true) |
| `incoming.initial_scan` | string | no | `baseline` or `replay` (default: `baseline`) |

### Example `wormholes.yaml`

```yaml
wormholes:
  enabled: true
  sync:
    interval: "5s"
    startup_sync: true
    clone_timeout: "2m"
    command_timeout: "30s"
    push_retries: 3
  repositories:
    - id: relay
      remote: "ssh://git@example.com/relay.git"
      branch: "main"
      access: read-write
      incoming:
        enabled: true
        initial_scan: baseline
```

## Message Transport

The wormhole uses **Git Maildir spool** for message transport:

1. **Sender** publishes message to `mail/<target>/new/<id>.md` via `spool.Publish()`
2. **Transport** commits+pushes to Git remote (Maildir spool in Git repo)
3. **Receiver** runs `processPendingMessages()` to:
   - Resolve the central set of locally responsible ships
   - Fetch + rebase remote changes
   - For every local ship: claim messages in `new/` → move to `cur/`
   - Import via `Bus.PostExternal()` → Comms inbox
   - Move `cur/` → `processed/` on success, or `failed/` on permanent failure
4. **At-least-once delivery**: Claims prevent duplicate processing; crashes between claim and import leave message in `cur/` for retry

### Central multi-ship cycle

`processPendingMessages()` runs the cycle above for the whole workspace. The
local ship set is resolved centrally (fleet configuration: flagship +
declarative standing ships; ship-name registry: reservations; comms board:
status records; the daemon's own valid `STARFLEET_SHIP_ID`) and never from the
daemon's user/host fallback identity. Every local ship gets its own mailbox
(`mail/<ship>/new/`) processed inside the single repository-scoped writer lock;
mailbox failures are isolated and aggregated, so one failing mailbox does not
block the others.

### Message Format

Messages are immutable YAML-frontmatter + body files:

```yaml
---
id: w0-20261110T095923Z-abc123
from: alpha
to: beta
subject: greeting
type: ship
reply_to: ""
created: "2026-11-10T09:59:23Z"
---
Message body content here.
```

Comms metadata (`type`, `reply_to`) is preserved through the transport.

### Initial Scan Modes

On first run for a given mailbox, `incoming.initial_scan` controls how existing messages are handled:

- **`baseline`** (default): Move all existing messages from `new/` to `processed/` without importing. Establishes a baseline so only future messages are imported.
- **`replay`**: Import all existing messages normally through the standard pipeline.

The scan state is persisted in `.starfleet-state/initial_scan_done` per repository/ship.

## CLI Commands

```
starfleetctl wormhole status    # Show daemon status (PID, uptime, process liveness)
starfleetctl wormhole sync      # One-shot sync + import (like one daemon tick)
starfleetctl wormhole send --to ShipB --text "message"
starfleetctl wormhole start     # Start daemon (forks background worker)
starfleetctl wormhole stop      # Stop daemon (SIGTERM, cleanup)
starfleetctl wormhole restart   # Stop + start
```

### `send` Options

```
--to       Recipient ship (mailbox owner) [required]
--repo     Repository id (default: first enabled)
--from     Sender ship (default: local ship identity)
--subject  Optional subject
--type     Message type (default: message)
--text     Message text
--stdin    Read message text from stdin
```

## Daemon Lifecycle

### Start
1. Acquire start lock (serializes concurrent start/autostart)
2. Resolve recorded instance from PID + state files:
   - PID file present but state missing/corrupt → hard error (refusing to act on an unverified PID)
   - PID file present but unreadable/unparseable/empty/non-positive → hard error (refusing to act on an unverified PID; the file is preserved for manual inspection)
   - PID file and state file disagree on PID → hard error (possible PID reuse)
   - State records a different workspace root → hard error
3. If a recorded instance exists and is alive → error (already running)
4. If a recorded instance exists but is dead → clean up stale PID + state files
5. Fork child process running `starfleetctl wormhole run-internal` (detached, new session, output → `daemon.log`)
6. Child removes a stale `ready` file, writes its own PID, marks state `starting` → `running`, writes `ready` after startup sync
7. Parent polls (30s deadline) for PID file match + `ready` file; an early child exit is detected immediately (reaped child) and reported as `exited during startup` — the state file is kept so `wormhole status` shows the failure reason

### Stop
1. Read PID from PID file (a present-but-unusable PID file is a hard refusal — it is never silently treated as "no daemon")
2. Cross-check with state file PID to detect PID reuse
3. Verify the PID is this workspace's wormhole worker (`/proc/<pid>/cmdline` + `MPBT_WORKSPACE_ROOT` environ) — live but unverifiable PIDs are never signaled
4. Send SIGTERM to process
5. Wait for graceful exit, force kill (SIGKILL, re-verified identity) if still alive
6. Clean up PID + state files; a dead recorded PID is reported as stale and cleaned up

### Status
Resolves the shared identity (PID file + state file cross-check, worker identity verification) and checks liveness. A state file left from a failed startup surfaces its recorded error. Cross-checks exist to detect PID reuse.

### Autostart
Called from flagship's `services.AutostartConfigured()` if `wormholes.enabled: true`. Idempotent: returns immediately if daemon already running. Concurrent autostarts converge — the loser of a race re-checks and reports `started=false` without error.

## Crash Recovery

- **PID file cleanup**: `StopDaemon` removes PID/state files; a failed start keeps the state file (with error reason) for `status`, removes PID + `ready`
- **Early child exit**: the parent reaps the child and detects a worker that dies before becoming ready (no 30s hang)
- **Startup sync failure**: fatal — RunLoop records `status: error` + reason in the state file and exits non-zero
- **Process crash**: dead worker detected via recorded instance liveness check
- **Maildir crash recovery**: `commitDirtyMailLocked()` recovers uncommitted lifecycle moves (new→cur, cur→processed, baseline moves)
- **Import crash**: Messages stay in `cur/` (re-claimed on restart via `processCurLocked`)
- **Initial scan crash**: a crash mid baseline move (partial rename, nothing committed, no marker) is recovered on the next cycle — the dirty move is committed, the scan finishes, nothing pre-existing is imported
- **Duplicate prevention**: `PostExternal` deduplicates via `source+externalID+payloadSHA`
- **ID reservation**: CommsID is reserved via `nextID()` BEFORE external record creation, eliminating crash window where a record could exist without an ID
- **Initial scan state**: Persisted in the clone-local state dir per repo/ship

## Read-Only Repositories

Repositories with `access: read-only`:
- Fetch remote changes normally
- Process mailbox locally (claim, import, move to processed/failed)
- **Skip all pushes** — the remote is never advanced by the read-only client
- Local Maildir transitions are committed locally on every cycle (working tree stays clean) but never pushed
- Local-only lifecycle commits are rebased onto new remote history with rename detection disabled (`-X no-renames` + `merge.directoryRenames=false`), so a remote-side publish into `new/` cannot be misread as a directory rename

## Comms Metadata Preservation

The transport preserves `Type` and `ReplyTo` fields through the entire pipeline:
1. `PublishRemoteMessage` receives `OutboundMessage` with `Type` and `ReplyTo`
2. `NewMessage` stores them in the wormhole envelope
3. Envelope serializes `type:` and `reply_to:` front-matter fields
4. On import, `importLocked` passes them to `PostExternal`
5. `externalRecord` stores `Type` and `ReplyTo`
6. `publishExternalRecipientLocked` writes them to the comms message record
7. Plugin receives full message object with metadata

**Attachments are not supported** — messages with inline attachment pointers (`[[attach:...]]`) are rejected with a clear error.

## Comms Routing (for remote delivery)

In `.starfleet-ai/conf/comms.yaml`:

```yaml
comms:
  routing:
    local_domain: local
    domains:
      - domain: peer.example
        transport: wormhole
        repository: relay
```

## Limitations / Known Constraints

- **Not a network daemon**: No listening socket; transport is Git-based
- **Single-writer per repo**: Repository lock serializes all Git operations
- **At-least-once delivery**: Crash between claim and seen_mark may cause re-delivery
- **No consumer groups**: Each ship has its own mailbox; no competing consumers
- **Read-only repos**: Can import but not publish messages
- **No attachments**: Messages with attachments are rejected with explicit error
- **Daemon tests require git remote**: Full lifecycle tests need a proper git remote

## Troubleshooting

| Symptom | Cause | Fix |
|---------|-------|-----|
| `wormhole daemon already running` | Daemon running | `starfleetctl wormhole stop` |
| `pid file and state file disagree` | Possible PID reuse | Inspect both files, then remove `.starfleet-ai/var/wormholes/{pid,state}` manually |
| `pid ... is alive but not verifiable as this workspace's wormhole worker` | PID reused by a foreign process | Verify the PID manually, then remove the pid/state files |
| `pid file ... is unreadable/unparseable/empty/non-positive` (refusing to act) | Present but unusable PID file | Inspect `pid` + `state` manually; if you are sure no wormhole daemon runs, remove `.starfleet-ai/var/wormholes/pid` (and `state`) |
| `wormhole daemon exited during startup` | Worker failed before ready (e.g. startup sync failed) | `starfleetctl wormhole status` shows the recorded error; check `daemon.log` in the wormholes dir |
| `clone failed` | Invalid remote/auth | Check SSH keys, remote URL, branch name |
| `push failed` | Non-fast-forward | Auto-retry via fetch+rebase (up to `push_retries`, for daemon cycles and `wormhole sync` alike) |
| Messages stuck in `cur/` | Import failed | Check comms bus, `processPendingMessages` logs |
| Messages in `failed/` | Permanent rejection | Check `*.md.reason` for details |

## Testing

```bash
# Run all wormhole tests
go test ./internal/wormhole/... -count=1 -timeout 120s

# Run specific test
go test ./internal/wormhole/... -run TestPublishProcessLifecycle -v

# Run comms tests
go test ./internal/comms/... -count=1

# Full test suite
go test ./... -count=1 -timeout 180s
```

---

*See `internal/wormhole/wormhole.go`, `spool.go`, `git_sync.go`, `messages.go`, `cli.go` for implementation details.*