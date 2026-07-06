// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// starfleetctl consolidates the flock/race-prone mpbt-workspace
// fleet-coordination scripts (agent-bus, pr-claim, ws-commit — in that
// order) into one Go CLI, one subcommand per script. See
// mpbt-workspace/DASHBOARD.md ("mpbtctl" row) and AGENTS.md for the full
// rationale and rollout plan. Lives in its own repo (metux/starfleetctl),
// built as an mpbt-workspace solution like go-x11proto/flyingtux, since it
// coordinates sessions across the workspace rather than shipping as part of
// any single release line.
package main

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/metux/starfleetctl/internal/agentbus"
	"github.com/metux/starfleetctl/internal/dashboard"
	"github.com/metux/starfleetctl/internal/prclaim"
	"github.com/metux/starfleetctl/internal/shipnames"
	"github.com/metux/starfleetctl/internal/withclonelock"
	"github.com/metux/starfleetctl/internal/wscommit"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: starfleetctl <agent-bus|dashboard|pr-claim|ws-commit|ship-names|with-clone-lock> [args…]")
		os.Exit(2)
	}

	// with-clone-lock is deliberately generic (like its bash original): it
	// operates on whatever git working tree the CALLER's cwd is in — an
	// xserver agent clone, a driver clone, anywhere — not just
	// mpbt-workspace, so it must NOT go through workspaceRoot()'s
	// AGENTS.md+scripts/ discovery (which would fail outside this checkout).
	if os.Args[1] == "with-clone-lock" {
		dir, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, "starfleetctl:", err)
			os.Exit(1)
		}
		os.Exit(withclonelock.Run(dir, os.Args[2:]))
	}

	root, err := workspaceRoot()
	if err != nil {
		fmt.Fprintln(os.Stderr, "starfleetctl:", err)
		os.Exit(1)
	}

	switch os.Args[1] {
	case "agent-bus":
		os.Exit(agentbus.Run(root, os.Args[2:]))
	case "dashboard":
		os.Exit(dashboard.Run(root, os.Args[2:]))
	case "pr-claim":
		os.Exit(prclaim.Run(root, os.Args[2:]))
	case "ws-commit":
		os.Exit(wscommit.Run(root, os.Args[2:]))
	case "ship-names":
		os.Exit(shipnames.Run(root, os.Args[2:]))
	default:
		fmt.Fprintf(os.Stderr, "starfleetctl: unknown subcommand: %s\n", os.Args[1])
		os.Exit(2)
	}
}

// workspaceRoot resolves the mpbt-workspace root: MPBT_WORKSPACE_ROOT if set
// (e.g. for an installed binary that no longer lives under any checkout),
// otherwise walk up from the current directory looking for the same
// landmarks a human would (AGENTS.md + scripts/), so `starfleetctl` behaves
// sensibly run from the repo root or any subdirectory — unlike the bash
// scripts (which resolve relative to their own on-disk path via
// dirname "$0"), an installed Go binary has no fixed path relative to the
// checkout, so cwd discovery is the more robust equivalent here.
func workspaceRoot() (string, error) {
	if r := os.Getenv("MPBT_WORKSPACE_ROOT"); r != "" {
		return r, nil
	}
	dir, err := os.Getwd()
	if err != nil {
		return "", err
	}
	for {
		if isFile(filepath.Join(dir, "AGENTS.md")) && isDir(filepath.Join(dir, "scripts")) {
			return dir, nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not locate mpbt-workspace root (no AGENTS.md + scripts/ found walking up from cwd); set MPBT_WORKSPACE_ROOT")
		}
		dir = parent
	}
}

func isFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}
