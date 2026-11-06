# Wormhole Manager Requirement Matrix (W0-017/W0-018/W0-024/W0-026)

## Requirement ID: W0-017 — Wormhole Manager Core

| # | Requirement | Implementation | File | Test |
|---|-------------|---------------|------|------|
| W0-017.1 | Config struct with all fields | `Config` struct with `ListenAddr`, `PIDFile`, `StateFile`, `MaxMessageAge`, `AutoStart`, `ForwardMode`, `WorkerPollInterval`, `MaxConcurrent`, `PersistState` | `internal/wormhole/wormhole.go` | `TestDefaultConfig` |
| W0-017.2 | Default configuration values | `DefaultConfig()` returns `Config` with `127.0.0.1:9999`, `pid`, `state`, `1h`, `true`, `targeted`, `3s`, `5`, `true` | `internal/wormhole/wormhole.go` | `TestDefaultConfig` |
| W0-017.3 | Message type | `Message` struct with `ID`, `From`, `To`, `Content`, `Timestamp`, `State` | `internal/wormhole/wormhole.go` | `TestNewMessage` |
| W0-017.4 | Message creation | `NewMessage(from, to, typ, content)` generates ID, sets timestamp, state="pending" | `internal/wormhole/wormhole.go` | `TestNewMessage` |
| W0-017.5 | Work directory | `WorkDir(root)` returns `<root>/.starfleet-ai/var/wormholes` | `internal/wormhole/wormhole.go` | `TestPIDFile`, `TestStateFile`, `TestConfigFile` |
| W0-017.6 | Ensure directory creation | `EnsureDir(root)` creates wormholes dir | `internal/wormhole/wormhole.go` | `TestStartStopDaemon` |
| W0-017.7 | PID file path | `PIDFile(root)` returns `<root>/.starfleet-ai/var/wormholes/pid` | `internal/wormhole/wormhole.go` | `TestPIDFile` |
| W0-017.8 | State file path | `StateFile(root)` returns `<root>/.starfleet-ai/var/wormholes/state` | `internal/wormhole/wormhole.go` | `TestStateFile` |
| W0-017.9 | Config file path | `ConfigFile(root)` returns `<root>/.starfleet-ai/conf/wormholes.yaml` | `internal/wormhole/wormhole.go` | `TestConfigFile` |
| W0-017.10 | Load config from file | `LoadConfig(root)` reads YAML, falls back to defaults on error | `internal/wormhole/wormhole.go` | — |
| W0-017.11 | YAML config parsing | `parseYAMLConfig()` parses `listen_addr`, `pid_file`, `state_file`, `max_message_age`, `auto_start`, `forward_mode`, `worker_poll_interval`, `max_concurrent`, `persist_state` | `internal/wormhole/wormhole.go` | — |

## Requirement ID: W0-018 — Wormhole Daemon Lifecycle

| # | Requirement | Implementation | File | Test |
|---|-------------|---------------|------|------|
| W0-018.1 | Start daemon | `StartDaemon(root)` writes PID file, state file, sets status to "running" | `internal/wormhole/wormhole.go` | `TestStartStopDaemon` |
| W0-018.2 | Prevent double start | `StartDaemon` returns error if daemon already running | `internal/wormhole/wormhole.go` | `TestStartStopDaemon` |
| W0-018.3 | Write PID file | `StartDaemon` writes current PID to PID file | `internal/wormhole/wormhole.go` | `TestStartStopDaemon` |
| W0-018.4 | Write state file | `StartDaemon` writes JSON state with "starting" → "running" | `internal/wormhole/wormhole.go` | `TestStartStopDaemon` |
| W0-018.5 | Stop daemon | `StopDaemon()` writes "stopped" state, removes PID file | `internal/wormhole/wormhole.go` | `TestStartStopDaemon` |
| W0-018.6 | Prevent double stop | `StopDaemon` returns error if daemon not running | `internal/wormhole/wormhole.go` | `TestStartStopDaemon` |
| W0-018.7 | Status query | `Status()` returns PID, status, started time | `internal/wormhole/wormhole.go` | `TestStartStopDaemon` |
| W0-018.8 | Is running check | `IsRunning()` returns true if daemon active | `internal/wormhole/wormhole.go` | — |
| W0-018.9 | Run loop | `RunLoop(root)` polls messages, handles SIGINT/SIGTERM | `internal/wormhole/wormhole.go` | — |

## Requirement ID: W0-024 — Message Handling

| # | Requirement | Implementation | File | Test |
|---|-------------|---------------|------|------|
| W0-024.1 | Message queue | `MessageQueue` with `Enqueue`, `Dequeue`, `ListPending` | `internal/wormhole/messages.go` | `TestMessageQueue` |
| W0-024.2 | Enqueue message | `Enqueue(msg)` adds message to queue | `internal/wormhole/messages.go` | `TestMessageQueue` |
| W0-024.3 | Dequeue message | `Dequeue()` returns pending message, marks as "delivered" | `internal/wormhole/messages.go` | `TestMessageQueue` |
| W0-024.4 | List pending | `ListPending()` returns all pending messages | `internal/wormhole/messages.go` | `TestMessageQueue` |
| W0-024.5 | Empty queue dequeue | `Dequeue()` on empty queue returns nil | `internal/wormhole/messages.go` | `TestMessageQueue` |
| W0-024.6 | Route message | `RouteMessage(root, msg)` writes JSON to target ship's comms inbox | `internal/wormhole/messages.go` | `TestRouteMessage` |
| W0-024.7 | Route to unseen | Message written to `<root>/.starfleet-ai/var/comms/msgs/<to>/unseen/<id>.json` | `internal/wormhole/messages.go` | `TestRouteMessage` |
| W0-024.8 | Message state transition | `RouteMessage` sets state to "delivered" on success | `internal/wormhole/messages.go` | `TestRouteMessage` |
| W0-024.9 | Parse from file | `parseMessageFromFile(data)` parses JSON message file | `internal/wormhole/wormhole.go` | — |
| W0-024.10 | Process pending | `processPendingMessages(root)` scans comms inbox, routes messages | `internal/wormhole/wormhole.go` | — |

## Requirement ID: W0-026 — CLI Integration

| # | Requirement | Implementation | File | Test |
|---|-------------|---------------|------|------|
| W0-026.1 | CLI command `status` | `wormhole status` shows PID, status, started time | `cmd/starfleetctl/main.go`, `internal/wormhole/cli.go` | — |
| W0-026.2 | CLI command `sync` | `wormhole sync` synchronizes message state | `internal/wormhole/cli.go` | — |
| W0-026.3 | CLI command `start` | `wormhole start` starts the daemon | `internal/wormhole/cli.go` | — |
| W0-026.4 | CLI command `stop` | `wormhole stop` stops the daemon | `internal/wormhole/cli.go` | — |
| W0-026.5 | CLI command `restart` | `wormhole restart` stops then starts daemon | `internal/wormhole/cli.go` | — |
| W0-026.6 | Subcommand registration | `wormhole` added to `starfleetctl` main switch | `cmd/starfleetctl/main.go` | — |
| W0-026.7 | Import wormhole package | `main.go` imports `internal/wormhole` | `cmd/starfleetctl/main.go` | — |

## Requirement ID: W0-026b — Configuration File

| # | Requirement | Implementation | File | Test |
|---|-------------|---------------|------|------|
| W0-026b.1 | Config file template | `.starfleet-ai/conf/wormholes.yaml` with all config options | `.starfleet-ai/conf/wormholes.yaml` | — |

## Requirement ID: W0-026c — Gitignore Entry

| # | Requirement | Implementation | File | Test |
|---|-------------|---------------|------|------|
| W0-026c.1 | Gitignore wormholes dir | `/var/wormholes/` added to `.starfleet-ai/.gitignore` | `.starfleet-ai/.gitignore` | — |

## Requirement ID: W0-026d — Documentation

| # | Requirement | Implementation | File | Test |
|---|-------------|---------------|------|------|
| W0-026d.1 | Wormhole documentation | `doc/wormhole.md` with architecture, config, CLI, message flow | `doc/wormhole.md` | — |
| W0-026d.2 | Config documentation | `doc/config.md` updated with wormholes.yaml section | `doc/config.md` | — |

## Requirement ID: W0-026e — Unit Tests

| # | Requirement | Implementation | File | Test |
|---|-------------|---------------|------|------|
| W0-026e.1 | Default config test | `TestDefaultConfig` verifies all default values | `internal/wormhole/wormhole_test.go` | `TestDefaultConfig` |
| W0-026e.2 | NewMessage test | `TestNewMessage` verifies message fields | `internal/wormhole/wormhole_test.go` | `TestNewMessage` |
| W0-026e.3 | PIDFile test | `TestPIDFile` verifies path construction | `internal/wormhole/wormhole_test.go` | `TestPIDFile` |
| W0-026e.4 | StateFile test | `TestStateFile` verifies path construction | `internal/wormhole/wormhole_test.go` | `TestStateFile` |
| W0-026e.5 | ConfigFile test | `TestConfigFile` verifies path construction | `internal/wormhole/wormhole_test.go` | `TestConfigFile` |
| W0-026e.6 | MessageQueue test | `TestMessageQueue` verifies Enqueue/Dequeue/ListPending | `internal/wormhole/wormhole_test.go` | `TestMessageQueue` |
| W0-026e.7 | RouteMessage test | `TestRouteMessage` verifies file write and state transition | `internal/wormhole/wormhole_test.go` | `TestRouteMessage` |
| W0-026e.8 | StartStopDaemon test | `TestStartStopDaemon` verifies start/stop lifecycle | `internal/wormhole/wormhole_test.go` | `TestStartStopDaemon` |

## Branch Information

- **Branch**: `work/wormhole-manager-w0-017`
- **Base**: `500f254` (origin/master at time of branching)
- **Worktree**: `/home/nekrad/src/xorg/mpbt-workspace/_WORK_/starfleetctl/sources/starfleetctl`
- **HEAD**: To be set after first commit

## Status

- **Implementations complete**: W0-017.1–W0-017.11, W0-018.1–W0-018.9, W0-024.1–W0-024.10, W0-026.1–W0-026.7, W0-026b.1, W0-026c.1, W0-026d.1–W0-026d.2, W0-026e.1–W0-026e.8
- **Tests**: All 8 unit tests pass
- **Build**: `go build .` successful
- **Next**: Commit to work branch, create PR to master for Enterprise review
