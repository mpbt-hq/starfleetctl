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
	"github.com/metux/starfleetctl/internal/bootstrap"
	"github.com/metux/starfleetctl/internal/dashboard"
	"github.com/metux/starfleetctl/internal/ghpr"
	"github.com/metux/starfleetctl/internal/prclaim"
	"github.com/metux/starfleetctl/internal/shipnames"
	"github.com/metux/starfleetctl/internal/withclonelock"
	"github.com/metux/starfleetctl/internal/wscommit"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: starfleetctl <agent-bus|dashboard|pr-claim|ws-commit|ship-names|with-clone-lock|bootstrap|pr-view|pr-ci|show-branch-file|backport-applies|show-pr-conflict|pr-comment|pr-label|pr-request-reviewers|pr-set-body|pr-append-body|pr-checkout|pr-amend-push|backport-commit|xx-make-pr> [args…]")
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

	// The ghpr (GitHub-interaction) subcommands are likewise standalone —
	// stateless `gh` wrappers with no fleet-file/lock dependency on this
	// checkout, so they must work from any cwd too, same reasoning as
	// with-clone-lock above.
	switch os.Args[1] {
	case "pr-view":
		os.Exit(ghpr.RunPRView(os.Args[2:]))
	case "pr-ci":
		os.Exit(ghpr.RunPRCi(os.Args[2:]))
	case "show-branch-file":
		os.Exit(ghpr.RunShowBranchFile(os.Args[2:]))
	case "backport-applies":
		os.Exit(ghpr.RunBackportApplies(os.Args[2:]))
	case "show-pr-conflict":
		os.Exit(ghpr.RunShowPRConflict(os.Args[2:]))

	// Phase 2 mutating subset (DASHBOARD.md "starfleetctl" row) — same
	// stateless-wrapper reasoning as the read-only group above: pr-comment,
	// pr-label, pr-request-reviewers, pr-set-body and pr-append-body are
	// pure `gh api`/`gh pr` wrappers with no fleet-file dependency;
	// pr-amend-push operates entirely on a clone dir given as an argument.
	// NOT cut over to "preferred" anywhere (AGENTS.md/.claude/settings.json
	// unchanged) — parity-proving only this session, per the standing rule
	// that a mutating command needs an explicit praetor go-ahead first.
	case "pr-comment":
		os.Exit(ghpr.RunPRComment(os.Args[2:]))
	case "pr-label":
		os.Exit(ghpr.RunPRLabel(os.Args[2:]))
	case "pr-request-reviewers":
		os.Exit(ghpr.RunPRRequestReviewers(os.Args[2:]))
	case "pr-set-body":
		os.Exit(ghpr.RunPRSetBody(os.Args[2:]))
	case "pr-append-body":
		os.Exit(ghpr.RunPRAppendBody(os.Args[2:]))
	case "pr-amend-push":
		os.Exit(ghpr.RunPRAmendPush(os.Args[2:]))
	case "xx-make-pr":
		// Standalone invocation operates on cwd, exactly like the bash
		// script (which reads make-pr.* from `git config` in whatever
		// directory it's run from). backport-commit below calls
		// ghpr.RunXXMakePR directly with an explicit clone dir instead of
		// going through this cwd-based path.
		dir, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, "starfleetctl:", err)
			os.Exit(1)
		}
		os.Exit(ghpr.RunXXMakePR(dir, os.Args[2:]))
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
	case "pr-checkout":
		os.Exit(ghpr.RunPRCheckout(root, os.Args[2:]))
	case "backport-commit":
		os.Exit(ghpr.RunBackportCommit(root, os.Args[2:]))
	case "bootstrap":
		os.Exit(bootstrap.Run(root, os.Args[2:]))
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
