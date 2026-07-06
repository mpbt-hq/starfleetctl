// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package withclonelock is the Go port of scripts/with-clone-lock — the
// generic "serialize mutating work within a single clone/working tree"
// primitive every other fleet-management lock (ws-commit, dashboard) is
// built on. It shares the exact same <gitdir>/mpbt-clone.lock file, so a Go
// invocation and a bash with-clone-lock/ws-commit/dashboard on the same
// working tree serialize against each other rather than racing the
// index/HEAD. ADVISORY ONLY, like the bash original: it only protects
// against other actors that also go through this wrapper (or a sibling that
// shares the same lock file).
package withclonelock

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// Run resolves the given directory's git dir, acquires the shared clone
// lock (honoring LOCK_WAIT seconds, default 600, like bash's `flock -w`),
// records holder diagnostics, then execs the given command with that lock
// held — or, with no command, an interactive shell, mirroring
// `with-clone-lock` / `with-clone-lock <command> [args...]`. Returns the
// process exit code.
func Run(dir string, args []string) int {
	gitDir, err := runCapture(dir, "git", "rev-parse", "--absolute-git-dir")
	if err != nil {
		fmt.Fprintln(os.Stderr, "with-clone-lock: not inside a git working tree")
		return 1
	}
	gitDir = strings.TrimSpace(gitDir)

	lockPath := filepath.Join(gitDir, "mpbt-clone.lock")
	f, err := os.OpenFile(lockPath, os.O_CREATE|os.O_RDWR, 0o644)
	if err != nil {
		fmt.Fprintln(os.Stderr, "with-clone-lock:", err)
		return 1
	}
	defer f.Close()

	wait := 600 * time.Second
	if v := os.Getenv("LOCK_WAIT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			wait = time.Duration(n) * time.Second
		}
	}

	deadline := time.Now().Add(wait)
	acquired := false
	for {
		if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
			acquired = true
			break
		}
		if time.Now().After(deadline) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !acquired {
		holder, _ := os.ReadFile(lockPath)
		fmt.Fprintf(os.Stderr, "with-clone-lock: could not acquire lock within %s\n", wait)
		fmt.Fprintln(os.Stderr, "with-clone-lock: current holder ->")
		fmt.Fprint(os.Stderr, string(holder))
		return 1
	}
	// Lock stays held for the lifetime of this process (released on exit,
	// same as bash's `exec 9>...; flock 9` — no explicit unlock needed since
	// the fd closes when the process exits).

	label := "<interactive shell>"
	if len(args) > 0 {
		label = strings.Join(args, " ")
	}
	holder := fmt.Sprintf("pid=%d user=%s host=%s cmd=%s\n", os.Getpid(), os.Getenv("USER"), hostname(), label)
	_ = f.Truncate(0)
	_, _ = f.WriteAt([]byte(holder), 0)

	var cmd *exec.Cmd
	if len(args) == 0 {
		shell := os.Getenv("SHELL")
		if shell == "" {
			shell = "bash"
		}
		cmd = exec.Command(shell)
	} else {
		cmd = exec.Command(args[0], args[1:]...)
	}
	cmd.Dir = dir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintln(os.Stderr, "with-clone-lock:", err)
		return 1
	}
	return 0
}

func hostname() string {
	h, err := os.Hostname()
	if err != nil {
		return "?"
	}
	return h
}

func runCapture(dir, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	out, err := cmd.Output()
	return string(out), err
}
