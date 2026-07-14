// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package dashboard

import (
	"fmt"
	"io"
	"os"
)

// DoPull syncs local DASHBOARD.md (and repo) with origin before editing —
// mirrors `scripts/dashboard pull`.
func (d *Dashboard) DoPull() error {
	return d.sync(run)
}

// DoShow prints the current DASHBOARD.md, implicitly pulling first. Sync
// output goes to stderr so stdout carries only the file content — mirrors
// `scripts/dashboard show`.
func (d *Dashboard) DoShow() error {
	if err := d.sync(runQuiet); err != nil {
		return err
	}
	data, err := os.ReadFile(d.File)
	if err != nil {
		return err
	}
	_, err = os.Stdout.Write(data)
	return err
}

// DoWrite replaces DASHBOARD.md's content from src ("-" for stdin) — for
// scripted/non-interactive updates. Does NOT commit — mirrors
// `scripts/dashboard write`.
func (d *Dashboard) DoWrite(src string) error {
	var r io.Reader
	if src == "-" {
		r = os.Stdin
	} else {
		f, err := os.Open(src)
		if err != nil {
			return err
		}
		defer f.Close()
		r = f
	}
	data, err := io.ReadAll(r)
	if err != nil {
		return err
	}
	return os.WriteFile(d.File, data, 0o644)
}

// DoCommit stages, commits, and (unless noPush is true) pushes DASHBOARD.md —
// the Go equivalent of `scripts/dashboard commit`, which wraps ws-commit.
// Runs under the shared clone lock (see lock.go) so a concurrent bash actor
// on the same clone serializes against it rather than racing the index/HEAD.
func (d *Dashboard) DoCommit(msg string, push bool) error {
	lh, err := d.lock()
	if err != nil {
		return err
	}
	defer lh.Close()

	// d.File lives under .starfleet-ai/ which the workspace .gitignore
	// excludes, so a plain `git add` refuses the (initially untracked) file.
	// Force-add: committing DASHBOARD.md is the whole point of this command,
	// and once tracked the path stays addable. Mirrors the bash original's
	// `git add -f`.
	if err := run(d.Root, "git", "add", "-f", d.File); err != nil {
		return err
	}

	// `git diff --cached --quiet` exits 0 when nothing is staged.
	if err := run(d.Root, "git", "diff", "--cached", "--quiet", "--", d.File); err == nil {
		fmt.Println("dashboard: nothing staged — nothing to commit")
		return nil
	}

	if err := run(d.Root, "git", "commit", "-m", msg); err != nil {
		return err
	}

	if !push {
		return nil
	}

	branch, err := d.branch()
	if err != nil {
		return err
	}
	// Best-effort, mirrors ws-commit's `|| true`: a concurrent push landing
	// between our commit and push is integrated race-free since we hold the
	// clone lock; a genuine rebase conflict still surfaces on the push below.
	_ = run(d.Root, "git", "pull", "--rebase", "--autostash")
	return run(d.Root, "git", "push", "origin", branch)
}

// sync runs `git pull --rebase --autostash` (no explicit remote/branch —
// relies on the checked-out branch's configured upstream, so this also
// works on a differently-named local branch tracking a remote one, e.g. a
// scripts/worktree checkout) using the given runner (run for visible
// output, runQuiet to suppress it to stderr).
func (d *Dashboard) sync(runner func(dir, name string, args ...string) error) error {
	return runner(d.Root, "git", "pull", "--rebase", "--autostash")
}
