// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package shipnames

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// lockHandle is a held exclusive lock on <ShipsDir>/.assign.lock — the SAME
// file scripts/ship-names uses for `assign`. Only `assign` locks; release/
// list/gc/flagship are unlocked in the bash original too.
type lockHandle struct{ f *os.File }

// assignLock mirrors bash's `flock -w 10 9` — a fixed 10s wait, not
// configurable (unlike LOCK_WAIT elsewhere), matching scripts/ship-names.
func (r *Registry) assignLock() (*lockHandle, error) {
	f, err := os.OpenFile(filepath.Join(r.ShipsDir, ".assign.lock"), os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(10 * time.Second)
	for {
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			return &lockHandle{f: f}, nil
		}
		if time.Now().After(deadline) {
			f.Close()
			return nil, fmt.Errorf("assign lock timeout")
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func (l *lockHandle) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	return l.f.Close()
}
