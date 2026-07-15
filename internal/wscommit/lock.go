// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package wscommit

import (
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/metux/starfleetctl/internal/flock"
)

// lock acquires the shared clone lock, honoring LOCK_WAIT (seconds, default
// 600) like bash's with-clone-lock does with `flock -w`. It is the SAME file
// scripts/with-clone-lock / scripts/ws-commit / internal/dashboard use, so
// all four serialize against each other on one working tree.
func (w *WsCommit) lock() (*flock.Handle, error) {
	path := filepath.Join(w.GitDir, "mpbt-clone.lock")

	wait := 600 * time.Second
	if v := os.Getenv("LOCK_WAIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			wait = time.Duration(n) * time.Second
		}
	}

	return flock.Lock(path, flock.Options{
		Timeout:     wait,
		HolderLabel: "starfleetctl-ws-commit",
	})
}
