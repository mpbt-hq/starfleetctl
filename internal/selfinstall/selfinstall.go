// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package selfinstall clones/pulls the starfleetctl source repository,
// builds the binary, and symlinks it into .starfleet-ai/bin/ — the
// "starfleetctl manages its own deployment" piece of item 37
// (2DO-starfleet-deployment.md). Called from the `self-install` subcommand
// (update path, when starfleetctl is already available) and also from the
// starfleet-bootstrap bash script (initial clone, handled by genesis-init).
package selfinstall

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
)

const (
	repoURL = "git@github.com:mpbt-hq/starfleetctl.git"
	srcDir  = ".starfleet-ai/src/starfleetctl"
	binDir  = ".starfleet-ai/bin"
	binName = "starfleetctl"
)

// Run clones (or pulls) the starfleetctl repo into root/.starfleet-ai/src/,
// builds it, and symlinks the binary to root/.starfleet-ai/bin/. Returns
// an exit code (0 = ok).
func Run(root string, args []string) int {
	srcPath := filepath.Join(root, srcDir)
	binPath := filepath.Join(root, binDir)
	binFile := filepath.Join(binPath, binName)

	for _, cmd := range []string{"git", "go"} {
		if _, err := exec.LookPath(cmd); err != nil {
			fmt.Fprintf(os.Stderr, "self-install: missing prerequisite: %s\n", cmd)
			return 1
		}
	}

	gitDir := filepath.Join(srcPath, ".git")
	if fi, err := os.Stat(gitDir); err == nil && fi.IsDir() {
		fmt.Println("-- updating starfleetctl source --")
		cmd := exec.Command("git", "-C", srcPath, "pull", "--ff-only", "origin", "master")
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "self-install: git pull failed: %v\n", err)
			return 1
		}
	} else {
		fmt.Println("-- cloning starfleetctl --")
		if err := os.MkdirAll(filepath.Dir(srcPath), 0o755); err != nil {
			fmt.Fprintf(os.Stderr, "self-install: mkdir: %v\n", err)
			return 1
		}
		cmd := exec.Command("git", "clone", repoURL, srcPath)
		cmd.Stdout = os.Stdout
		cmd.Stderr = os.Stderr
		if err := cmd.Run(); err != nil {
			fmt.Fprintf(os.Stderr, "self-install: git clone failed: %v\n", err)
			return 1
		}
	}

	fmt.Println("-- building starfleetctl --")
	build := exec.Command("go", "build", "-o", "starfleetctl", "./cmd/starfleetctl")
	build.Dir = srcPath
	build.Stdout = os.Stdout
	build.Stderr = os.Stderr
	if err := build.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "self-install: go build failed: %v\n", err)
		return 1
	}

	if err := os.MkdirAll(binPath, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "self-install: mkdir bin: %v\n", err)
		return 1
	}
	os.Remove(binFile)
	if err := os.Symlink("../src/starfleetctl/starfleetctl", binFile); err != nil {
		fmt.Fprintf(os.Stderr, "self-install: symlink: %v\n", err)
		return 1
	}

	fmt.Println("self-install: done — starfleetctl rebuilt")
	return 0
}
