// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package sop

import (
	"os"
	"path/filepath"
	"strconv"
	"time"

	"github.com/metux/starfleetctl/internal/flock"
)

// lock acquires the shared clone lock, honoring LOCK_WAIT (seconds, default
// 600) like bash's with-clone-lock does with `flock -w`. It is the SAME file
// scripts/with-clone-lock / internal/dashboard / internal/wscommit use, so a
// Go ships commit and a concurrent bash/Go actor on this clone serialize
// against each other instead of both mutating the index/HEAD at once.
func (s *SOP) lock() (*flock.Handle, error) {
	path := filepath.Join(s.GitDir, "mpbt-clone.lock")

	wait := 600 * time.Second
	if v := os.Getenv("LOCK_WAIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			wait = time.Duration(n) * time.Second
		}
	}

	return flock.Lock(path, flock.Options{
		Timeout:     wait,
		HolderLabel: "starfleetctl-sop",
	})
}
