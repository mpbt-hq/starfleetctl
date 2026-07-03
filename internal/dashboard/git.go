// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package dashboard

import (
	"bytes"
	"os"
	"os/exec"
)

// run executes a command with stdin/stdout/stderr connected to ours —
// mirrors the bash script's un-redirected commands (e.g. plain `pull`).
func run(dir string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// runQuiet is like run but routes stdout to our stderr too — mirrors the
// bash `show` case's `sync >&2`, which keeps git's pull chatter off stdout
// so only the file content is printed there.
func runQuiet(dir string, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// runCapture executes a command and returns its trimmed stdout.
func runCapture(dir string, name string, args ...string) (string, error) {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	err := cmd.Run()
	return out.String(), err
}
