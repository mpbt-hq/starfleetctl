# dashboard

Project status tracking via topic files.

## Synopsis

```
starfleetctl dashboard <command> [args...]
starfleetctl dashboard topic <command> [args...]
```

## How It Works

The dashboard is a thin index (`.starfleet-ai/var/DASHBOARD.md`) that links to per-topic files under `.starfleet-ai/var/dashboard/topics/`. Each topic tracks one initiative or topic. The index is regenerated from topic file frontmatter — two agents racing a `reindex` converge to the same output.

## Commands

### Index Management

```sh
# Show current dashboard (pulls from remote first)
starfleetctl dashboard show

# Pull latest from remote
starfleetctl dashboard pull

# Regenerate index from topic files
starfleetctl dashboard reindex

# Write new content (no commit)
echo "new content" | starfleetctl dashboard write -

# Commit and push
starfleetctl dashboard commit -m "update: added new topic"
starfleetctl dashboard commit -m "update" --no-push
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

`topic commit` locks only the one topic file's `git add`, so two agents committing different topics never collide. The full `dashboard commit` locks the entire workspace commit path.
