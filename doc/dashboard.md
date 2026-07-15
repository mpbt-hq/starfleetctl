# dashboard

Project status tracking via theme files.

## Synopsis

```
starfleetctl dashboard <command> [args...]
starfleetctl dashboard theme <command> [args...]
```

## How It Works

The dashboard is a thin index (`DASHBOARD.md`) that links to per-theme files under `dashboard/themes/`. Each theme tracks one initiative or topic. The index is regenerated from theme file frontmatter — two agents racing a `reindex` converge to the same output.

## Commands

### Index Management

```sh
# Show current dashboard (pulls from remote first)
starfleetctl dashboard show

# Pull latest from remote
starfleetctl dashboard pull

# Regenerate index from theme files
starfleetctl dashboard reindex

# Write new content (no commit)
echo "new content" | starfleetctl dashboard write -

# Commit and push
starfleetctl dashboard commit -m "update: added new theme"
starfleetctl dashboard commit -m "update" --no-push
```

### Theme Files

```sh
# List all themes
starfleetctl dashboard theme list
starfleetctl dashboard theme list --json

# Show a theme's content
starfleetctl dashboard theme show my-feature

# Write a theme file
starfleetctl dashboard theme write my-feature new-content.md
cat content.md | starfleetctl dashboard theme write my-feature -

# Create a new theme
starfleetctl dashboard theme new my-feature --title "My Feature" --status "planned"
starfleetctl dashboard theme new parked-idea --title "Idea" --status "noted" --parked

# Commit a single theme (concurrent-safe)
starfleetctl dashboard theme commit my-feature -m "progress: build passes"
```

## Theme File Format

Each theme file has YAML frontmatter:

```markdown
---
slug: my-feature
title: "My Feature"
category: active
status: "in progress"
since: 2026-07-15
---

Description of what this theme tracks...
```

### Categories

- `active` — currently being worked on
- `parked` — noted but not started (uses `notedBy` instead of `status`)

## Concurrency

`theme commit` locks only the one theme file's `git add`, so two agents committing different themes never collide. The full `dashboard commit` locks the entire workspace commit path.
