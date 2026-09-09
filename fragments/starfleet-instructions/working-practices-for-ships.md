---
slug: starfleet-instructions/working-practices-for-ships
title: "Working practices (standing instructions for ships)"
order: 20
owner: "starfleetctl"
---

<!-- Auto-installed by `starfleetctl sop install-starfleet` into sop.d/starfleet-instructions/working-practices-for-ships.md — do not hand-edit the installed copy; edit this source fragment in the starfleetctl repo instead. -->

## Working practices (standing instructions for ships)

These apply to **every** session — they keep knowledge and tooling from decaying as sessions are
cleared:

- **Background / auto ships never prompt on their console.** If `STARFLEET_LAUNCH_TYPE` is
   `background` or `auto` (detached, no human at the terminal), do NOT ask clarifying questions on
   the console and never block waiting for stdin — there is nobody there to answer. Act autonomously
   from the directive you were given; if you genuinely must ask, do it ONLY over the agent bus
   (`starfleetctl comms ask "<question>"` or `tell <sender>`), which the praetor/another ship can
   answer asynchronously. Terminal-launched ships (launch type `terminal`) may interact with the
   human at the console as normal.
- **Always call starfleetctl as `.starfleet-ai/bin/starfleetctl`, never bare `starfleetctl`.** The
   workspace binary lives under `.starfleet-ai/bin/` (symlink into the starfleetctl source). A bare
   `starfleetctl` resolves via PATH and can hit a stale/older install (e.g. `~/go/bin`) that predates
   the `agent-bus`→`comms` rename — its `--help` then teaches wrong commands and its messages land in
   the dead `var/agent-bus/` directory where nobody reads them. If `--help` or a subcommand behaves
   unexpectedly, check which binary you are actually invoking (`which starfleetctl`) and use the
   workspace one.
- **You may commit + push directly on the praetor's staging branch without asking** — lessons,
  config tweaks, dashboard updates, whatever the session produced. Generalizing something onto the
  main branch for all users is a deliberate, separate decision the praetor makes per item.
- **Project knowledge lives in the repo, not in per-user agent memory.** Lessons, CI gotchas,
  failure modes, and workflow quirks go into the SOP fragments (`.starfleet-ai/var/sop.d/`) or
  dashboard topic docs — version-controlled and shared with the whole team. A machine-local agent
  memory store is private and invisible to teammates, so it must **not** hold project facts. Never
  create a `memory/` directory inside a source clone.
- **Turn repeated commands into scripts, then authorize them.** If you find yourself running the
  same multi-step command (especially GitHub/`gh` access), factor it into a generic
  `scripts/<name>` (match the existing style) and add allow rules so it runs without a
  confirmation prompt.
- **Bash cwd persists silently across tool calls.** After `cd`-ing into a nested directory for one
   investigation, every later command keeps running there until you explicitly `cd` back. Always use
   an explicit absolute path or `cd` to the workspace root before commands whose output isn't meant
   to land in a subdirectory.
- **Never abort a task because a file access was denied.** A denied `read`/`edit`/`write`/`bash`
  permission is a recoverable tool error, not a reason to give up. When a path is denied, fix the
  approach and retry: use a workspace-relative path (never a root-absolute one like
  `/.starfleet-ai/...` — that is outside the workspace and denied), access dashboard/session data via
  `starfleetctl` commands instead of raw files, or pick an allowed alternative. Then continue the
  task and report what you did.
- **Dashboard & topics: CLI only, never raw files.** All access to the dashboard and its
  topics goes through `starfleetctl dashboard`/`starfleetctl task` subcommands. **NEVER**
  use `Read`/`Edit`/`Write`/`Glob`/`Grep` on `DASHBOARD.md` or `dashboard/topics/*.md` —
  not even "just to look".
- **Ships do NOT act autonomously on startup.** After launch, a ship ONLY registers on the board
   (sets status `idle`) and waits for an explicit directive via comms. No autonomous task pickup,
   no dashboard scanning, no proactive work — wait for a `tell`/`ask`/`broadcast` directive.

Task handling specifics (lifecycle, reports, capture-first) live in the **`starfleet-tasks`**
skill; load it when you take on or work a task.