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
- **Worktrees & agent clones: IMMER starfleet-Tooling, nie direkt `git worktree`/`git clone`.**
  Für temporäre/isolierte Arbeit: `starfleetctl worktree add repos/<repo> <name>` (→ `_WORK_/worktrees/<repo>/<name>`,
  Branch `wt/<name>`, per `worktree list/remove/prune` verwaltet). Für PRs: `starfleetctl github pr checkout <pr#>`
  bzw. `github pr mk-agent-clone <branch> [name]` (isolierte PR-Clones — deren Pfad wartet das PR-Tooling ab).
  Rohes `git worktree`/manuelle Clones erzeugen verstreute Orte, Orphan-Branches und Races zwischen Ships.
  Mutierende git-Operationen im selben Clone mit `starfleetctl with-clone-lock <cmd...>` serialisieren (derselbe
  `mpbt-clone.lock`, den `ws-commit` nutzt). Vor jeder Arbeit erst prüfen, ob ein anderes Schiff im Repo aktiv ist
  (siehe inter-ship-communication).
- **Source-Checkouts nur an den erlaubten Orten — NIE im Workspace-Root, `_WORK_`-Root, `_WORK_/tmp` oder
  in ad-hoc Verzeichnissen.** Erlaubt sind ausschließlich: die mpbt-managed Clone
  (`_WORK_/<solution>/sources/**`), starfleet-Worktrees (`_WORK_/worktrees/<repo>/<name>`) und
  PR-/Agent-Clones (`github pr checkout` / `mk-agent-clone`). Insbesondere gilt: **kein `git checkout`/`git clone`
  direkt im mpbt-workspace-Root** (das beschädigt den Agent-Config-Checkout des Root-Repos) und **kein
  vollständiger Source-Clone in `_WORK_/tmp` oder unter einem Repo-Nachbarn**. Bei Unsicherheit: erst
  `starfleetctl worktree add` bzw. das passende mpbt/PR-Tooling nutzen.
- **Vor JEDEM mutierenden Git-Befehl: `rev-parse --show-toplevel` prüfen!** `git checkout|switch|pull|
  reset|rebase|cherry-pick|stash` läuft NUR innerhalb des tatsächlich gemeinten Repos. Der Workspace-Root
  hat sein eigenes `.git` (das Agent-Config-Repo `/home/nekrad/src/xorg/mpbt-workspace`) — ein
  `git checkout <xserver-branch>` dort wechselt/erzeugt den Branch **im falschen Repo** (Footgun, bereits
  mehrfach passiert: `wip/*`, `wt/*`, `xserver/master`-Refs landeten im Workspace-Repo; Worktrees wurden
  als Workspace-Repo-Worktrees statt als xserver-Worktrees angelegt). Guard-Befehl VOR jeder Aktion:
  `git rev-parse --show-toplevel` — Ergebnis muss exakt dem gewünschten Clone/Worktree entsprechen
  (z.B. `…/_WORK_/xserver-master/sources/xlibre/xserver` bzw.
  `…/_WORK_/worktrees/xserver/<name>`), sonst erst `cd` in das richtige Verzeichnis oder
  `starfleetctl worktree add <expliziter-repo-pfad>` (Ort danach verifizieren).
- **Primary/shared Clone ist passiv.** Branch-Wechsel, Rebases, `--amend`, Force-Push-Vorbereitung usw.
  gehören ausschließlich in den EIGENEN separaten Worktree/PR-Clone — nie im primären mpbt-Clone. Andere
  Ships lesen/bauen dort. Für aktive Arbeit: `starfleetctl worktree add _WORK_/xserver-master/sources/xlibre/xserver <name>`
  bzw. `github pr checkout`.
- **Dashboard & topics: CLI only, never raw files.** All access to the dashboard and its
  topics goes through `starfleetctl dashboard`/`starfleetctl task` subcommands. **NEVER**
  use `Read`/`Edit`/`Write`/`Glob`/`Grep` on `DASHBOARD.md` or `dashboard/topics/*.md` —
  not even "just to look".
- **Ships do NOT act autonomously on startup.** After launch, a ship ONLY registers on the board
   (sets status `idle`) and waits for an explicit directive via comms. No autonomous task pickup,
   no dashboard scanning, no proactive work — wait for a `tell`/`ask`/`broadcast` directive.
- **Keep board status current during long tasks.** Long-running operations (rebases, builds,
   test suites, large edits) must periodically refresh the board heartbeat so the fleet sees
   accurate state. Call `starfleetctl comms status working "<what>"` or `starfleetctl comms touch`
   at regular intervals — e.g., every 10–20 rebase steps, every ~5 minutes during builds, or
   after each major step. A stale `idle`/`working` status misleads the fleet and the flagship.
   Use a timer (`starfleetctl timer set --every 5m --type ship --text "comms touch"`) as a
   self-reminder if needed. The board must reflect reality at all times.

Task handling specifics (lifecycle, reports, capture-first) live in the **`starfleet-tasks`**
skill; load it when you take on or work a task.