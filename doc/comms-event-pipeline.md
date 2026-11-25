# Comms event delivery pipeline

Status: design baseline for issue #23 (2026-11-25).

## Goals

Unify the two existing inbound-message delivery paths without making the Go
Comms layer depend on OpenCode hook names or insertion locations:

- one generic poll operation and versioned event envelope;
- one plugin-side event dispatcher and prompt preparation path;
- one Comms-owned formatter for the complete prompt text;
- two thin OpenCode delivery adapters: current-turn context and asynchronous
  new-turn submission;
- explicit, retryable lifecycle reporting.

This is an incremental migration. Existing behavior must remain available until
the new path has equivalent coverage.

## Responsibility boundary

### Go / Comms

Go owns message-to-prompt composition: sender and context prefixes, grouping,
separators, and the complete text to be injected. The event payload carries
that text as a string or string array.

Go does not know about OpenCode SDK types, hook names, `output.system`,
`promptAsync`, or the position where text is inserted.

### Plugin

The plugin owns:

- polling and validating the generic event envelope;
- event dispatch and in-process deduplication;
- adapting event payloads into a common prepared-prompt value;
- choosing the appropriate OpenCode delivery adapter;
- reporting lifecycle transitions that it can actually observe.

The plugin must not reimplement Comms-specific message formatting.

## Event envelope (illustrative V1)

```json
{
  "protocol_version": 1,
  "events": [
    {
      "id": "evt-123",
      "type": "prompt.inject",
      "source": {
        "kind": "comms.message",
        "id": "m123"
      },
      "payload": {
        "prompt": [
          "--- fleet directives ---",
          "Directive m123 from Enterprise:",
          "Please report status.",
          "--- end fleet directives ---"
        ]
      },
      "actions": {
        "start_turn": true
      }
    }
  ]
}
```

This is a design example, not yet a frozen wire schema. Event IDs are stable
across retries. Polling is non-destructive: returning an event does not consume
or acknowledge it. Unknown event types must be safely rejected or skipped with
an observable diagnostic, not interpreted as prompt text.

The `prompt` field is the complete Comms-rendered text. A string is valid for
one contiguous prompt; a string array preserves the formatter's ordered
segments. The plugin may join segments when an OpenCode API requires one text
field, but must not add sender prefixes, separators, or other Comms formatting.

## Shared plugin pipeline

Both existing paths use the same internal value:

```ts
interface PreparedPrompt {
  eventIds: string[];
  parts: string[];
  startTurn: boolean;
}
```

A single event dispatcher owns polling, validation, ordering, deduplication,
and the pending queue. There must be only one in-flight poll at a time. Hooks
must not independently call the inbox/poll command.

The dispatcher converts `prompt.inject` events into `PreparedPrompt` once.
Delivery mode is a plugin-internal strategy, not a field in the Go contract:

- `current_turn`: append the prepared text to the current turn's system
  context;
- `new_turn`: wrap the same prepared text in the OpenCode API's asynchronous
  prompt request and start a new turn when requested.

The adapters differ only in OpenCode-specific placement and invocation. They
must not format messages or independently decide which events have already
been delivered.

A coordinator/lease within the plugin prevents the two adapters from
submitting the same event concurrently. A failed submission remains eligible
for retry. In-memory deduplication is an optimization, not durable proof of
delivery across restarts.

## Lifecycle and acknowledgement semantics

Do not overload the existing `seen_mark` operation. At minimum, distinguish:

- `offered`: event returned by poll;
- `applied` (or `submitted`): adapter accepted the prompt for insertion /
  submission;
- `turn_started`: the associated model turn is observed to have started;
- `completed`: processing completion is explicitly observed;
- `failed`: a stage failed, with a retryable/permanent classification.

A transition is reported only when its evidence exists. In particular,
successful return from a prompt submission API is not proof that a model turn
started or completed. Existing `seen_mark` compatibility must be preserved or
migrated explicitly; it must not silently acquire stronger semantics.

The durable event state and lease/retry policy belong in Comms. Plugin-local
state may prevent duplicate concurrent work but cannot be the source of truth.

## OpenCode compatibility prerequisite

Before implementing or freezing adapter signatures, capture the exact deployed
OpenCode CLI/runtime version and the corresponding plugin SDK/API from a
representative ship. The repository's plugin SDK dependency is currently
unbounded and the checked-in plugin uses the legacy V1 hook/client surface.
The Starfleet plugin version is not the OpenCode runtime version.

Do not mix V1 and V2 API shapes or infer runtime behavior from current
documentation alone. Record the observed version, hook signatures, and
observable turn-start/completion events in tests or a compatibility note.

### Observed deployment baseline (2026-11-25)

The owner-requested read-only probe was performed on **Discovery**:

- Command: `opencode --version`
- Exact output: `1.19.3`
- Timestamp: `2026-11-25T17:22:47Z`
- Resolved SDK: `@opencode-ai/plugin 1.19.3`, read from
  `.opencode/node_modules/@opencode-ai/plugin/package.json`
- Wormhole correlation: request
  `w0-20261125T172016Z-6cdb81958af49800`, Comms message `m25551`,
  reply `m25554`, reported by Enterprise in
  `w0-20261125T172427Z-213efe6684c9b70a`.

The upstream OpenCode tag `v1.19.3` confirms the checked-in legacy adapter
surface is present in this deployed version:

- `Hooks` declares `chat.params` and
  `experimental.chat.system.transform` in
  `packages/plugin/src/index.ts`.
- The generated SDK exposes `client.session.promptAsync()` at
  `/session/{id}/prompt_async`; its API comment says it starts a session
  message and returns immediately.
- The event schema includes `message.updated` with the updated message and
  `session.idle` with a session ID.

This validates retaining the existing V1 hooks and `promptAsync` adapter for
the first migration slice; do not switch to the V2 plugin API. The
`session.idle` event is session-scoped and has no message/event ID, so it is
not by itself proof that a particular Comms directive was processed. Turn
start/completion still require explicit correlation and must not be inferred
from `promptAsync` returning.

## Implemented event wire contract (protocol v1)

The plugin polls with `{"cmd":"events.poll"}`. The response is a versioned,
non-destructive event envelope:

```json
{
  "ok": true,
  "protocol_version": 1,
  "events": [
    {
      "id": "m123",
      "type": "prompt.inject",
      "source": {
        "kind": "comms.message",
        "id": "m123",
        "from": "Enterprise",
        "target": "Voyager",
        "message_type": "ship"
      },
      "payload": {
        "prompt": "--- fleet directives ...\nDirective m123 from Enterprise:\n...\n\n--- end fleet directives ---\n\n"
      },
      "actions": { "start_turn": true }
    },
    {
      "id": "m124",
      "type": "command.execute",
      "source": {
        "kind": "comms.message",
        "id": "m124",
        "from": "Enterprise",
        "message_type": "command"
      },
      "payload": { "command": "task status" }
    },
    {
      "id": "m125",
      "type": "unsupported",
      "source": {
        "kind": "comms.message",
        "id": "m125",
        "message_type": "future-type"
      },
      "payload": {}
    }
  ]
}
```

- Event IDs are stable Comms message IDs; source metadata is preserved.
- Go owns the complete prompt text, including sender/context prefix and
  separators. A prompt may be a string or string array; the plugin concatenates
  supplied parts without adding Comms-specific formatting.
- `prompt.inject` carries `payload.prompt` and generic `actions.start_turn`.
- `command.execute` preserves existing command and model-shorthand behavior
  during migration. Later work can replace individual commands with typed
  events such as model switch, quit, or reset.
- Unknown source message types become `unsupported`; the plugin does not
  acknowledge them, so they remain pending for explicit handling.
- Polling is non-destructive. Existing `seen_mark` remains the submission
  acknowledgement and is not evidence of turn start or completion.
- `init` returns the same versioned `events` plus legacy `messages`, so old
  plugins continue to work. The new plugin falls back to `inbox` only when
  paired with a CLI that does not implement `events.poll`.

## Incremental implementation plan

1. **Compatibility baseline:** complete — Discovery reports OpenCode and
   plugin SDK 1.19.3; the V1 hooks, promptAsync endpoint, and session event
   shapes were checked against upstream tag v1.19.3.
2. **Shared prompt preparation:** complete — both adapters consume one
   OpenCode-independent PreparedPrompt; string/string[] payloads are validated
   without modifying the Go-owned prompt text.
3. **Shared poller and event dispatcher:** complete — both OpenCode hooks use
   one single-flight event poller/coordinator. Startup events and live polls
   are deduplicated; one adapter leases an event at a time; failed handoffs
   release leases; unsupported event types remain pending.
4. **Generic Go event contract:** complete — protocol v1 is implemented in
   `events.poll`; Go formats prompt strings and classifies prompt, command,
   and unsupported events. `init` returns both events and legacy messages.
   The old inbox command remains for older plugins.
5. **Lifecycle:** next — add distinct submitted, turn-started, completed, and
   failed reporting with durable idempotency and restart recovery.
6. **Future actions:** model switch, quit, reset, and similar operations become
   typed event handlers; they do not become special cases in the polling loop.

## Required tests

- Formatter output is identical regardless of delivery adapter.
- Multiple messages preserve order and grouping.
- A single event cannot be submitted by both adapters or twice concurrently.
- Poll failure, malformed response, unknown event, and adapter failure are
  observable and do not silently consume the event.
- Retry after failure and recovery after plugin restart are idempotent.
- Current-turn and new-turn adapters preserve their existing behavior.
- Lifecycle tests prove that offered, submitted, turn-started, and completed
  are distinct.
- An end-to-end test covers Comms message -> poll -> prepared prompt -> adapter
  insertion -> observed turn start -> completion acknowledgement.

