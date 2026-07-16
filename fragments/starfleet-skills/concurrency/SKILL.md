---
name: concurrency
description: "Concurrency and isolation — multiple sessions, agent clones, PR-claiming, worktrees, and the two-tier permission model. Use when setting up parallel agent work, resolving concurrency conflicts, or understanding the isolation model for fleet sessions."
---

# Concurrency / isolation (multiple sessions + manual work)

**The unit of isolation is the clone (working tree + index), not the repo.** All safety rules
follow from that.

Full reference: **`reference.md`** in this skill's directory. This skill is the actionable checklist.

## Key rules

- **Different working trees cannot clobber each other.** Parallelize across independent workspaces freely.
- **The hazard:** two actors mutating the **same** clone at once — git has no native working-tree lock.
- **PR-branch ownership:** separate clones isolate files, but every clone pushes to the **same GitHub PR branch**. Use `starfleetctl github pr claim` before mutating a PR.

## Quick setup

```bash
# Agent-owned clone (preferred)
starfleetctl github pr mk-agent-clone <branch> [name]

# PR claiming
starfleetctl github pr claim <pr#> "what you're doing"
# ... work ...
starfleetctl github pr claim --release <pr#>

# Per-task worktree
.starfleet-ai/bin/starfleetctl worktree add <repo> [name]

# Serialize mutating work
starfleetctl with-clone-lock <cmd...>
```

## Two-tier permission model

- **Upper tier** (interactive): ordinary permission prompts
- **Lower tier** (auto-spawned workers): `--permission-mode dontAsk`, blocked actions reported to supervisor
