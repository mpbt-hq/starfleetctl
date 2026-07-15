// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package flock

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestLockAcquireRelease(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.lock")

	h, err := Lock(path, Options{Timeout: 1 * time.Second, HolderLabel: "test"})
	if err != nil {
		t.Fatalf("Lock failed: %v", err)
	}
	if err := h.Close(); err != nil {
		t.Fatalf("Close failed: %v", err)
	}
}

func TestLockBlocksHeldLock(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.lock")

	h, err := Lock(path, Options{Timeout: 1 * time.Second})
	if err != nil {
		t.Fatalf("first Lock failed: %v", err)
	}
	defer h.Close()

	// a second acquire with a short timeout must fail (lock is held)
	start := time.Now()
	_, err = Lock(path, Options{Timeout: 300 * time.Millisecond})
	if err == nil {
		t.Fatal("second Lock succeeded while lock was held")
	}
	if time.Since(start) < 200*time.Millisecond {
		t.Errorf("Lock returned before the timeout elapsed: %v", time.Since(start))
	}
}

func TestLockReacquireAfterClose(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "test.lock")

	h1, err := Lock(path, Options{Timeout: 1 * time.Second})
	if err != nil {
		t.Fatal(err)
	}
	if err := h1.Close(); err != nil {
		t.Fatal(err)
	}
	// holder file must be cleared on release so it isn't mistaken for live
	if data, err := os.ReadFile(path); err == nil && len(data) != 0 {
		t.Errorf("holder not cleared on Close: %q", data)
	}

	h2, err := Lock(path, Options{Timeout: 1 * time.Second})
	if err != nil {
		t.Fatalf("reacquire after Close failed: %v", err)
	}
	h2.Close()
}
