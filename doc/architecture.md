# Architecture

How starfleetctl coordinates multiple AI-agent sessions.

## The Problem

When several AI agents (Claude Code, opencode, etc.) work on the same repository simultaneously, they need:

1. To know who else is active and what they're doing
2. To send tasks and questions to each other
3. To avoid editing the same files or branches at the same time
4. To safely commit changes without race conditions

## The Solution

starfleetctl provides a file-based coordination layer. All state lives in `_WORK_/` under your workspace root — plain text files that any process can read, no database or server required.

```
workspace/
├── _WORK/
│   ├── agent-bus/          # Status board + message bus
│   │   ├── .lock           # Exclusive flock for atomic operations
│   │   ├── .seq            # Message ID counter
│   │   ├── status/         # Per-agent heartbeat files
│   │   │   ├── Enterprise.tsv
│   │   │   └── Voyager.tsv
│   │   ├── msgs/           # Directive files
│   │   │   ├── m0001.tsv
│   │   │   └── m0002.tsv
│   │   ├── acks/           # Acknowledgment markers
│   │   ├── attachments/    # Large payload storage
│   │   └── events.log      # Audit trail
│   └── agent-claims/       # PR-branch locks
│       ├── pr-3162.tsv
│       └── pr-3163.tsv
├── DASHBOARD.md            # Project status index
└── dashboard/topics/       # Per-topic status files
```

## Core Concepts

### Ships

Each agent session is identified by a "ship name" (e.g., `Enterprise`, `Voyager`). The name is set via the `STARFLEET_SHIP_ID` environment variable. If unset, starfleetctl falls back to `user@hostname`.

### Agent Bus

The agent bus is a publish-subscribe system built on files:

1. **Status heartbeats** — Each ship writes a TSV file with its current state (`working`, `idle`, `blocked`, etc.). Other ships read these to see who's active.

2. **Directives** — Messages are written as individual TSV files. Each has a unique ID (`m0001`, `m0002`, ...), a sender, a target (or `all` for broadcasts), and a text body.

3. **Acknowledgments** — When a ship processes a directive, it creates an empty ack file. Fully-acked old directives are pruned automatically.

### File Locking

All mutations go through `flock(2)` on `_WORK_/agent-bus/.lock`. This ensures:

- Multiple processes can read concurrently
- Only one process writes at a time
- Go and bash implementations interoperate safely (same lock file)

### PR Claims

Each PR can be "claimed" by one agent at a time. Claims are advisory (cooperative) — they prevent two agents from pushing to the same branch, but don't block git operations at the filesystem level.

## Data Flow

### Agent Startup

```
1. Agent session starts
2. ship-names assign → gets "Voyager"
3. agent-bus status working "starting up"
4. Dashboard shows: Voyager | working | 0s
```

### Cross-Agent Communication

```
Enterprise                          Voyager
    │                                   │
    │  agent-bus tell Voyager "..."     │
    │  ─────────────────────────────►   │
    │                                   │
    │                           agent-bus inbox
    │                           shows m0042
    │                                   │
    │                           agent-bus ack m0042
    │                           agent-bus tell Enterprise "done"
    │  ◄─────────────────────────────   │
    │                                   │
```

### Safe Commits

```
1. ws-commit -m "fix: ..." src/foo.c
2. Acquires flock on _WORK_/agent-bus/.lock
3. git add src/foo.c
4. git commit -m "fix: ..."
5. git push
6. Releases lock
```

### PR Branch Locking

```
1. pr-claim 3162 "fixing build"
2. Creates _WORK_/agent-claims/pr-3162.tsv
3. Other agents see claim, avoid branch
4. pr-claim --release 3162
5. Claim file removed
```

## Interoperability

The Go implementation reads and writes the exact same file formats as the original bash scripts (`scripts/agent-bus`, `scripts/pr-claim`). A workspace can have:

- Some agents using the Go binary
- Others using the bash scripts
- Both operating on the same `_WORK_/` files

This allows gradual migration without breaking existing setups.

## Workspace Discovery

starfleetctl needs to find the workspace root. It checks:

1. `$MPBT_WORKSPACE_ROOT` if set
2. Walk up from `cwd` looking for `.starfleet-ai/` + `scripts/`
3. Stop at git repo boundaries (won't escape into parent repos)

GitHub-interaction commands (`pr-view`, `pr-ci`, etc.) don't need a workspace root — they work from any directory.
