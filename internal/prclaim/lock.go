// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package prclaim

import (
	"os"
	"path/filepath"
	"syscall"
)

// lockHandle is a held exclusive lock on <ClaimDir>/.lock, mirroring bash's
// `exec 9>"$CLAIM_DIR/.lock"; flock 9` (registry-wide lock, held only for
// the duration of one read-modify-write, unlike the bus lock which the bash
// original also holds for the whole process — here each Do* call takes and
// releases it, matching the bash functions' per-call `lock_registry`).
type lockHandle struct{ f *os.File }

func (c *Claims) lockRegistry() (*lockHandle, error) {
	f, err := os.OpenFile(filepath.Join(c.ClaimDir, ".lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX); err != nil {
		f.Close()
		return nil, err
	}
	return &lockHandle{f: f}, nil
}

func (l *lockHandle) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	return l.f.Close()
}
