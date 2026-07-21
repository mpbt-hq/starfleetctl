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
| `github pr claim <cmd>` | Advisory PR-branch lock + work log |
| `ws-commit -m <msg> <paths>` | Atomic commit+push under clone lock |
| `ship-names <cmd>` | Ship name registry (assign/release/list/gc/shell-env) |
| `with-clone-lock [cmd...]` | Serialize mutating work in a git working tree |

### GitHub interaction (read-only)

| Subcommand | Purpose |
|---|---|
| `github pr view <pr#>` | PR metadata via gh |
| `github pr ci <pr#\|URL>` | CI status classified by conclusion |
| `github pr show-branch-file <ref> <path>` | Print file at any ref via GitHub API |
| `github backport applies <path> <grep-ERE> [release...]` | Check applicability across release lines (defaults from project config) |

### GitHub interaction (mutating)

| Subcommand | Purpose |
|---|---|
| `github pr comment <pr#> <body-file>` | Post PR comment |
| `github pr label <pr#> add\|remove` | Add/remove labels |
| `github pr set-body <pr#> <body-file>` | Replace PR body |
| `github pr checkout <pr#>` | Isolated clone for PR repair |
| `github pr amend-push <clone-dir>` | Amend + force-push |
| `github backport commit <release> <commit>` | One-shot backport |
| `github pr make <commits>` | Submit PR from commits |

## Known limitations

- `agent-bus monitor-loop`/`fleet-watch` known broken under Claude Code's `Monitor` tool (workaround: bash originals)
- `github backport commit` path-remap uses project config (`.starfleet-ai/conf/project.yaml`) for prefix/behavior (default: `Xext/`, enabled for xserver)
- `github pr make` marker-leak bug fixed 2026-07-07 (both Go and bash)
