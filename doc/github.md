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

#### Staging Branch (`tmp-<branchName>`)

`xx-make-pr` creates a temporary staging branch named `tmp-<branchName>` where `<branchName>` is the PR branch name (auto-generated as `pr/<upstreamBranch>-<slug>_<timestamp>` or user-provided via `--branch`).

1. **Staging branch creation**: `tmp-<branchName>` is checked out from the upstream base branch.
2. **Cherry-pick**: Commits are cherry-picked onto the staging branch.
3. **Rename**: The staging branch is renamed to the final PR branch name (`pr/...`) via `git branch -M` before pushing.
4. **Push**: Only the final `pr/...` branch is pushed; the `tmp-<branchName>` staging branch is **never pushed**.

**No automatic cleanup on error**: If a step fails (e.g., cherry-pick conflict), the `tmp-...` branch is **left behind intentionally** so you can manually resolve conflicts and continue. There is **no automatic cleanup** — this is intentional for manual recovery.

#### Git D/F Conflict Warning

Git treats branch names like a filesystem (D/F conflict): if a branch named `foo` exists, you cannot create `foo/bar`, and vice versa. This affects the `tmp-<branchName>` staging branch:

```sh
# If you create a branch named exactly 'tmp-pr':
git branch tmp-pr

# Then run xx-make-pr (which tries to create 'tmp-pr/...'):
starfleetctl xx-make-pr --branch pr/master-feature_x
# fatal: cannot lock ref 'refs/heads/tmp-pr/master-foo_x': 'refs/heads/tmp-pr' exists
```

**Avoid creating a branch named exactly `tmp-pr`** (or any prefix that would conflict with your PR branch name). If you encounter the error, remove the conflicting branch: `git branch -D tmp-pr`.

Note: `tmp-pr-1`, `tmp-pr-2`, etc. do **not** conflict — only an exact prefix match (`tmp-pr` or `tmp-pr/...`) triggers the D/F conflict.

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
