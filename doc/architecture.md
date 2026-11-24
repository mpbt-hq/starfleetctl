# Architecture

How starfleetctl coordinates multiple AI-agent sessions.

## The Problem

When several AI agents (Claude Code, opencode, etc.) work on the same repository simultaneously, they need:

1. To know who else is active and what they're doing
2. To send tasks and questions to each other
3. To avoid editing the same files or branches at the same time
4. To safely commit changes without race conditions

## The Solution

starfleetctl provides a file-based coordination layer. All state lives in `.starfleet-ai/var/...` under your workspace root — plain text files that any process can read, no database or server required.

```
workspace/
├── .starfleet-ai/
│   ├── var/
│   │   ├── comms/
│   │   │   ├── .lock             # shared flock domain
│   │   │   ├── .seq              # message ID counter
│   │   │   ├── status/           # per-ship status/<ship>.json
│   │   │   ├── msgs/             # per-recipient message JSON
│   │   │   │   └── <ship>/
│   │   │   │       ├── unseen/   # pending messages
│   │   │   │       └── seen/     # acknowledged messages
│   │   │   ├── attachments/     # large payload storage
│   │   │   └── events.log       # audit trail
│   │   ├── agent-claims/         # advisory PR-branch claims
│   │   └── dashboard/topics/    # project task/topic files
│   └── conf/                    # workspace configuration
└── sop.d/                       # user-maintained SOP fragments
```

## Core Concepts

### Ships

Each agent session is identified by a ship name (e.g., `Enterprise`, `Voyager`) in `STARFLEET_SHIP_ID`. If unset, the Comms bus uses the process-local fallback `anon-<pid>`; launchers normally set a stable ship identity explicitly.

### Comms Bus

The comms bus is a publish-subscribe system built on files:

1. **Status heartbeats** — Each ship writes a JSON record at `status/<ship>.json`, containing its state and liveness metadata.

2. **Directives** — Messages are JSON files in each recipient's `msgs/<ship>/unseen/` directory. Broadcasts are materialized as per-recipient copies.

3. **Acknowledgments** — Acknowledging a directive moves that recipient's copy from `unseen/` to `seen/`. Old fully-acknowledged directives can then be pruned. Legacy flat message files remain readable during migration.

### File Locking

Comms bus mutations go through `flock(2)` on `.starfleet-ai/var/comms/.lock`. This ensures:

- Multiple processes can read concurrently
- Only one process writes at a time
- Go and bash implementations interoperate safely (same lock file)

### PR Claims

Each PR can be "claimed" by one ship at a time. Claims are advisory (cooperative) — they prevent two ships from pushing to the same branch, but don't block git operations at the filesystem level.

## Data Flow

### Agent Startup

```
1. Agent session starts
2. ship-names assign → gets "Voyager"
3. comms status working "starting up"
4. Dashboard shows: Voyager | working | 0s
```

### Cross-Agent Communication

```
Enterprise                          Voyager
    │                                   │
    │  comms tell Voyager "..."     │
    │  ─────────────────────────────►   │
    │                                   │
    │                           comms inbox
    │                           shows m0042
    │                                   │
    │                           comms ack m0042
    │                           comms tell Enterprise "done"
    │  ◄─────────────────────────────   │
    │                                   │
```

### Safe Commits

```
1. ws-commit -m "fix: ..." src/foo.c
2. Acquires flock on .starfleet-ai/var/comms/.lock
3. git add src/foo.c
4. git commit -m "fix: ..."
5. git push
6. Releases lock
```

### PR Branch Locking

```
1. pr-claim 3162 "fixing build"
2. Creates .starfleet-ai/var/agent-claims/pr-3162.tsv
3. Other ships see claim, avoid branch
4. pr-claim --release 3162
5. Claim file removed
```

## Interoperability

The Go implementation reads and writes the exact same file formats as the original bash scripts (`scripts/comms`, `scripts/pr-claim`). A workspace can have:

- Some ships using the Go binary
- Others using the bash scripts
- Both operating on the same `.starfleet-ai/var/...` files

This allows gradual migration without breaking existing setups.

## Workspace Discovery

starfleetctl needs to find the workspace root. It checks:

1. `$MPBT_WORKSPACE_ROOT` if set
2. Walk up from `cwd` looking for `.starfleet-ai/` + `scripts/`
3. Stop at git repo boundaries (won't escape into parent repos)

GitHub-interaction commands (`pr-view`, `pr-ci`, etc.) don't need a workspace root — they work from any directory.
