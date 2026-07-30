---
slug: starfleet-instructions/working-practices-standing-instructions-for-agents
title: "Working practices (standing instructions for agents)"
order: 20
owner: "starfleetctl"
---

<!-- Auto-installed by `starfleetctl agents install-starfleet` into agents.d/starfleet-instructions/working-practices-standing-instructions-for-agents.md — do not hand-edit the installed copy; edit this source fragment in the starfleetctl repo instead. -->

## Working practices (standing instructions for agents)

These apply to **every** session — they keep knowledge and tooling from decaying as sessions are
cleared:

- **Background / auto ships never prompt on their console.** If `STARFLEET_LAUNCH_TYPE` is
   `background` or `auto` (detached, no human at the terminal), do NOT ask clarifying questions on
   the console and never block waiting for stdin — there is nobody there to answer. Act autonomously
   from the directive you were given; if you genuinely must ask, do it ONLY over the agent bus
   (`starfleetctl comms ask "<question>"` or `tell <sender>`), which the praetor/another ship can
   answer asynchronously. Terminal-launched ships (launch type `terminal`) may interact with the
   human at the console as normal.
- **You may commit + push directly on the praetor's staging branch without asking** — lessons,
  config tweaks, dashboard updates, whatever the session produced. Generalizing something onto the
  main branch for all users is a deliberate, separate decision the praetor makes per item.
- **Project knowledge lives in the repo, not in per-user agent memory.** Lessons, CI gotchas,
  failure modes, and workflow quirks go into `.starfleet-ai/agents.d/index.md` or topic docs —
  version-controlled and shared with the whole team. A machine-local agent memory store is private
  and invisible to teammates, so it must **not** hold project facts. Never create a `memory/`
  directory inside a source clone.
- **Turn repeated commands into scripts, then authorize them.** If you find yourself running the
  same multi-step command (especially GitHub/`gh` access), factor it into a generic
  `scripts/<name>` (match the existing style) and add allow rules so it runs without a
  confirmation prompt.
- **Bash cwd persists silently across tool calls.** After `cd`-ing into a nested directory for one
   investigation, every later command keeps running there until you explicitly `cd` back. Always use
   an explicit absolute path or `cd` to the workspace root before commands whose output isn't meant
   to land in a subdirectory.
- **Ships do NOT act autonomously on startup.** After launch, a ship ONLY registers on the board
   (sets status `idle`) and waits for an explicit directive via comms. No autonomous task pickup,
   no dashboard scanning, no proactive work — wait for a `tell`/`ask`/`broadcast` directive.
