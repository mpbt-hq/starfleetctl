---
name: task-capture
---

# starfleetctl task capture — full reference

## Synopsis

```
starfleetctl task capture --title "<t>" [options]
```

## Description

Captures a task into the workspace dashboard (as a `dashboard/topics/<slug>.md`
topic entry, appearing under "Active Topics" in `DASHBOARD.md`) and optionally
commissions a free ship to work it. **Never executes the task itself** — this is
pure commandeering.

The dashboard topic is created via the sanctioned `dashboard` package calls only;
no raw filesystem access to topic files.

## Options

| Flag | Required | Description |
|------|----------|-------------|
| `--title "<t>"` | yes | Task title. Used for slug derivation and display. |
| `--desc "<text>"` | no | Free-form task description / acceptance criteria. |
| `--slug "<slug>"` | no | Override the auto-derived dashboard topic slug. |
| `--assign [<ship>]` | no | Commission a ship. Without arg: pick first idle, non-stale ship. With arg: commission that specific ship. |
| `--no-push` | no | Stage + commit locally but do not push to origin. |
| `-h`, `--help` | no | Show help. |

## Exit codes

| Code | Meaning |
|------|---------|
| 0 | Task captured (and assigned, if requested). |
| 2 | Bad arguments / missing `--title`. |
| 3 | Slug already exists (collision — pick a different title/slug). |
| 4 | No free ship available for `--assign` (without explicit ship name). |

## Output

On success, prints:

```
task-captured: slug=<slug> status=<status> assigned-to=<ship|—>
commissioned-ship: <ship>          # only if a ship was commissioned
```

## Slug derivation

If `--slug` is not given, the title is lowercased, non-alphanumeric characters
collaps to dashes, and `task-` is prepended. Example: "Fix NVIDIA build" →
`task-fix-nvidia-build`.

## Examples

```sh
# Capture a task, no assignment
starfleetctl task capture --title "Fix NVIDIA build" \
    --desc "Build fails with undefined symbols on 535.xx"

# Capture and auto-assign to first free ship
starfleetctl task capture --title "Review PR #42" --assign

# Capture and assign to specific ship
starfleetctl task capture --title "Backport fix" \
    --desc "Cherry-pick commit abc123 to release/25.2" \
    --assign Voyager

# Capture locally only (no push)
starfleetctl task capture --title "WIP: refactor dashboard" --no-push
```

## How it works (internally)

1. `dashboard.New(root)` — initialize dashboard handle.
2. `dashboard.DoTopicNew(slug, title, "open", "")` — reserve slug (collision guard).
3. If `--assign`: `agentbus.BoardEntries()` → pick first `idle && !stale` ship.
4. Build topic file content (frontmatter + body) with `kind: task`, `created-by`,
   `created`, `assigned-to`, `status`.
5. `dashboard.DoTopicWrite(slug, tmpFile)` — write via sanctioned path.
6. `dashboard.DoTopicCommit(slug, "task: "+title, push)` — commit the topic.
7. `dashboard.DoReindex()` + `dashboard.DoCommit("reindex: add task "+slug, push)`
   — refresh DASHBOARD.md index.
8. If ship commissioned: `agentbus.Tell(ship, german directive)` — notify via
   agent-bus.

## Integration with agent-bus

When a ship is commissioned, it receives a German-language directive via
`agent-bus tell`:

```
Neue Aufgabe für dich erfasst: <title> (Dashboard-Topic `<slug>`). Bitte dort
Details lesen und abarbeiten. Status danach via agent-bus melden.
```

The receiving ship should:
1. Read the dashboard topic via `starfleetctl dashboard topic show <slug>`.
2. Execute the task.
3. Report completion via `agent-bus tell` to the praetor/sender.
