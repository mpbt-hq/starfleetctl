---
slug: starfleet-instructions/inter-ship-communication
title: "Inter-ship communication (comms)"
order: 12
owner: "starfleetctl"
---

<!-- Auto-installed by `starfleetctl sop install-starfleet` into sop.d/starfleet-instructions/inter-ship-communication.md — do not hand-edit the installed copy; edit this source fragment in the starfleetctl repo instead. -->

## Inter-ship communication (comms)

Ships communicate autonomously via `starfleetctl comms`. No central
orchestrator is required — every ship reads its inbox, acts on directives,
and responds. See the `starfleet` skill for the full command reference.

### Standing rules

1. **Always answer broadcast check-ins / roll calls.** When another ship sends
   a broadcast asking all ships to check in, ack the message and reply
   with status.

2. **Ships accept and process tasks autonomously.** If a directive can be
   handled without human intervention, do it and report back. If clarification
   is needed, ask via comms.

3. **Report status proactively.** After any action taken on behalf of another
   ship, send a status update so the fleet knows the state of play.

4. **Keep the board current.** Set your status after starting or finishing
   work so the fleet sees who is idle/working/blocked.

5. **Always respond to bus messages over the bus.** When a `tell` or `ask` arrives
   in your inbox, always send a reply via `starfleetctl comms tell <sender> <reply>`
   — never only process it internally without answering. The sender expects a bus
   response; silence means the message was lost. Ack the message (`comms ack <id>`)
   after responding.

6. **Comms questions: answer via comms AND console.** When a question arrives via
   comms (an `ask` or a `tell` that requests information or a decision), always
   answer in two places: (1) via `starfleetctl comms tell <sender> <answer>` so the
   sender gets the reply on the bus, and (2) output the answer to the local console
   as well, so the human at the terminal can see it.

### Dashboard

The dashboard is the cross-session "what's in flight" index. All access via
`starfleetctl dashboard` subcommands — **never** `Read`/`Edit`/`Write`/`Glob`/`Grep`
on `DASHBOARD.md` or `dashboard/topics/*.md`.

- **Keep the dashboard current.** When you start, pause, or finish a topic,
  update its entry **in the same session**.
- **"Take on / pick up / capture a task" always means: write it into the dashboard,
  NOT just say you'll do it.** Use `starfleetctl task capture` — this is the only
  correct way to accept a task.
- **Notice something worth a look → park it immediately.** Add a dashboard Parked
  entry rather than just mentioning it and moving on.

### Work coordination (avoiding concurrent edits + exchanging findings)

Ships notify each other about what they are working on so nobody unintentionally
edits the same codebase at the same time, and so parallel work on the same problem
stays in sync:

1. **Announce codebase work before starting.** When you begin work on a shared
   source repo (the starfleetctl source, an xserver/driver clone, any workspace
   repo), announce it over comms — which repo, which branch, what you are about
   to do. First check the board and recent comms for another ship already active
   there; if so, coordinate before touching anything.

2. **Only one ship edits the same source at a time.** If another ship has
   announced a codebase as busy, either wait for it to finish (ask via comms) or
   use a separate clone/worktree. Never silently start editing a repo another
   ship is actively working in.

3. **Parallel work on the same problem.** When several ships are assigned the
   same or an overlapping problem (e.g. parallel analyses commissioned for
   cross-checking), they work in parallel BUT must (a) keep their changes in
   separate branches/worktrees so they cannot overwrite each other, and
   (b) exchange findings as they are found — interim results, conclusions and
   decisions go to the other ship(s) AND the commissioning sender via
   `starfleetctl comms tell` immediately, not just in a final report.

4. **Publish discoveries while they are fresh.** Anything relevant to a shared
   problem goes onto the bus (and, per rule 6 above, the local console) the moment
   it is found — the other ship(s) need it now, not after the session ends.

