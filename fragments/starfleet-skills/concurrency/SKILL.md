---
name: concurrency
description: "Concurrency and isolation — multiple sessions, agent clones, PR-claiming, worktrees, and the two-tier permission model. Use when setting up parallel agent work, resolving concurrency conflicts, or understanding the isolation model for fleet sessions."
---

# Concurrency / isolation (multiple sessions + manual work)

Ships must be very careful on not working in the same git working trees at the same time.
Therefore they need to use separate clones, or at least separate working trees. Ships need
to coordinate on who is using which worktree / clone. A good option is to use a separate
subdir per ship, where the worktrees / clones are hosted.

When doing work, they shall output their currently used work tree on the console as well
as their current ship status (via agent bus).

Also when working on PRs, arbitration on who's currently working on it (pr claim -- see below)

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
