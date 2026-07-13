---
name: starfleetctl
description: "starfleetctl fleet-management CLI — subcommand reference, deployment, and known limitations. Use when running starfleetctl commands, troubleshooting fleet coordination, or understanding the CLI's capabilities and known issues."
---

# starfleetctl — fleet-management CLI

A Go CLI that consolidates flock/race-prone shell scripts into one binary for coordinating
independent, concurrent AI-agent sessions ("ships").

Full reference: **`reference.md`** in this skill's directory. This skill is the actionable checklist.

## Deployment

```bash
# Phase A: genesis (from an existing binary)
starfleetctl genesis-init .

# Phase B: bootstrap (from the committed script)
./starfleet-bootstrap
```

Everything under `.starfleet-ai/` is gitignored. Re-run `./starfleet-bootstrap` anytime to update.

## Quick subcommand reference

### Fleet coordination

| Subcommand | Purpose |
|---|---|
| `agent-bus <cmd>` | Status board + directive bus (status/board/tell/broadcast/ack/inbox) |
| `dashboard <cmd>` | DASHBOARD.md read/write/commit cycle |
| `pr-claim <cmd>` | Advisory PR-branch lock + work log |
| `ws-commit -m <msg> <paths>` | Atomic commit+push under clone lock |
| `ship-names <cmd>` | Ship name registry (assign/release/list/gc/shell-env) |
| `with-clone-lock [cmd...]` | Serialize mutating work in a git working tree |

### GitHub interaction (read-only)

| Subcommand | Purpose |
|---|---|
| `pr-view <pr#>` | PR metadata via gh |
| `pr-ci <pr#\|URL>` | CI status classified by conclusion |
| `show-branch-file <ref> <path>` | Print file at any ref via GitHub API |
| `backport-applies <path> <grep-ERE>` | Check applicability across release lines |

### GitHub interaction (mutating)

| Subcommand | Purpose |
|---|---|
| `pr-comment <pr#> <body-file>` | Post PR comment |
| `pr-label <pr#> add\|remove` | Add/remove labels |
| `pr-set-body <pr#> <body-file>` | Replace PR body |
| `pr-checkout <pr#>` | Isolated clone for PR repair |
| `pr-amend-push <clone-dir>` | Amend + force-push |
| `backport-commit <release> <commit>` | One-shot backport |
| `xx-make-pr <commits>` | Submit PR from commits |

## Known limitations

- `agent-bus monitor-loop`/`fleet-watch` known broken under Claude Code's `Monitor` tool (workaround: bash originals)
- `backport-commit` path-remap uses `strings.ReplaceAll` not regex (simplification, no real impact)
- `xx-make-pr` marker-leak bug fixed 2026-07-07 (both Go and bash)
