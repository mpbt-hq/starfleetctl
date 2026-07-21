---
slug: starfleet/working-practices-standing-instructions-for-agents
title: "Working practices (standing instructions for agents)"
order: 20
owner: "starfleetctl"
---

<!-- Auto-installed by `starfleetctl agents install-starfleet` into agents.d/starfleet/working-practices-standing-instructions-for-agents.md — do not hand-edit the installed copy; edit this source fragment in the starfleetctl repo instead. -->

## Working practices (standing instructions for agents)

These apply to **every** session — they keep knowledge and tooling from decaying as sessions are
cleared:

- **Language policy: converse with the praetor and other ships in the project's primary spoken
   language, everything else in English.** Any `starfleetctl agent-bus tell`/`broadcast` text — whether directed
  at the praetor or at another ship — is in the project's primary spoken language (e.g. German for
  the XLibre fleet). Anything that becomes part of the project's durable artifacts stays English:
  code, code comments, commit messages, PR titles/descriptions/review comments, other GitHub
  interactions, and doc files. Applies fleet-wide, not just to the flagship session.
- **Record lessons learned as you go.** Append durable, non-obvious findings to the relevant
  section of `.starfleet-ai/agents.d/index.md` (or a topic doc) **within the session**, not at the end. Session context
  is wiped on session end; only what's written survives.
- **Keep the dashboard current** — it's the cross-session "what's in flight / what got parked"
  index. When you start, pause, or finish a topic (an initiative spanning more than one PR, or a
  decision still pending), update its entry **in the same session**. Use the CLI (`starfleetctl
  dashboard` subcommands) for every read and write — never edit the index file directly.

- **Dashboard files: CLI-only, no exceptions.** NEVER use `Read`/`Edit`/`Write`/`Glob`/`Grep`
  directly on `.starfleet-ai/dashboard/topics/*.md` or `.starfleet-ai/DASHBOARD.md`. All access
  goes through `starfleetctl dashboard topic show|write|new|commit` and `starfleetctl dashboard
  reindex|commit`. This is not optional — agents have been caught hand-editing topic files,
  which breaks the encapsulation needed for future multi-host operation. The pattern is:
  `topic show <slug> > /tmp/t.md` → edit `/tmp/t.md` → `topic write <slug> /tmp/t.md` →
  `topic commit <slug> -m "<msg>"`. For new topics: `topic new --title "…" --kind bug`.
- **Notice something worth a look while doing unrelated work → park it immediately.** A suspicious
   code path, a possible follow-up cleanup, an untriaged idea — add a dashboard Parked entry
   right away rather than just mentioning it in the response and moving on.

- **"Take on / pick up / capture a (new) task" always means: write it into the dashboard, NOT just
   say you'll do it.** When the praetor or another ship hands you a task, or you decide to track a
   to-do for the fleet, run the sanctioned capture command — it records a `dashboard/topics/<slug>.md`
   entry (FORBIDDEN: never create/edit topic files via `Write`/`Edit`/`Read`):
   `starfleetctl task capture --title "<short title>" --desc "<what to do / acceptance criteria>"`
   The command prints `task-captured: slug=…`; forward that summary to the sender. "Aufgabe
   aufnehmen", "neue Aufgabe", "track this" and "capture a task" all mean the same thing. If the task
   is meant for another ship, pass `--assign <ship>`; otherwise leave it open. Small/fast models:
   this is the ONLY correct way to accept a task — do not just acknowledge it in chat.

- **Background / auto ships never prompt on their console.** If `STARFLEET_LAUNCH_TYPE` is
   `background` or `auto` (detached, no human at the terminal), do NOT ask clarifying questions on
   the console and never block waiting for stdin — there is nobody there to answer. Act autonomously
   from the directive you were given; if you genuinely must ask, do it ONLY over the agent bus
   (`starfleetctl agent-bus ask "<question>"` or `tell <sender>`), which the praetor/another ship can
   answer asynchronously. The launch prompt already states this; treat it as binding. Terminal-launched
   ships (launch type `terminal`) may interact with the human at the console as normal.
- **Never `nohup` the web server.** Use `starfleetctl web restart` to start or restart the fleet
   web console as a daemon. It kills any existing instance, waits for the port to free, then
   daemonizes a fresh one. `starfleetctl web autostart` (for cron) only starts if not already
   running. Never use `nohup starfleetctl web …` — it bypasses PID tracking and leaves orphan
   processes.
- **Always respond to bus messages over the bus.** When a `tell` or `ask` arrives in your inbox,
   always send a reply via `starfleetctl agent-bus tell <sender> <reply>` — never only process
   it internally without answering. The sender expects a bus response; silence means the message
   was lost. Ack the message (`agent-bus ack <id>`) after responding.
- **You may commit + push directly on the praetor's staging branch without asking** — lessons,
  config tweaks, dashboard updates, whatever the session produced. Generalizing something onto the
  main branch for all users is a deliberate, separate decision the praetor makes per item.
- **Project knowledge lives in the repo, not in per-user agent memory.** Lessons, CI gotchas,
  failure modes, and workflow quirks go into `.starfleet-ai/agents.d/index.md` or topic docs — version-controlled and
  shared with the whole team. A machine-local agent memory store is private and invisible to
  teammates, so it must **not** hold project facts. Never create a `memory/` directory inside a
  source clone.
- **Turn repeated commands into scripts, then authorize them.** If you find yourself running the
  same multi-step command (especially GitHub/`gh` access), factor it into a generic
  `scripts/<name>` (match the existing style) and add allow rules so it runs without a
  confirmation prompt.
- **Bash cwd persists silently across tool calls.** After `cd`-ing into a nested directory for one
  investigation, every later command keeps running there until you explicitly `cd` back. Always use
  an explicit absolute path or `cd` to the workspace root before commands whose output isn't meant
  to land in a subdirectory.
- **Opening URLs (e.g. PR links) in the praetor's browser** — only on explicit request. Never in
  headless/CI runs. Use `xdg-open` in interactive sessions.
