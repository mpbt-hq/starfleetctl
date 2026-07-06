// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package ghpr

import (
	"bytes"
	"os"
	"os/exec"
)

// gitRun executes `git <args...>` in dir with stdin/stdout/stderr connected
// to ours — mirrors an un-redirected bash `git -C "$DEST" ...` call.
func gitRun(dir string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// gitRunErr is gitRun but with stdout routed to our stderr too — for git
// commands whose progress output shouldn't land on our own stdout (mirrors
// the bash originals' `>&2` redirections on progress-only git calls).
func gitRunErr(dir string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// gitRunSilent runs `git <args...>` in dir with all output discarded — for
// speculative cleanup calls where any outcome (success or failure) is
// expected and uninteresting (mirrors the bash originals' `... 2>/dev/null
// || true` on the same calls).
func gitRunSilent(dir string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	return cmd.Run()
}

// gitCapture runs `git <args...>` in dir and returns its trimmed stdout.
func gitCapture(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	return trimTrailingNewline(out.String()), err
}

// gitCaptureQuiet is gitCapture with stderr discarded too — for probes where
// a non-zero exit is an expected, silent outcome (e.g. testing whether a
// commit object exists locally).
func gitCaptureQuiet(dir string, args ...string) (string, error) {
	cmd := exec.Command("git", append([]string{"-C", dir}, args...)...)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	return trimTrailingNewline(out.String()), err
}

// gitConfigGet reads a single git config key in dir, returning "" if unset
// (mirrors bash's `git config --get X || true`).
func gitConfigGet(dir, key string) string {
	v, err := gitCaptureQuiet(dir, "config", "--get", key)
	if err != nil {
		return ""
	}
	return v
}

func trimTrailingNewline(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == '\r') {
		s = s[:len(s)-1]
	}
	return s
}
