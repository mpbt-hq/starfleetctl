# dashboard

Project status tracking via topic files.

## Synopsis

```
starfleetctl dashboard <command> [args...]
starfleetctl dashboard topic <command> [args...]
```

## How It Works

The dashboard is a set of per-topic files under `.starfleet-ai/dashboard/topics/`. Each topic tracks one initiative. The topic files are the source of truth and are read live; there is no central `DASHBOARD.md` document.

## Accessing the dashboard (read vs write)

The dashboard is **always** accessed via `starfleetctl dashboard …` — never by editing files under `.starfleet-ai/dashboard/topics/` directly.

**Read**
- `starfleetctl dashboard show` — render the whole dashboard (Active + Parked) from the topic files.
- `starfleetctl dashboard topic list [--json]` — list topics (use `--json` for filtering).
- `starfleetctl dashboard topic show <slug>` — print one topic.

**Write**
- `starfleetctl dashboard topic write <slug> <file|->` — replace one topic's content.
- `starfleetctl dashboard topic commit <slug> -m "<msg>"` — commit + push that one topic.
- `starfleetctl dashboard topic new <slug> --title "<t>" [--parked]` — scaffold a new topic.

(There is no `dashboard list`, no `dashboard show <file>`, `write`, `pull`, `commit` or `reindex` any more — those operated on the removed `DASHBOARD.md` file.)

## ⚠️ Common Mistake

**WRONG:** `starfleetctl dashboard list`  
**CORRECT:** `starfleetctl dashboard topic list`

The `dashboard` subcommand itself has no `list` — all topic operations are under `dashboard topic`.

## Commands

### Render

```sh
# Print the whole dashboard (Active + Parked), rendered live from the topic files
starfleetctl dashboard show
```

### Topic Files

```sh
# List all topics
starfleetctl dashboard topic list
starfleetctl dashboard topic list --json

# Filter by category
starfleetctl dashboard topic list --category active
starfleetctl dashboard topic list --category parked

# Filter by status (substring match, case-insensitive)
starfleetctl dashboard topic list --status done
starfleetctl dashboard topic list --status open

# Combine filters
starfleetctl dashboard topic list --category active --status done
starfleetctl dashboard topic list --json --category active --status open

# Show a topic's content
starfleetctl dashboard topic show my-feature

# Write a topic file
starfleetctl dashboard topic write my-feature new-content.md
cat content.md | starfleetctl dashboard topic write my-feature -

# Create a new topic
starfleetctl dashboard topic new my-feature --title "My Feature" --status "planned"
starfleetctl dashboard topic new parked-idea --title "Idea" --status "noted" --parked

# Commit a single topic (concurrent-safe)
starfleetctl dashboard topic commit my-feature -m "progress: build passes"
```

## Topic File Format

Each topic file has YAML frontmatter:

```markdown
---
slug: my-feature
title: "My Feature"
category: active
status: "in progress"
since: 2026-07-15
---

Description of what this topic tracks...
```

### Categories

- `active` — currently being worked on
- `parked` — noted but not started (uses `notedBy` instead of `status`)

## Concurrency

`topic commit` locks only the one topic file's `git add`, so two ships committing different topics never collide.
