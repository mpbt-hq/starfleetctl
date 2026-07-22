---
slug: starfleet/agent-bus-opencode-polling
title: "Agent-bus opencode polling"
order: 215
owner: "starfleetctl"
---

<!-- Auto-installed by `starfleetctl agents install-starfleet` into agents.d/starfleet/agent-bus-opencode-polling.md — do not hand-edit the installed copy; edit this source fragment in the starfleetctl repo instead. -->

## Agent-bus opencode polling

opencode has no `Monitor` tool (Claude Code only), so the background
`agent-bus-monitor-loop` cannot surface directives as in-context events.
Instead, the `.opencode/plugins/starfleet-dispatch.ts` plugin handles
incoming messages in two ways:

1. **Visible toast notification** — each new tell/broadcast directive
   appears as a toast popup in the TUI with title `[fleet] <id> von <sender>`
   and the full message text (auto-dismisses after ~10s). This is implemented
   via `client.tui.showToast()`.

2. **System prompt injection** — at the start of each turn, the plugin
   injects unseen directives into the system prompt via
   `experimental.chat.system.transform` (as before). No manual check
   command is needed — the model sees directives automatically in context.

If new directives are shown, the assistant should handle them as it would
if they had surfaced via a Monitor event — ack, act, or defer as
appropriate. The plugin shares dedup state with `agent-bus-monitor-loop`
so the same message is only shown once regardless of which mechanism
surfaced it first.

The legacy `autoPong()` responder (hardcoded to answer only Enterprise
pings) has been removed. Ships now handle all messages natively through
their model.

### Rules for the assistant

1. **Never manually call `starfleetctl agent-bus inbox`.** The poller already injects
   new messages into the next turn's context. Doing so wastes a turn and
   is redundant.

2. **Never call `starfleetctl agent-bus --help`.** The full interface (tell, ack, ask,
   board, status, etc.) is documented in the inter-ship-communication
   fragment (typically `agents.d/inter-ship-communication.md` or
   `agents.d/starfleet/inter-ship-communication.md`). These fragments are
   loaded every session — reach for the docs, not the `--help` output.

### Model tracking and model-API errors

The plugin tracks the **currently active model** in-memory via opencode's
`message.updated` events — it reads `modelID`/`providerID` from each
assistant message. It does **not** poll the API, and it is multi-session
safe (it never guesses across parallel sessions in the same project). The
active model is written to the ship's status record after the first
assistant turn.

The plugin also watches `session.error` events and classifies model-API
failures via `classifyModelError()`:

- `nim-overload` — NIM (NVIDIA inference microservice) overloaded: 5xx,
  connection reset / refused, gateway timeouts.
- `zen-ratelimit` — ZEN temporarily blocks the account: HTTP 429, "usage
  limit", "quota exceeded", "request limit reached", "access denied",
  "try again later".
- `resource-exhausted` — worker/model capacity reached: gRPC
  `ResourceExhausted` ("Worker local total request limit reached"),
  token quota, or context-length / maximum-context exceeded.

The detected class is written into the status record as `error_tag` and
reported to the flagship (Enterprise) as part of the error message. A
subsequent successful assistant turn auto-clears `error_tag` and the
`blocked` state (auto-recovery).

**Rule for the assistant:** if you receive a `zen-ratelimit` (usage cap
reached) or `nim-overload` error from the model API, do **not** keep
retrying the same model — switch to a different provider/model (use the
web `models.yaml` / `starfleetctl` model list) and continue. The fleet
console and flagship already see the `error_tag`, so no extra reporting is
needed beyond resuming your task on the alternate model.

### External model switching (setModel directive)

Ships can switch their model at runtime via an agent-bus directive — no
session restart required. Any ship or the web frontend can trigger this:

```sh
starfleetctl agent-bus tell <ship> "setModel <model-name>"
```

Example:
```sh
starfleetctl agent-bus tell Enterprise "setModel opencode/big-pickle"
starfleetctl agent-bus tell Yamato "setModel anthropic/claude-sonnet-4"
```

The dispatch plugin parses the `setModel` directive from the inbox,
executes `client.session.switchModel()` on the opencode runtime, and
logs the result to the tick log. A toast notification confirms the
switch (success or failure). The status record is updated with the new
model after the next assistant turn.

**Limitations:**
- Only one `setModel` per error-recovery cycle (the `hasSwitchedToFallback`
  flag prevents double-switches during automated error handling; external
  `setModel` directives bypass this guard).
- The model name must match an entry in the configured model list.
- The directive is fire-and-forget — no RPC response to the sender.
  Check the tick log (`.starfleet-ai/var/agent-bus/poll/<SHIP>.tick`) or status
  record to verify the switch succeeded.
