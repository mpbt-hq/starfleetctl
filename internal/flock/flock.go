// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

// Package flock provides a single shared implementation of the advisory
// exclusive file lock every starfleetctl subcommand uses to serialize
// read-modify-write cycles on the shared working tree / bus / registry.
//
// It mirrors bash's `flock -w <secs>` (LOCK_EX|LOCK_NB in a polling loop with
// a deadline) and, when a holder label is supplied, records the current
// holder into the lock file and clears it again on release — so a crashed
// process can never leave a stale holder behind for the next waiter to
// misreport.
//
// Before this package existed, six near-identical lockHandle + lock()
// implementations lived in comms/prclaim/dashboard/agents/shipnames/
// wscommit; they diverged (some blocked forever, some never recorded a
// holder). This package is the one true implementation.
package flock

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

// Options configures a Lock call.
type Options struct {
	// Timeout bounds how long Lock waits for the lock before returning an
	// error. Zero means block indefinitely (generally discouraged — prefer a
	// finite timeout so a wedged holder can't hang the caller forever).
	Timeout time.Duration
	// HolderLabel, when non-empty, is written into the lock file on
	// acquisition and truncated away on Close, so a crash can't leave a stale
	// holder for the next waiter to misreport.
	HolderLabel string
}

// Handle is a held exclusive lock. Call Close to release it.
type Handle struct {
	f *os.File
}

// Close releases the lock (unlocking then closing the fd) and, if a holder
// label was recorded on acquisition, clears the holder file so it can't be
// mistaken for a live holder by a later waiter.
func (h *Handle) Close() error {
	if h == nil || h.f == nil {
		return nil
	}
	_ = syscall.Flock(int(h.f.Fd()), syscall.LOCK_UN)
	_ = h.f.Truncate(0)
	err := h.f.Close()
	h.f = nil
	return err
}

// Lock acquires an exclusive advisory lock on path. It polls with
// LOCK_EX|LOCK_NB until acquired or Options.Timeout elapses (when Timeout is
// zero it blocks until acquired). On timeout it reports the current holder
// content (best-effort) in the error so the caller can surface who is
// holding the lock.
func Lock(path string, opts Options) (*Handle, error) {
	f, err := os.OpenFile(path, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		return nil, err
	}

	var deadline time.Time
	blocking := opts.Timeout <= 0
	if !blocking {
		deadline = time.Now().Add(opts.Timeout)
	}

	for {
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			break
		}
		if blocking {
			// Should not happen with LOCK_NB + blocking=false, but guard anyway.
			continue
		}
		if time.Now().After(deadline) {
			holder, _ := os.ReadFile(path)
			f.Close()
			return nil, fmt.Errorf("flock: could not acquire %s within %s\nflock: current holder ->\n%s",
				filepath.Base(path), opts.Timeout, string(holder))
		}
		time.Sleep(200 * time.Millisecond)
	}

	if opts.HolderLabel != "" {
		holder := fmt.Sprintf("pid=%d user=%s cmd=%s\n", os.Getpid(), os.Getenv("USER"), opts.HolderLabel)
		_ = f.Truncate(0)
		_, _ = f.WriteAt([]byte(holder), 0)
	}

	return &Handle{f: f}, nil
}
