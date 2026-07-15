---
name: task-capture
description: "Capture a fleet task into the dashboard (and optionally commission a free ship) purely by commandeering — never executing the task, never touching dashboard files directly. Also re-assign / unassign / update an existing task's status via the same sanctioned path. Use when asked to 'record a task', 'capture an aufgabe', 'track a to-do for the fleet', 'assign a task to a free ship', or 'reassign / unassign / update a task'."
---

# task-capture — pure commandeering of fleet tasks

Record a task for the fleet in the workspace dashboard, and optionally hand it
to a free ship. **This skill only commands — it never does the work.** Run it
and report back; do not start implementing the task yourself.

Designed to be driven by a small/fast model: the real logic lives in
`starfleetctl task capture`, so the model only issues one deterministic command
and forwards the printed summary. No judgement, no file editing.

## Hard rules (non-negotiable)

1. **Never execute the task.** Capturing is the whole job. The assigned ship
   does the work later.
2. **No direct file access.** Never `Read`/`Edit`/`Write` `DASHBOARD.md` or
   `dashboard/topics/*.md`. All access goes through `starfleetctl task …` or
   `starfleetctl dashboard topic …` (the helper already does this).
3. **Messages to other ships / the praetor go in German** (agent-bus
   `tell`/`broadcast`). Code, commits, and doc files stay English.
4. **Report back** to the sender (e.g. Enterprise) via `agent-bus tell` with
   the printed summary after running.

## The one-liner (preferred — small-model friendly)

```sh
starfleetctl task capture --title "<short task title>" \
    [--desc "<what needs doing / acceptance criteria>"] \
    [--slug "<override-slug>"] \
    [--assign [<ship>]] \
    [--no-push]
```

- `--assign` with **no ship name** → picks the first `idle`, non-stale ship
  from the agent-bus board and commissions it.
- `--assign <ship>` → commission that specific ship.
- Without `--assign` → task is recorded as `open` (open), no ship yet.
- `--no-push` → stage + commit locally but don't push to origin.

The command prints `task-captured: slug=… status=… assigned-to=…` — forward
that to the sender.

## What the command does (so you can explain it)

1. Reserves a dashboard topic slug (`task-<derived>` unless `--slug` given)
   via `starfleetctl dashboard topic new` — refuses if the slug already exists
   (collision guard).
2. Optionally selects a free ship (idle + not stale) before writing.
3. Writes the task as a `category: active` topic (shows in DASHBOARD.md's
   "Active Topics") with task-specific frontmatter: `kind: task`,
   `created-by`, `created`, `assigned-to`, plus the description body.
4. `starfleetctl dashboard topic commit` — commits + pushes just that one file.
5. `starfleetctl dashboard reindex` + `dashboard commit` — refreshes the index.
6. If a ship was chosen: `starfleetctl agent-bus tell <ship>` with a German
   directive pointing at the dashboard topic.

## Assigning, unassigning, and updating existing tasks

To re-assign, unassign, or change the status of a task that already exists as a
dashboard topic, use the dedicated `task` subcommands — **no hand-editing of
`dashboard/topics/*.md` required**:

```sh
# Re-assign an existing task (no <ship> -> first idle,non-stale ship from the
# board; with <ship> -> that ship). Commits + reindexes automatically and
# commissions the ship with a German directive:
starfleetctl task assign <slug> [<ship>] [--no-push]

# Clear an assignment (status -> open, assigned-to -> —):
starfleetctl task unassign <slug> [--no-push]

# Set a task's status field (e.g. open, assigned, done):
starfleetctl task status <slug> <status> [--no-push]
```

- All three go through the sanctioned dashboard path (no raw file access) and
  refresh `DASHBOARD.md`'s "Active Topics"/"Parked" index automatically — no
  separate `reindex` step is needed.
- `task assign` with **no `<ship>`** behaves like `capture --assign`: it picks
  the first `idle`, non-stale ship from the agent-bus board and commissions it.
  With `<ship>` it commissions that specific ship (the German directive reads as
  a fresh assignment vs. a reassignment automatically).
- The commands print `task-assigned: …`, `task-unassigned: …`,
  `task-status: …` respectively — forward that to the sender.

These commands are the sanctioned replacement for any direct topic-file
editing when managing task assignment.

## Manual fallback (only if `starfleetctl task capture` is unavailable)

Use the same `starfleetctl` subcommands directly — never the files:

```sh
SLUG="task-my-task"
starfleetctl dashboard topic new "$SLUG" --title "<title>" --status "open"
# build the topic content (frontmatter + body) in a scratch file under _WORK_/.tmp
starfleetctl dashboard topic write "$SLUG" _WORK_/.tmp/task.md
starfleetctl dashboard topic commit "$SLUG" -m "task: <title>"
starfleetctl dashboard reindex
starfleetctl dashboard commit -m "reindex: add task $SLUG"
# optional commission:
SHIP=$(starfleetctl agent-bus board --json | <pick idle,non-stale>)
starfleetctl agent-bus tell "$SHIP" "Neue Aufgabe für dich erfasst: <title> (Dashboard-Topic \`$SLUG\`). Bitte dort Details lesen und abarbeiten."
```
