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

## Incremental implementation plan

1. **Compatibility baseline:** obtain deployed OpenCode version/API evidence;
   document the current two paths and their observable lifecycle signals.
2. **Shared prompt preparation:** introduce the `PreparedPrompt` type and one
   formatter-to-prompt conversion path. Keep both existing hooks, but make them
   thin adapters over the same prepared value. This is the first safe code
   slice and should not change the wire protocol yet.
3. **Generic dispatcher:** add one poller, validated event envelope, queue,
   ordering, and single-flight delivery coordination. Remove hook-local inbox
   polling only after parity tests exist.
4. **Go event contract:** add generic event polling and Go-owned prompt
   formatting. Keep the old inbox path during a compatibility window.
5. **Lifecycle:** add distinct applied, turn-started, completed, and failed
   reporting with durable idempotency and restart recovery.
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

