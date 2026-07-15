# pr-claim

Advisory PR-branch locking for concurrent agent sessions.

## Synopsis

```
starfleetctl pr-claim <pr#> ["what"]
starfleetctl pr-claim --list [--json]
starfleetctl pr-claim --release <pr#>
starfleetctl pr-claim --release-all
starfleetctl pr-claim --steal <pr#> ["what"]
starfleetctl pr-claim --who <pr#>
```

## How It Works

Each agent works in its own clone, but pushes to the same GitHub PR branch. `pr-claim` is a cooperative registry — agents claim a PR before editing it, so others know to avoid it.

Claims are **advisory only**: they don't block git operations at the filesystem level. They work because all participating agents check claims before starting work.

## Commands

```sh
# Claim a PR
starfleetctl pr-claim 3162 "fixing build regression"

# List all claims
starfleetctl pr-claim --list
starfleetctl pr-claim --list --json

# See who owns a claim
starfleetctl pr-claim --who 3162

# Release your claim
starfleetctl pr-claim --release 3162

# Release all your claims
starfleetctl pr-claim --release-all

# Take over a claim (e.g. if the original agent is stuck)
starfleetctl pr-claim --steal 3162 "taking over, original timed out"
```

## Storage

Claims are stored in `_WORK_/agent-claims/pr-<number>.tsv`:

```
epoch   timestamp           agent     what
173498  2026-07-15T10:00:00 Voyager  fixing build regression
```

Stale claims (older than `CLAIM_TTL`, default 1 hour) are automatically ignored.

## Interoperability

Reads/writes the same format as the bash original (`scripts/pr-claim`).
