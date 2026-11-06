# Wormhole Manager Documentation

## Overview

The wormhole manager handles incoming/outgoing messages between fleet ships via
the comms bus. It provides a daemon mode with PID/State files, configuration via
`.starfleet-ai/conf/wormholes.yaml`, and CLI commands for status monitoring,
synchronization, and daemon lifecycle management.

## Architecture

The wormhole manager consists of:

- **Config**: `.starfleet-ai/conf/wormholes.yaml` — daemon configuration
- **Package**: `internal/wormhole` — Go package for message handling and daemon
- **Worker Daemon**: Background process with PID and State files
- **CLI Commands**: `wormhole status/sync/start/stop/restart`

### Directory Layout

```
.starfleet-ai/
├── conf/
│   └── wormholes.yaml           # wormhole configuration
└── var/
    └── wormholes/
        ├── pid                  # daemon PID file
        └── state                # daemon state file
```

## Configuration

See `.starfleet-ai/conf/wormholes.yaml` for the full configuration reference.

### Key Options

| Option | Type | Default | Description |
|--------|------|---------|-------------|
| `listen_addr` | string | `127.0.0.1:9999` | Daemon listen address |
| `pid_file` | string | `pid` | PID file name (in wormholes dir) |
| `state_file` | string | `state` | State file name (in wormholes dir) |
| `max_message_age` | duration | `3600s` | Maximum message age |
| `auto_start` | bool | `true` | Auto-start on flagship launch |
| `forward_mode` | string | `targeted` | `targeted` or `broadcast` |
| `worker.poll_interval` | duration | `3s` | Message polling interval |
| `worker.max_concurrent` | int | `5` | Max concurrent messages |
| `worker.persist_state` | bool | `true` | Persist state across restarts |

## CLI Commands

```
starfleetctl wormhole status    # Show daemon status
starfleetctl wormhole sync      # Synchronize wormhole state
starfleetctl wormhole start     # Start the daemon
starfleetctl wormhole stop      # Stop the daemon
starfleetctl wormhole restart   # Restart the daemon
```

## Message Flow

1. Ship A sends a message via `comms tell ShipB "message"`
2. The message is written to `.starfleet-ai/var/comms/msgs/ShipB/unseen/`
3. The wormhole daemon polls the comms bus
4. Messages are routed to the appropriate ship's inbox
5. Message state is tracked via `state` field (`pending`, `delivered`, `failed`)
