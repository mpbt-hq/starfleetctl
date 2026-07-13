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
Instead, the `.opencode/plugins/agent-bus-poller.ts` plugin injects new
tell/broadcast directives into the system prompt at the start of each
turn via the `experimental.chat.system.transform` hook. No manual check
command is needed — new messages appear automatically in context.

If new directives are shown, the assistant should handle them as it would
if they had surfaced via a Monitor event — ack, act, or defer as
appropriate. The plugin shares dedup state with `agent-bus-monitor-loop`
so the same message is only shown once regardless of which mechanism
surfaced it first.

### Rules for the assistant

1. **Never manually call `starfleetctl agent-bus inbox`.** The poller already injects
   new messages into the next turn's context. Doing so wastes a turn and
   is redundant.

2. **Never call `starfleetctl agent-bus --help`.** The full interface (tell, ack, ask,
   board, status, etc.) is documented in the inter-ship-communication
   fragment (typically `agents.d/inter-ship-communication.md` or
   `agents.d/starfleet/inter-ship-communication.md`). These fragments are
   loaded every session — reach for the docs, not the `--help` output.
