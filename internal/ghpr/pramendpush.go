// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Go port of scripts/pr-amend-push: fold working-tree changes into the
// current (PR head) commit of an agent clone and force-push (with lease)
// back to the PR branch. Operates purely on a given clone directory — no
// workspace-root dependency, unlike pr-checkout/backport-commit.
package ghpr

import (
	"fmt"
	"os"
	"path/filepath"
)

const prAmendPushUsage = `usage: starfleetctl pr-amend-push <clone-dir> [files...]
files...   paths (relative to the clone) to stage; default: all modified
           tracked files (git add -u).
`

// RunPRAmendPush implements `starfleetctl pr-amend-push`.
func RunPRAmendPush(args []string) int {
	if len(args) >= 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Print(prAmendPushUsage)
		return 0
	}
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, prAmendPushUsage)
		return 2
	}
	dest := args[0]
	files := args[1:]

	if fi, err := os.Stat(filepath.Join(dest, ".git")); err != nil || !fi.IsDir() {
		// .git can legitimately be a file (worktrees/submodules) — bash's
		// `[ -d "$DEST/.git" ]` would reject those too, so match that
		// literally rather than "improving" on it here.
		fmt.Fprintf(os.Stderr, "pr-amend-push: not a git clone: %s\n", dest)
		return 1
	}

	br, err := gitCapture(dest, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		fprintErr("pr-amend-push", err)
		return 1
	}
	if br == "HEAD" {
		fmt.Fprintln(os.Stderr, "pr-amend-push: clone is in detached HEAD — check out the PR branch first")
		return 1
	}

	if len(files) > 0 {
		addArgs := append([]string{"add", "--"}, files...)
		if err := gitRunErr(dest, addArgs...); err != nil {
			fprintErr("pr-amend-push", err)
			return 1
		}
	} else {
		if err := gitRunErr(dest, "add", "-u"); err != nil {
			fprintErr("pr-amend-push", err)
			return 1
		}
	}

	// `git diff --cached --quiet` exits 1 when there IS a diff, 0 when
	// there's nothing staged — the inverse of a typical error check.
	if err := gitRunErr(dest, "diff", "--cached", "--quiet"); err == nil {
		fmt.Fprintln(os.Stderr, "pr-amend-push: nothing staged — no changes to push")
		return 1
	}

	if err := gitRunErr(dest, "commit", "--amend", "--no-edit"); err != nil {
		fprintErr("pr-amend-push", err)
		return 1
	}
	if err := gitRun(dest, "show", "--stat", "HEAD"); err != nil {
		fprintErr("pr-amend-push", err)
		return 1
	}

	remote := gitConfigGet(dest, "branch."+br+".remote")
	if remote == "" {
		remote = "origin"
	}
	if err := gitRunErr(dest, "push", "--force-with-lease", remote, "HEAD:"+br); err != nil {
		fprintErr("pr-amend-push", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "pr-amend-push: force-pushed amended commit to '%s/%s'\n", remote, br)
	return 0
}
