// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package agentbus

import (
	"os"
	"path/filepath"
	"syscall"
)

// lockHandle is a held exclusive lock on BUS_DIR/.lock, mirroring bash's
// `exec 9>"$BUS_DIR/.lock"; flock 9` (held for the rest of the process,
// released here explicitly via Close instead of process exit).
type lockHandle struct{ f *os.File }

// lockBus takes the exclusive bus lock, blocking until acquired — same
// mutual-exclusion domain as the bash script's flock 9, so a Go and bash
// agent-bus invocation never interleave a read-modify-write on the same file.
func (b *Bus) lockBus() (*lockHandle, error) {
	f, err := os.OpenFile(filepath.Join(b.BusDir, ".lock"), os.O_CREATE|os.O_RDWR, 0o644)
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
