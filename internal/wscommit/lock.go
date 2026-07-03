// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package wscommit

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"syscall"
	"time"
)

// lockHandle is a held exclusive lock on <gitdir>/mpbt-clone.lock — the SAME
// file scripts/with-clone-lock / scripts/ws-commit / internal/dashboard use,
// so all four serialize against each other on one working tree.
type lockHandle struct{ f *os.File }

// lock acquires the shared clone lock, honoring LOCK_WAIT (seconds, default
// 600) like bash's with-clone-lock does with `flock -w`.
func (w *WsCommit) lock() (*lockHandle, error) {
	path := filepath.Join(w.GitDir, "mpbt-clone.lock")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}

	wait := 600 * time.Second
	if v := os.Getenv("LOCK_WAIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			wait = time.Duration(n) * time.Second
		}
	}

	deadline := time.Now().Add(wait)
	for {
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			break
		}
		if time.Now().After(deadline) {
			holder, _ := os.ReadFile(path)
			f.Close()
			return nil, fmt.Errorf("ws-commit: could not acquire clone lock within %s\nws-commit: current holder ->\n%s", wait, holder)
		}
		time.Sleep(200 * time.Millisecond)
	}

	holder := fmt.Sprintf("pid=%d user=%s cmd=starfleetctl-ws-commit\n", os.Getpid(), os.Getenv("USER"))
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(holder), 0)

	return &lockHandle{f: f}, nil
}

func (l *lockHandle) Close() error {
	if l == nil || l.f == nil {
		return nil
	}
	_ = syscall.Flock(int(l.f.Fd()), syscall.LOCK_UN)
	return l.f.Close()
}
