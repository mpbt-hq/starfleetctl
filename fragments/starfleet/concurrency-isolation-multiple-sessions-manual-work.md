---
title: "Concurrency / isolation (multiple sessions + manual work)"
order: 160
owner: "starfleetctl"
---

## Concurrency / isolation (multiple sessions + manual work)

**The unit of isolation is the clone (working tree + index), not the repo.** All safety rules
follow from that.

- **Safe by construction:** different working trees cannot clobber each other's files/index/HEAD.
  They share only the GitHub remote. **Parallelize across independent workspaces freely.**
- **The hazard:** two actors (two sessions, or a session + manual edits) mutating the **same**
  clone at once. Git has no native working-tree lock — concurrent `checkout` / `add` / `commit`
  / `rebase` will silently corrupt each other's state.

### Two agents on the same PR — claim it first (`starfleetctl pr-claim`)

Separate clones isolate *files*, but every clone pushes to the **same GitHub PR branch**. So two
agents repairing the **same PR** in two clones is still a conflict: their force-pushes clobber each
other (last writer wins, the other's fix is silently lost). Clone isolation does **not** cover this;
PR-branch ownership does.

**Protocol — before you start mutating a PR (repair, amend, backport-to-a-PR):**

```bash
export AGENT_ID=<short-unique-name>      # e.g. repair-3132
starfleetctl pr-claim <pr#> "what you're doing"
# ... work ...
starfleetctl pr-claim --release <pr#>    # when done
```

- `starfleetctl pr-claim --list` is the shared work log.
- Advisory: it only guards actors that also call it.
- Claims are keyed by PR#, serialized via `flock`, stored gitignored, and go stale after a
  configurable TTL (default 1h). Use `--steal <pr#>` to take over a stale claim from a dead agent.
- Pushing to **distinct** branches never conflicts, so across *different* PRs you still
  parallelize freely.

### Central control plane — one agent monitors/steers the others (`starfleetctl agent-bus`)

`pr-claim` coordinates *ownership of one PR branch*; `agent-bus` is the broader **control plane**
so a flagship agent — or a future dashboard / voice UI / MCP bus — can see what every independent
session is doing and steer it. All parties read/write the same gitignored files, serialized via
`flock`, so it works across **totally independent** sessions, not just spawned subagents.

- **Worker session** (one per task): set a unique `$AGENT_ID`, then
  `starfleetctl agent-bus status <state> ["note"]` to report a heartbeat.
  Check `starfleetctl agent-bus inbox` for directives, and `starfleetctl agent-bus ack <id>` when
  handled. `starfleetctl agent-bus clear` on exit.
- **Control agent** (the flagship): `starfleetctl agent-bus board` is the whole-fleet view.
  Steer with `starfleetctl agent-bus tell <agent> <text...>` (one agent) or
  `starfleetctl agent-bus broadcast <text...>` (all). For payloads larger than
  ~100 KB (logs, diffs, long briefings) pass the body via stdin to avoid the
  OS `ARG_MAX` limit on command-line arguments:
  `starfleetctl agent-bus tell <agent> --stdin < brief.txt` (or
  `… broadcast --stdin`).

**Heartbeats are auto-reported** via session hooks or wrappers — details are workspace-specific.

**Auto-surfacing directives via Claude Code's `Monitor` tool:** `agent-bus-monitor-loop` (a script
shipped with starfleetctl) polls the inbox and lands new directives as in-context events inside a
running Claude Code session. The assistant still reasons about each directive and goes through the
normal tool-permission flow.

**Automated ships and the two-tier permission model:**

Ships spawned by `starfleetctl fleet-autoscale` are a distinct, lower **tier**:

- **Upper tier** — interactively launched sessions: ordinary permission prompts, because a human
  is there to answer them.
- **Lower tier** — auto-spawned workers: launched with `--permission-mode dontAsk` (Claude Code),
  so anything outside the pre-authorized allowlist is rejected outright instead of blocking on a
  confirmation nobody is watching. A worker that hits a blocked action must report it to its
  supervisor via `agent-bus tell` and continue other queued work.

**Preferred: agents work in their own dedicated clones.** Create an agent-owned clone:

```bash
starfleetctl mk-agent-clone <branch> [name]
# -> _WORK_/agent/<name>/<repo>  (gitignored)
```

- **Full isolation, cheap.** Object store is borrowed from a reference clone via git **alternates**
  (`--reference-if-able`), so the new `.git` is a few hundred KB.
- **This is why a clone, not a worktree:** some workflows rewrite branch history. A worktree shares
  the ref store, so that rewrite would mutate the user's branch. A separate clone shares only
  objects, so it can't.
- **Multiple parallel agents:** give each its own `name`.

**Object-sharing caveat:** the agent clone borrows objects from the reference clone, so don't run
`git gc --prune` / aggressive `git repack` in the reference while agent clones reference it.

**Fallbacks when not using a dedicated clone:**

- **Per-task git worktrees** — own working dir + index + HEAD, shares the object store.
  **`starfleetctl worktree`** wraps the lifecycle.
- **`starfleetctl with-clone-lock`** — advisory per-working-tree `flock`; wrap mutating commands so
  cooperating actors serialize. Keyed to the per-worktree git dir. Auto-releases on process exit.

**Rule of thumb:** agents → own clone; parallelize freely across tasks; within a task use a
per-agent clone name (or worktree / `with-clone-lock`).
