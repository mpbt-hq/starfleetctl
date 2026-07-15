# GitHub Commands

PR viewing, commenting, labeling, and management.

## Synopsis

```
starfleetctl <command> [args...]
```

All commands are stateless wrappers around `gh` CLI (which owns auth/config). They default to the repo in the current directory; override with `REPO` env var.

## Read-Only Commands

### pr-view

```sh
starfleetctl pr-view 3162
starfleetctl pr-view 3162 "number,title,state,mergeable"
```

### pr-ci

```sh
# CI status with failure classification
starfleetctl pr-ci 3162
starfleetctl pr-ci https://github.com/user/repo/pull/123

# Machine-readable
starfleetctl pr-ci 3162 --json
```

Classifies by conclusion, not raw count — one real `FAILURE` cancels sibling jobs via fail-fast. Shows pass/fail/cancelled/pending/skip buckets and known-flake hints.

### show-branch-file

```sh
# Print a file at any branch ref
starfleetctl show-branch-file origin/master src/main.c

# Extract a specific symbol
starfleetctl show-branch-file origin/master src/main.c SomeFunction
```

### backport-applies

```sh
# Check if a commit's markers exist on release branches
starfleetctl backport-applies src/main.c "SOME_FIX" 25.2 25.1
```

### show-pr-conflict

```sh
# List all conflicting PRs
starfleetctl show-pr-conflict
```

## Mutating Commands

### pr-comment

```sh
# Post a comment
starfleetctl pr-comment 3162 comment-body.txt

# Post with bot disclosure banner
starfleetctl pr-comment 3162 comment-body.txt --bot-review
```

### pr-label

```sh
# Add/remove labels
starfleetctl pr-label 3162 add backport candidate
starfleetctl pr-label 3162 remove needs-testing

# Set review outcome (atomic swap)
starfleetctl pr-label 3162 set-review passed
starfleetctl pr-label 3162 set-review changes-requested
```

### pr-set-body / pr-append-body

```sh
# Replace PR body
starfleetctl pr-set-body 3162 new-body.md

# Append to PR body
starfleetctl pr-append-body 3162 additional-text.md
```

### pr-request-reviewers

```sh
starfleetctl pr-request-reviewers 3162 octocat dev1
```

### pr-checkout

```sh
# Set up isolated clone for a PR
DIR=$(starfleetctl pr-checkout 3162)
cd "$DIR"

# With custom agent name
starfleetctl pr-checkout 3162 my-agent
```

Handles same-repo and cross-fork PRs (wires a `fork` remote when needed).

### pr-amend-push

```sh
# Amend edits into existing commit and force-push
cd /path/to/agent-clone
starfleetctl pr-amend-push .
starfleetctl pr-amend-push . src/foo.c src/bar.c
```

### backport-commit

```sh
# Cherry-pick a commit onto a release branch
starfleetctl backport-commit 25.2 abc1234
starfleetctl backport-commit 25.2 3162
```

Falls back to path-remapped apply if source tree reorganized between branches.

### xx-make-pr

```sh
# Create PR from current branch commits
starfleetctl xx-make-pr HEAD~3..HEAD

# With explicit branch name
starfleetctl xx-make-pr --branch fix-my-bug HEAD~2..HEAD
```

Uses `git config make-pr.*` keys for upstream configuration.

## Utilities

### json

```sh
# Validate JSON
echo '{"a":1}' | starfleetctl json validate

# Pretty-print
starfleetctl json pretty data.json

# Get a field
starfleetctl json get data.json ".field.name"
```

### with-clone-lock

```sh
# Serialize git operations in any working tree
starfleetctl with-clone-lock git commit -m "safe commit"
starfleetctl with-clone-lock git push
```
