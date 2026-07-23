# comms

Cross-session messaging and status board for coordinating ships.

## Synopsis

```
starfleetctl comms <command> [args...]
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
starfleetctl comms cmd Voyager model gpt-4o
starfleetctl comms cmd Voyager quit
starfleetctl comms cmd Voyager reset

# Directives (injected as system prompt)
starfleetctl comms tell Voyager "run tests"
starfleetctl comms broadcast "roll call"
```

## Environment Variables

| Variable | Default | Description |
|---|---|---|
| `STARFLEET_SHIP_ID` | `user@hostname` | Unique ship identifier |
| `STARFLEET_BUS_DIR` | `.starfleet-ai/var/comms` | Storage directory |
| `STARFLEET_STARFLEET_BUS_TTL` | `900` (15 min) | Heartbeat time-to-live |
| `PROJECT` | — | Project label on the board |
| `AGENT_CONTROLLER` | `control` | Control agent for `ask`/`reply` |

## Commands

### Status and Heartbeat

```sh
# Post/update your status
starfleetctl comms status working "building feature X"
starfleetctl comms status idle
starfleetctl comms status blocked "waiting for review"

# Refresh heartbeat without changing state (for monitor loops)
starfleetctl comms touch

# Remove your heartbeat (call on session exit)
starfleetctl comms clear
```

### Board

```sh
# See all ships and their status
starfleetctl comms board

# Machine-readable output
starfleetctl comms board --json
```

Output columns: `AGENT`, `PROJECT`, `STATE`, `AGE`, `INBOX`, `ATTACH`, `NOTE`. Stale entries (older than `STARFLEET_STARFLEET_BUS_TTL`) are marked `[STALE]`.

### Directives (Messaging)

```sh
# Send a message to a specific ship
starfleetctl comms tell Voyager "run tests on branch feature-x"

# Ship names with spaces MUST be quoted
starfleetctl comms tell 'Wild Mary' "check status"

# Broadcast to all ships
starfleetctl comms broadcast "build is broken, hold off"

# Send large payloads via stdin (bypasses ARG_MAX limit)
cat report.txt | starfleetctl comms tell Voyager --stdin

# Check your inbox
starfleetctl comms inbox

# Acknowledge a directive (removes from inbox)
starfleetctl comms ack m0042
starfleetctl comms ack m0042 "done"
```

**Note:** `tell` and `cmd` print a warning to stderr when the target ship has no heartbeat on the board (e.g. not running or stale). The message is still delivered — the warning is informational only.

### Ask/Reply (Blocking Questions)

```sh
# Ask the flagship a question (blocks until reply)
starfleetctl comms ask "should I force-push?"

# Custom controller and timeout
starfleetctl comms ask "approve PR?" --to control --timeout 30

# Reply to a pending question (control side)
starfleetctl comms reply m0042 "yes, proceed"

# List pending questions (control side)
starfleetctl comms asks
```

### Large Payloads

Messages over 768 bytes are auto-spilled into attachments:

```sh
# Retrieve a large payload
starfleetctl comms get m0042              # print to stdout
starfleetctl comms get m0042 --out file  # write to file
```

### Maintenance

```sh
# List all directives (control side)
starfleetctl comms msgs

# Show recent events
starfleetctl comms events 20

# Garbage-collect stale entries
starfleetctl comms prune
```

## Interoperability

Reads/writes JSON message format. Go and bash sessions can operate on the same bus concurrently.
