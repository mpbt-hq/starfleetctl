---
slug: starfleet/starfleetctl
title: "starfleetctl — fleet-management CLI (auto-installed by `agents install-self`, do not hand-edit)"
order: 900
owner: "starfleetctl"
---

# starfleetctl

A Go CLI that consolidates a set of `flock`/race-prone shell scripts used to coordinate many
independent, concurrent AI-agent sessions ("ships") working on the same codebase — status
heartbeats, cross-session directives, PR-branch locking, shared-doc commits, and read/write access
to GitHub PRs — into one binary, one subcommand per script.

It grew out of `mpbt-workspace` (a build orchestrator + fleet-coordination workspace), where the
original bash scripts still live under `scripts/*` and remain the reference implementation for
anything not yet ported here.

## Deployment

starfleetctl is deployed into a workspace via the **genesis → bootstrap** two-phase model:

1. **Phase A (genesis):** One already-built starfleetctl binary writes a minimal
   `starfleet-bootstrap` shell script into a fresh checkout:
   ```
   starfleetctl genesis-init .
   ```
   This also runs `bootstrap --fix` to set up .starfleet-ai/agents.d/index.md, DASHBOARD.md, allowlist entries,
   `_WORK_/` directories, agent fragments, and opencode plugins/scripts.

2. **Phase B (bootstrap):** The committed `starfleet-bootstrap` script clones or pulls the
   starfleetctl source repo, builds it with `go build`, symlinks the binary to
   `.starfleet-ai/bin/starfleetctl`, and re-runs `bootstrap --fix`:
   ```
   ./starfleet-bootstrap
   ```
   Re-run anytime to update starfleetctl and re-apply all workspace configuration.

Everything under `.starfleet-ai/` is gitignored. The `starfleet-bootstrap` script is the only
committed file. starfleetctl also provides `starfleetctl self-install` to clone/build/link
itself without the bootstrap script (useful for updates when the binary is already installed).

## Why a Go rewrite of working bash scripts?

1. **One allowlist entry covers every subcommand.** Tools like Claude Code gate shell commands
   behind a per-command permission allowlist. ~30 separate bash scripts needed ~30 separate
   allowlist entries (`Bash(scripts/foo)` + `Bash(scripts/foo *)` each); a single
   `Bash(scripts/starfleetctl)`/`Bash(scripts/starfleetctl *)` pair covers every subcommand this
   binary has now *and* every one it gains later. The trade-off, accepted deliberately: less
   granular — a bug in one subcommand isn't scoped out from the others by the allowlist.
2. **`encoding/json` + `os/exec` argument arrays eliminate a real class of bash bugs** — quoting
   and word-splitting mistakes, brittle `jq`/`sed`-based JSON parsing, and `set -e` silently
   swallowed inside a pipeline. What this rewrite does *not* eliminate: CI/VM flakiness, `gh` CLI
   quirks (e.g. a broken `gh pr edit` on some repos), and shell-permission-matching gotchas are
   external to the implementation language and need the same handling either way.

This was **not** a big-bang rewrite of every script in the source workspace — only the
highest-value, highest-race-risk ones were ported (see the subcommand table below); single-purpose
scripts with no shared-state risk are still bash-only there.

## Build

Plain Go, stdlib only (no third-party dependencies):

```sh
make build      # -> ./starfleetctl
make test       # go test ./...
make fmt vet    # go fmt / go vet
```

or directly:

```sh
go build -o starfleetctl ./cmd/starfleetctl
```

Inside `mpbt-workspace`, starfleetctl is deployed under `.starfleet-ai/` by the
`starfleet-bootstrap` script (or via `starfleetctl self-install` for updates).

## Usage

```sh
starfleetctl <subcommand> [args…]
```

Inside `mpbt-workspace` (or any fleet workspace), the canonical path is
`.starfleet-ai/bin/starfleetctl`, installed by `starfleet-bootstrap` or `starfleetctl self-install`.
Run it from the workspace root (or any subdirectory — it discovers the root by walking up):

```sh
.starfleet-ai/bin/starfleetctl <subcommand> [args...]
```

Every subcommand that touches the workspace's shared coordination files (the "fleet
coordination" group below) needs to know the workspace root: it's resolved from
`$MPBT_WORKSPACE_ROOT` if set, otherwise by walking up from the current directory looking for an
`.starfleet-ai/agents.d/index.md` next to a `scripts/` directory (the same landmarks a human would look for) — so it
works run from the workspace root or any subdirectory of it. The GitHub-interaction subcommands and
`with-clone-lock` don't need any of that; they work from any `cwd`.

Run `starfleetctl --help` for a top-level command list, or
`starfleetctl <subcommand> --help` for that subcommand's own usage text —
this README summarizes them, the `--help` output is authoritative.

## Subcommand reference

### Fleet coordination

These share on-disk file formats/lock files with their bash-original namesakes (`scripts/agent-bus`
etc. in `mpbt-workspace`), so a Go and a bash invocation against the same workspace interoperate
transparently — one session can run the Go binary while another runs the bash script against the
same `_WORK_/agent-bus/` files without racing or misreading each other's state.

| Subcommand | Purpose |
|---|---|
| `agent-bus <cmd>` | Cross-session status board + directive bus. Worker side: `status <state> ["note"]`, `inbox [--json]`, `ack <id>`, `ask "<q>"`, `clear`. Control side: `board [--json]`, `tell <agent> <text>`, `broadcast <text>`, `reply <qid> <answer>`, `asks`, `msgs [--json]`, `events [N]`, `prune`. `--json` on `board`/`inbox`/`msgs`/`asks` prints a JSON array instead of the human table. `tell`/`broadcast` also accept `--stdin` to read the message body from stdin, bypassing the OS `ARG_MAX` limit on argv (use it for payloads > ~100 KB). Also has `monitor-loop`/`fleet-watch`/`watch` polling loops — **see [Known limitations](#known-limitations-and-parity-notes)**, they are not wired into any production polling harness. |
| `dashboard <cmd>` | Read/write/commit cycle for `mpbt-workspace`'s `DASHBOARD.md` (the thin, regenerated index): `pull`, `show`, `write <file\|->`, `commit -m "<msg>" [--no-push]`, `reindex` (rebuild the index from every `dashboard/topics/*.md` file's frontmatter). `dashboard topic <cmd>` is the per-topic-file counterpart, the only sanctioned way to read/write `dashboard/topics/*.md` (agents must not touch it via `Read`/`Edit`/`Write` directly — see `mpbt-workspace`'s `.starfleet-ai/agents.d/index.md`): `topic list [--json]`, `topic show <slug>`, `topic write <slug> <file\|->`, `topic new <slug> --title "<t>" [--status "<s>"] [--parked]`, `topic commit <slug> -m "<msg>" [--no-push]` (commits+pushes just that one file, correctly `git add`s it whether already tracked or brand new). |
| `pr-claim <cmd>` | Advisory cross-agent PR-branch lock + shared work log, keyed by PR number: `pr-claim <pr#> ["what"]`, `--list [--json]`, `--release <pr#>`, `--release-all`, `--steal <pr#> ["what"]`, `--who <pr#>`. |
| `ws-commit` | `ws-commit -m "<msg>" <path> [<path>...]` (or `-a` for all tracked changes, `--no-push` to skip the push) — commit+push under the shared clone lock, so concurrent sessions don't race the same working tree's index/HEAD. |
| `ship-names <cmd>` | Star-Trek-topicd per-session identity registry: `assign [flagship]`, `release <name>`, `list [--json]`, `gc`, `flagship`, `shell-env` (outputs eval-able shell code to set `STARFLEET_SHIP_ID`, PS1 prefix, and EXIT trap — usage: `eval "$(starfleetctl ship-names shell-env)"`). |
| `with-clone-lock [cmd...]` | Generic "serialize mutating work in this git working tree" primitive everything above is built on — acquires `<gitdir>/mpbt-clone.lock`, then execs the given command (or an interactive shell with none given) with the lock held. Works in *any* git working tree, not just an `mpbt-workspace` checkout. |
| `hook claude monitor-hint` | Claude Code `SessionStart` hook helper: emits `hookSpecificOutput.additionalContext` JSON telling the assistant to unconditionally arm Monitor-tool watchers on its agent-bus inbox (and, for `Enterprise`, fleet-watch too). Wired as a `SessionStart` hook in `.claude/settings.json`. Quiet no-op when `$STARFLEET_SHIP_ID` is unset. |
| `hook claude permission` | Claude Code `PreToolUse` hook helper: reads the tool-invocation JSON from stdin, asks the control agent (`$AGENT_CONTROLLER`) via agent-bus `ask`/`reply` for an allow/deny decision, blocks up to `$AGENT_PERM_TIMEOUT` (default 60s), and emits a `permissionDecision` JSON. Fail-closed (deny on timeout unless `$AGENT_PERM_TIMEOUT_DECISION=ask`). Used by `scripts/agent-permission-hook`. |
| `session attach <id> [--read-only] [--independent]` | Resolve an agent ID / handle / tmux session name / unique substring to a running tmux session and attach the caller's terminal to it (shared rw / read-only / independent grouped window). Replaces `scripts/agent-attach`. |
| `session attach --list` | List running `mpbt-` tmux sessions and the agent-bus board in one call. |
| `session run <release> [--client claude\|opencode\|shell] [--name <id>] [--tier <tier>] [--supervisor <name>] [--permission-mode <mode>] [-- <args>]` | Launch a detached tmux session for an agent/CLI and post the initial heartbeat (replaces `scripts/agent-run`). Pass `--print` to emit the shell-evaluable launch variables (`AGENT_ID`, `SESSION`, `RELEASE_FULL`, `CLIENT`, `INNER_CMD`) instead. |
| `session stop <id\|session>` | Kill a tmux session, clear its agent-bus heartbeat, and release its ship name (replaces `scripts/agent-run --stop`). |
| `session autoscale status [--max <cap>]` | Show current non-stale fleet size, idle count, and configured cap. |
| `session autoscale need <N> --reason "<text>" [--release <rel>] [--client claude\|opencode] [--max <cap>] [--supervisor <name>] [--permission-mode <mode>] [--dry-run]` | On-demand fleet elasticity: spawn up to `<cap>` minus current fleet size, capped at what's needed after subtracting idle ships. Prints decision + audit log; real spawn also posts an agent-bus broadcast (`scripts/fleet-autoscale` delegates here). |

### GitHub interaction — read-only

Stateless wrappers around the `gh` CLI (which owns auth/config) — parsing/formatting is done
natively in Go instead of `jq`/`grep`/`sed`. All default to the repo in the current directory; override
with the `REPO` environment variable.

| Subcommand | Purpose |
|---|---|
| `pr-view <pr#> [json-fields]` | `gh pr view --json <fields>` (default fields: `number,title,state`). |
| `pr-ci <pr#\|URL> [--json]` | CI status classified **by conclusion, not raw count** — the underlying CI matrix is fail-fast, so one real `FAILURE` cancels every still-running sibling; a big "N failing" number is usually mostly collateral `CANCELLED` jobs. Prints pass/fail/cancelled/pending/skip buckets, the actual failures, a verdict line, and a known-CI-flake hint. `--json` prints the raw `statusCheckRollup`. |
| `show-branch-file <ref> <path> [symbol]` | Print a repo file (or, with `[symbol]`, just the region after a literal-substring match, `grep -A`-style with multi-hit/merged-context semantics) at any ref via the GitHub contents API — no local clone needed. Auto-retries with a leading path segment toggled, for repos that reorganized their directory layout between branches. |
| `backport-applies <master-path> <grep-ERE> [release ...]` | Run an extended-regex marker search across several release branches at once (built on `show-branch-file`) — e.g. classify each branch as vulnerable / already-fixed / not-applicable in one call. Defaults to release lines `25.2 25.1 25.0`. |
| `show-pr-conflict` | List all open PRs whose `mergeable` status is `CONFLICTING`. |

### GitHub interaction — mutating

Same stateless-wrapper approach as above, but these change something (a PR body/label/comment, a
branch, a remote). **Not enabled as the default/preferred path anywhere yet** — they exist for
parity verification; each was checked against its bash original on live or scratch-repo data (see
each file's own doc comment for exactly how), but promoting any of them to "preferred" is a
separate decision gated on review, same as the read-only set was before it got there.

| Subcommand | Purpose |
|---|---|
| `pr-comment <pr#> <body-file> [--bot-review]` | Post a PR comment; `--bot-review` prepends a fixed disclosure banner naming the `$STARFLEET_SHIP_ID` that posted it. |
| `pr-label <pr#> add\|remove <label...>` / `pr-label <pr#> set-review passed\|changes-requested` | Add/remove labels via the REST API (works around a broken `gh pr edit` on some repos); `set-review` swaps two mutually-exclusive review-outcome labels atomically. |
| `pr-request-reviewers <pr#> <login> [login...]` | Request reviewers via the REST API. |
| `pr-set-body <pr#> <body-file>` | Replace a PR's body via the REST API. |
| `pr-append-body <pr#> <text-file>` | Fetch a PR's current body, append the given text, write it back. |
| `pr-checkout <pr#> [agent-name]` | Set up an isolated local clone checked out to a PR's head branch — handles both same-repo and cross-fork PRs (wires a dedicated `fork` remote when needed) — and prints the clone directory on stdout. |
| `pr-amend-push <clone-dir> [files...]` | Fold local edits into a PR's existing commit (`--amend --no-edit`, keeping the original message/trailers) and force-with-lease push it back. |
| `backport-commit <release> <commit-ish\|PR#> [agent-name]` | One-shot backport: refresh/create an isolated agent clone for the target release, cherry-pick the given commit (falling back to a path-remapped apply if the source tree reorganized between branches), then hand off to `xx-make-pr`. |
| `xx-make-pr [options] <commit> [<commit>...]` | Submit one or more commits from the current branch as a PR against a configured upstream (via `git config`'s `make-pr.*` keys in the working directory), then mark only the incubator's copies of those commits with the resulting PR number (never the pushed/merged PR branch) and rebase the source branch. `--branch <name>` sets an explicit PR branch name. |

## Known limitations and parity notes

- **`agent-bus monitor-loop`/`fleet-watch` are known broken specifically when run under Claude
  Code's `Monitor` tool.** Both correctly detect a backlog match (a message that already exists
  when the process starts) but reproducibly fail to notice one that arrives while already running,
  *only* when spawned that way — the same binary works correctly backgrounded via plain `&`, and
  three independent from-scratch minimal Go reproductions of the same shape (a bare sleep-loop, a
  directory-poll loop, and that loop plus a held file handle) all worked fine under `Monitor` too.
  Directory-cache staleness, held-fd interference, and workspace-root resolution were all
  specifically ruled out; the actual cause is not understood. Until it is, the bash originals
  (`scripts/agent-bus-monitor-loop`, `scripts/agent-bus-fleet-watch`) remain the only
  `Monitor`-tool-safe implementation. `agent-bus watch` (a `setsid`-detached background daemon, a
  different execution model entirely) was not tested against this failure mode.
- **`backport-commit`'s path-remap fallback uses a literal string replace, not a regex, unlike the
  bash original.** The bash script's directory-reorg remap runs the old/new path pair through
  `sed`, so a `.` in a path is technically a basic-regex wildcard there; this port uses
  `strings.ReplaceAll` instead. Behaviourally identical for every real path in the source tree this
  targets (plain `word/word/word.c` names, no regex metacharacters) — flagged here as a disclosed,
  deliberate simplification rather than a silent behavior change.
- **`xx-make-pr`'s marker-leak bug is fixed (2026-07-07), in both this port and the bash original.**
  The old default "rebase" mode's PR-number-marker rewrite touched the *pushed* PR branch itself,
  not just the source/incubator branch — leaked onto merged upstream commits (PR #3162). Both
  implementations now always mark only the incubator branch, via a scripted `GIT_SEQUENCE_EDITOR`
  (appends `exec` after just the todo lines for the submitted commits) instead of a human-driven
  interactive rebase — no CLI mode flag needed or offered any more.

## Development

```sh
make test   # go test ./...
make vet    # go vet ./...
make fmt    # go fmt ./...
```

`internal/ghpr/backportcommit_test.go` covers the path-remap fallback's reorg-detection logic with
table-driven tests; the rest of the coverage so far is manual parity verification against bash
originals (documented per-subcommand in each source file's doc comment) rather than an automated
test suite — contributions welcome.

## License

AGPL-3.0-or-later. See [`LICENSE`](LICENSE).
