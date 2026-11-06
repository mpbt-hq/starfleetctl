//go:build unix

// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
package wormhole

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"testing"
	"time"
)

// TestGitCommandTimeoutKillsBlockedProcess proves GT-3/GT-6: git operations
// that exceed their configured deadline are terminated (the direct git process
// is killed by exec.CommandContext) and the call returns with a timeout error
// instead of hanging. Covered: gitIndexLocked/gitLocked, clone, and the
// CommandTimeout helpers gitConfigIn, gitRevParseIn, currentHEAD and
// validateCloneDir. No process group termination is asserted — the implementation
// kills only the direct process (default CommandContext behavior, no Setpgid).
func TestGitCommandTimeoutKillsBlockedProcess(t *testing.T) {
	const (
		deadline = 200 * time.Millisecond
		minWait  = 150 * time.Millisecond // proves the call actually waited for the deadline
		maxWait  = 2 * time.Second        // deadline + generous scheduling tolerance
	)

	// gitPIDsWithMarker returns PIDs of git processes whose command line contains
	// the marker. Used to observe the concretely started direct git process and to
	// prove no matching blocked process remains after the call. On Linux and other
	// systems with /proc, /proc/<pid>/cmdline is read; otherwise `ps` is queried
	// (BSD and POSIX argument forms are tried). The second result reports whether
	// process enumeration is available: a caller must not treat "no matches" as a
	// successful proof when enumeration itself is unavailable.
	gitPIDsWithMarker := func(marker string) ([]int, bool) {
		if entries, err := os.ReadDir("/proc"); err == nil {
			var pids []int
			for _, e := range entries {
				if !e.IsDir() {
					continue
				}
				pid, err := strconv.Atoi(e.Name())
				if err != nil {
					continue
				}
				data, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
				if err != nil {
					continue
				}
				cmdline := string(bytes.ReplaceAll(data, []byte{0}, []byte{' '}))
				if strings.Contains(cmdline, "git") && strings.Contains(cmdline, marker) {
					pids = append(pids, pid)
				}
			}
			return pids, true
		}
		for _, args := range [][]string{
			{"-axo", "pid=,command="}, // BSD (Linux, macOS, *BSD)
			{"-eo", "pid=,args="},     // POSIX/SysV (e.g. illumos)
		} {
			out, err := exec.Command("ps", args...).Output()
			if err != nil {
				continue
			}
			var pids []int
			for _, line := range strings.Split(string(out), "\n") {
				line = strings.TrimSpace(line)
				if line == "" {
					continue
				}
				fields := strings.Fields(line)
				if len(fields) < 2 {
					continue
				}
				pid, err := strconv.Atoi(fields[0])
				if err != nil {
					continue
				}
				cmdline := strings.Join(fields[1:], " ")
				if strings.Contains(cmdline, "git") && strings.Contains(cmdline, marker) {
					pids = append(pids, pid)
				}
			}
			return pids, true
		}
		return nil, false
	}

	requireScanner := func(t *testing.T, label string) {
		t.Helper()
		if _, ok := gitPIDsWithMarker(""); !ok {
			t.Fatalf("%s: cannot enumerate processes (no /proc and ps unavailable); cannot prove direct-process termination on this platform", label)
		}
	}

	// runAndObserve runs fn (a single git operation) concurrently, polls for the
	// concretely started git process matching marker, and asserts: the process was
	// observed while blocked, the call aborted around the configured deadline with
	// a timeout error (cause = deadline, not some other failure), the observed
	// direct process is gone afterwards, and no matching git process lingers.
	runAndObserve := func(t *testing.T, label, marker, wantErrSubstr string, fn func() error) {
		t.Helper()
		requireScanner(t, label)
		if pids, _ := gitPIDsWithMarker(marker); len(pids) != 0 {
			t.Fatalf("%s: precondition: lingering git process(es) %v already match marker", label, pids)
		}

		type result struct {
			err     error
			elapsed time.Duration
		}
		ch := make(chan result, 1)
		start := time.Now()
		go func() {
			err := fn()
			ch <- result{err: err, elapsed: time.Since(start)}
		}()

		observedPID := 0
		watchdog := time.NewTimer(10 * time.Second)
		defer watchdog.Stop()
		ticker := time.NewTicker(5 * time.Millisecond)
		defer ticker.Stop()
		var res result
	waitLoop:
		for {
			select {
			case res = <-ch:
				break waitLoop
			case <-ticker.C:
				if observedPID == 0 {
					if pids, ok := gitPIDsWithMarker(marker); ok && len(pids) > 0 {
						observedPID = pids[0]
					}
				}
			case <-watchdog.C:
				t.Fatalf("%s: operation did not return; blocked git process was not terminated", label)
			}
		}

		if res.err == nil {
			t.Fatalf("%s: expected timeout error, got nil (after %v)", label, res.elapsed)
		}
		if !strings.Contains(res.err.Error(), wantErrSubstr) {
			t.Fatalf("%s: expected timeout cause containing %q, got: %v", label, wantErrSubstr, res.err)
		}
		if res.elapsed < minWait {
			t.Fatalf("%s: returned after %v (< %v); operation did not block until the deadline (timeout not the cause?)", label, res.elapsed, minWait)
		}
		if res.elapsed > maxWait {
			t.Fatalf("%s: returned after %v (> %v); blocked git process was not terminated at deadline", label, res.elapsed, maxWait)
		}
		if observedPID == 0 {
			t.Fatalf("%s: did not observe the concretely started git process (marker %q) while blocked — cannot prove direct-process termination", label, marker)
		}
		// The observed direct git process must be gone (CommandContext kills the
		// direct process; no process-group kill is claimed). ESRCH proves it no
		// longer exists; EPERM means it still exists but is not signalable (not a
		// termination proof); any other error is also not a success.
		if err := syscall.Kill(observedPID, 0); err == nil {
			t.Fatalf("%s: observed direct git process pid %d still alive after timeout", label, observedPID)
		} else if errors.Is(err, syscall.EPERM) {
			t.Fatalf("%s: observed direct git process pid %d still exists but is not signalable (EPERM); cannot prove termination", label, observedPID)
		} else if !errors.Is(err, syscall.ESRCH) {
			t.Fatalf("%s: cannot verify observed direct git process pid %d termination: %v", label, observedPID, err)
		}
		// No matching git process may linger after the timeout.
		for i := 0; i < 20; i++ {
			if pids, ok := gitPIDsWithMarker(marker); !ok {
				t.Fatalf("%s: process enumeration became unavailable; cannot prove no process lingers", label)
			} else if len(pids) == 0 {
				return
			}
			time.Sleep(10 * time.Millisecond)
		}
		if pids, _ := gitPIDsWithMarker(marker); len(pids) != 0 {
			t.Fatalf("%s: blocked git process(es) still linger after timeout: %v", label, pids)
		}
	}

	// GT-3 (locked command) + GT-6 helpers, exercised on a prepared clone.
	// A FIFO with no writer, exported as GIT_CONFIG_GLOBAL, makes every git
	// invocation block deterministically when reading its config.
	t.Run("helpers and locked command", func(t *testing.T) {
		remote := setupRemote(t)
		root := t.TempDir()
		cfg := testConfig(remote)
		cfg.Sync.CommandTimeout = deadline.String()
		cfg.Sync.CloneTimeout = deadline.String()
		writeConfig(t, root, cfg)
		cloneTimeout, commandTimeout := testTimeouts(cfg)
		sp := newSpool(root, cfg.Repositories[0], 0, "alpha", cloneTimeout, commandTimeout)

		// Prepare a real clone with the FIFO inactive.
		if err := sp.Publish(NewMessage("alpha", "beta", "info", "timeout-seed", "")); err != nil {
			t.Fatalf("publish seed: %v", err)
		}
		cloneDir := sp.git.CloneDir()

		fifoDir := t.TempDir()
		fifo := filepath.Join(fifoDir, "gitconfig")
		if err := exec.Command("mkfifo", fifo).Run(); err != nil {
			t.Fatalf("mkfifo: %v", err)
		}
		t.Setenv("GIT_CONFIG_GLOBAL", fifo)

		// GT-3: locked git command terminated at the configured deadline.
		runAndObserve(t, "gitLocked(status)", cloneDir, "git command timed out after", func() error {
			_, err := sp.git.gitLocked("status")
			return err
		})

		// GT-6: gitConfigIn / gitRevParseIn / validateCloneDir each apply
		// CommandTimeout and return a timeout-caused error.
		runAndObserve(t, "gitConfigIn", cloneDir, "git config timeout after", func() error {
			_, err := sp.git.gitConfigIn(cloneDir, "user.name")
			return err
		})
		runAndObserve(t, "gitRevParseIn", cloneDir, "git rev-parse timeout after", func() error {
			_, err := sp.git.gitRevParseIn(cloneDir, "rev-parse", "HEAD")
			return err
		})
		// validateCloneDir calls gitConfigIn first; the timeout surfaces from
		// that path, wrapped by validateCloneDir's own error.
		runAndObserve(t, "validateCloneDir", cloneDir, "git config timeout after", func() error {
			return sp.git.validateCloneDir(cloneDir)
		})

		// GT-6: currentHEAD swallows errors (returns "" on failure) but must
		// still respect CommandTimeout and terminate the blocked git process.
		// We prove the underlying operation was genuinely blocked (observed git
		// process), aborted around the deadline, and left no lingering process.
		requireScanner(t, "currentHEAD")
		if pids, _ := gitPIDsWithMarker(cloneDir); len(pids) != 0 {
			t.Fatalf("currentHEAD: precondition: lingering git process(es) %v", pids)
		}
		type headRes struct {
			head    string
			elapsed time.Duration
		}
		headCh := make(chan headRes, 1)
		headStart := time.Now()
		go func() {
			h := sp.git.currentHEAD()
			headCh <- headRes{head: h, elapsed: time.Since(headStart)}
		}()
		observedHEADPID := 0
		headWatchdog := time.NewTimer(10 * time.Second)
		defer headWatchdog.Stop()
		headTicker := time.NewTicker(5 * time.Millisecond)
		defer headTicker.Stop()
		var hres headRes
	headWait:
		for {
			select {
			case hres = <-headCh:
				break headWait
			case <-headTicker.C:
				if observedHEADPID == 0 {
					if pids, ok := gitPIDsWithMarker(cloneDir); ok && len(pids) > 0 {
						observedHEADPID = pids[0]
					}
				}
			case <-headWatchdog.C:
				t.Fatal("currentHEAD: operation did not return; blocked git process was not terminated")
			}
		}
		if hres.head != "" {
			t.Fatalf("currentHEAD on blocked git should return empty, got %q", hres.head)
		}
		if hres.elapsed < minWait || hres.elapsed > maxWait {
			t.Fatalf("currentHEAD: returned after %v (want ~%v..%v); deadline not enforced", hres.elapsed, minWait, maxWait)
		}
		if observedHEADPID == 0 {
			t.Fatalf("currentHEAD: did not observe the concretely started git process (marker %q) while blocked — cannot prove direct-process termination", cloneDir)
		}
		if err := syscall.Kill(observedHEADPID, 0); err == nil {
			t.Fatalf("currentHEAD: observed direct git process pid %d still alive after timeout", observedHEADPID)
		} else if errors.Is(err, syscall.EPERM) {
			t.Fatalf("currentHEAD: observed direct git process pid %d still exists but is not signalable (EPERM); cannot prove termination", observedHEADPID)
		} else if !errors.Is(err, syscall.ESRCH) {
			t.Fatalf("currentHEAD: cannot verify observed direct git process pid %d termination: %v", observedHEADPID, err)
		}
		lingered := false
		for i := 0; i < 20; i++ {
			pids, ok := gitPIDsWithMarker(cloneDir)
			if !ok {
				t.Fatal("currentHEAD: process enumeration became unavailable; cannot prove no process lingers")
			}
			if len(pids) == 0 {
				lingered = false
				break
			}
			lingered = true
			time.Sleep(10 * time.Millisecond)
		}
		if lingered {
			if pids, _ := gitPIDsWithMarker(cloneDir); len(pids) != 0 {
				t.Fatalf("currentHEAD: blocked git process(es) still linger after timeout: %v", pids)
			}
		}
	})

	// GT-3 (clone path): EnsureCloneLocked must fail with a clone timeout when
	// the initial clone is blocked, and the direct clone process must be
	// terminated (no lingering process referencing the remote).
	t.Run("clone path", func(t *testing.T) {
		remote := setupRemote(t)
		root := t.TempDir()
		cfg := testConfig(remote)
		cfg.Sync.CloneTimeout = deadline.String()
		cfg.Sync.CommandTimeout = deadline.String()
		writeConfig(t, root, cfg)
		cloneTimeout, commandTimeout := testTimeouts(cfg)

		fifoDir := t.TempDir()
		fifo := filepath.Join(fifoDir, "gitconfig")
		if err := exec.Command("mkfifo", fifo).Run(); err != nil {
			t.Fatalf("mkfifo: %v", err)
		}
		t.Setenv("GIT_CONFIG_GLOBAL", fifo)

		sp := newSpool(root, cfg.Repositories[0], 0, "alpha", cloneTimeout, commandTimeout)
		// "git clone" cmdline contains the remote URL/path; use it as marker.
		runAndObserve(t, "EnsureCloneLocked", remote, "clone timed out after", func() error {
			return sp.git.EnsureCloneLocked()
		})
	})
}
