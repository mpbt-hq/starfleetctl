// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package wscommit

import "fmt"

// DoCommit stages the given paths, commits, and (unless push is false)
// pulls --rebase --autostash then pushes — the Go equivalent of
// scripts/ws-commit. paths may be the literal single element "-u" (the
// -a/--all flag's expansion, mirrored verbatim from the bash original,
// which builds paths=(-u) and passes it straight to `git add`). Runs under
// the shared clone lock for the whole add+commit+pull+push, so a concurrent
// push landing between our commit and push is integrated race-free (we hold
// the lock; the rebase replays our commit on top, then we push).
func (w *WsCommit) DoCommit(msg string, paths []string, push bool) error {
	if msg == "" {
		return fmt.Errorf("-m <message> is required")
	}
	if len(paths) == 0 {
		return fmt.Errorf("no paths given (use -a for all tracked changes)")
	}

	lh, err := w.lock()
	if err != nil {
		return err
	}
	defer lh.Close()

	if err := run(w.Root, "git", append([]string{"add"}, paths...)...); err != nil {
		return err
	}

	// `git diff --cached --quiet` exits 0 when nothing is staged.
	if err := run(w.Root, "git", "diff", "--cached", "--quiet"); err == nil {
		fmt.Println("ws-commit: nothing staged — nothing to commit")
		return nil
	}

	if err := run(w.Root, "git", "commit", "-m", msg); err != nil {
		return err
	}

	if !push {
		return nil
	}

	branch, err := w.branch()
	if err != nil {
		return err
	}
	// Best-effort, mirrors bash's `|| true`.
	_ = run(w.Root, "git", "pull", "--rebase", "--autostash")
	return run(w.Root, "git", "push", "origin", branch)
}
