# agent-bus

Cross-session messaging and status board for coordinating agent sessions.

## Synopsis

```
starfleetctl agent-bus <command> [args...]
```

## How It Works

Each agent session ("ship") writes a heartbeat file and reads messages from a shared
state directory underneath `.starfleet-ai/var/...`.

## Environment Variables

| Variable | Default | Description |
|---|---|---|
| `STARFLEET_SHIP_ID` | `user@hostname` | Unique ship identifier |
| `STARFLEET_BUS_DIR` | `./.starfleet-ai/var/agent-bus` | Storage directory |
| `STARFLEET_STARFLEET_BUS_TTL` | `900` (15 min) | Heartbeat time-to-live |
| `PROJECT` | — | Project label on the board |
| `AGENT_CONTROLLER` | `control` | Control agent for `ask`/`reply` |

## Commands

### Status and Heartbeat

```sh
# Post/update your status
starfleetctl agent-bus status working "building feature X"
starfleetctl agent-bus status idle
starfleetctl agent-bus status blocked "waiting for review"

# Refresh heartbeat without changing state (for monitor loops)
starfleetctl agent-bus touch

# Remove your heartbeat (call on session exit)
starfleetctl agent-bus clear
```

### Board

```sh
# See all ships and their status
starfleetctl agent-bus board

# Machine-readable output
starfleetctl agent-bus board --json
```

Output columns: `AGENT`, `PROJECT`, `STATE`, `AGE`, `INBOX`, `ATTACH`, `NOTE`. Stale entries (older than `STARFLEET_STARFLEET_BUS_TTL`) are marked `[STALE]`.

### Directives (Messaging)

```sh
# Send a message to a specific agent
starfleetctl agent-bus tell Voyager "run tests on branch feature-x"

# Ship names with spaces MUST be quoted
starfleetctl agent-bus tell 'Wild Mary' "check status"

# Broadcast to all agents
starfleetctl agent-bus broadcast "build is broken, hold off"

# Send large payloads via stdin (bypasses ARG_MAX limit)
cat report.txt | starfleetctl agent-bus tell Voyager --stdin

# Check your inbox
starfleetctl agent-bus inbox

# Acknowledge a directive (removes from inbox)
starfleetctl agent-bus ack m0042
starfleetctl agent-bus ack m0042 "done"
```

### Ask/Reply (Blocking Questions)

```sh
# Ask the control agent a question (blocks until reply)
starfleetctl agent-bus ask "should I force-push?"

# Custom controller and timeout
starfleetctl agent-bus ask "approve PR?" --to control --timeout 30

# Reply to a pending question (control side)
starfleetctl agent-bus reply m0042 "yes, proceed"

# List pending questions (control side)
starfleetctl agent-bus asks
```

### Large Payloads

Messages over 768 bytes are auto-spilled into attachments:

```sh
# Retrieve a large payload
starfleetctl agent-bus get m0042              # print to stdout
starfleetctl agent-bus get m0042 --out file  # write to file
```

### Maintenance

```sh
# List all directives (control side)
starfleetctl agent-bus msgs

# Show recent events
starfleetctl agent-bus events 20

# Garbage-collect stale entries
starfleetctl agent-bus prune
```

## Interoperability

Reads/writes the same TSV format as the bash original (`scripts/agent-bus`). Go and bash sessions can operate on the same bus concurrently.
