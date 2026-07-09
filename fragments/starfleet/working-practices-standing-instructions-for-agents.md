---
title: "Working practices (standing instructions for agents)"
order: 20
owner: "starfleetctl"
---

## Working practices (standing instructions for agents)

These apply to **every** session — they keep knowledge and tooling from decaying as sessions are
cleared:

- **Language policy: converse with the praetor and other ships in the project's primary spoken
  language, everything else in English.** Any `agent-bus tell`/`broadcast` text — whether directed
  at the praetor or at another ship — is in the project's primary spoken language (e.g. German for
  the XLibre fleet). Anything that becomes part of the project's durable artifacts stays English:
  code, code comments, commit messages, PR titles/descriptions/review comments, other GitHub
  interactions, and doc files. Applies fleet-wide, not just to the flagship session.
- **Record lessons learned as you go.** Append durable, non-obvious findings to the relevant
  section of `AGENTS.md` (or a topic doc) **within the session**, not at the end. Session context
  is wiped on session end; only what's written survives.
- **Keep the dashboard current** — it's the cross-session "what's in flight / what got parked"
  index. When you start, pause, or finish a theme (an initiative spanning more than one PR, or a
  decision still pending), update its entry **in the same session**. Use the CLI (`starfleetctl
  dashboard` subcommands) for every read and write — never edit the index file directly.
- **Notice something worth a look while doing unrelated work → park it immediately.** A suspicious
  code path, a possible follow-up cleanup, an untriaged idea — add a dashboard Parkplatz entry
  right away rather than just mentioning it in the response and moving on.
- **You may commit + push directly on the praetor's staging branch without asking** — lessons,
  config tweaks, dashboard updates, whatever the session produced. Generalizing something onto the
  main branch for all users is a deliberate, separate decision the praetor makes per item.
- **Project knowledge lives in the repo, not in per-user agent memory.** Lessons, CI gotchas,
  failure modes, and workflow quirks go into `AGENTS.md` or topic docs — version-controlled and
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
