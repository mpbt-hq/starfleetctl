# agent-bus

Cross-session messaging and status board for coordinating ships.

## Synopsis

```
starfleetctl agent-bus <command> [args...]
```

## How It Works

Each ship writes a heartbeat file and reads messages from a shared
state directory underneath `.starfleet-ai/var/...`.

## Message Format

Messages are JSON files stored in `msgs/<target>/unseen/`:

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

**Fields:**

| Field | Required | Description |
|-------|----------|-------------|
| `id` | yes | Unique message ID (`msg-<random>`) |
| `epoch` | yes | Unix timestamp |
| `iso` | yes | ISO 8601 timestamp |
| `from` | yes | Sender ship ID |
| `target` | yes | Recipient ship ID or `all` for broadcast |
| `text` | yes | Message body |
| `type` | yes | Message type (see below) |
| `reply_to` | no | ID of message being replied to |
| `attach` | no | Attachment filename if present |

**Message types (`type` field):**

| Type | Plugin behavior | CLI command |
|------|-----------------|-------------|
| `ship` | Injected as system prompt (directive) | `tell`, `broadcast` |
| `user` | Injected as system prompt (directive) | `tell`, `broadcast` |
| `control` | Injected as system prompt (directive) | `tell`, `broadcast` |
| `command` | Executed locally by plugin, NOT injected | `cmd` |

**Commands** (`type=command`) — executed by opencode plugin's `handleMessage()`:

| Verb | Args | Effect |
|------|------|--------|
| `model` | `<model-name>` | Switch session model (e.g. `model gpt-4o`) |
| `quit` | — | Gracefully shut down the session |
| `reset` | — | Clear session conversation |
| `status` | — | Report status back to sender |

```sh
# Commands (executed, not injected as text)
starfleetctl agent-bus cmd Voyager model gpt-4o
starfleetctl agent-bus cmd Voyager quit
starfleetctl agent-bus cmd Voyager reset

# Directives (injected as system prompt)
starfleetctl agent-bus tell Voyager "run tests"
starfleetctl agent-bus broadcast "roll call"
```

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
# Send a message to a specific ship
starfleetctl agent-bus tell Voyager "run tests on branch feature-x"

# Ship names with spaces MUST be quoted
starfleetctl agent-bus tell 'Wild Mary' "check status"

# Broadcast to all ships
starfleetctl agent-bus broadcast "build is broken, hold off"

# Send large payloads via stdin (bypasses ARG_MAX limit)
cat report.txt | starfleetctl agent-bus tell Voyager --stdin

# Check your inbox
starfleetctl agent-bus inbox

# Acknowledge a directive (removes from inbox)
starfleetctl agent-bus ack m0042
starfleetctl agent-bus ack m0042 "done"
```

**Note:** `tell` and `cmd` print a warning to stderr when the target ship has no heartbeat on the board (e.g. not running or stale). The message is still delivered — the warning is informational only.

### Ask/Reply (Blocking Questions)

```sh
# Ask the flagship a question (blocks until reply)
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

Reads/writes JSON message format. Go and bash sessions can operate on the same bus concurrently.
