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
	"github.com/metux/starfleetctl/internal/agents"
	"github.com/metux/starfleetctl/internal/bootstrap"
	"github.com/metux/starfleetctl/internal/bridged"
	"github.com/metux/starfleetctl/internal/dashboard"
	"github.com/metux/starfleetctl/internal/genesis"
	"github.com/metux/starfleetctl/internal/ghpr"
	"github.com/metux/starfleetctl/internal/hook"
	"github.com/metux/starfleetctl/internal/prclaim"
	"github.com/metux/starfleetctl/internal/selfinstall"
	"github.com/metux/starfleetctl/internal/shipnames"
	"github.com/metux/starfleetctl/internal/session"
	"github.com/metux/starfleetctl/internal/withclonelock"
	"github.com/metux/starfleetctl/internal/worktree"
	"github.com/metux/starfleetctl/internal/wscommit"
)

const helpText = `starfleetctl — fleet-coordination tool for the mpbt-workspace agent-bus.

Usage:  starfleetctl <subcommand> [args...]

Fleet management:
  agent-bus         operate the session bus (read/write/ack/notify/status)
  bootstrap         verify/fix workspace structure (dirs, allowlist, fragments)
  bridged           manage bridged agent sessions (exec/status/log)
  dashboard         render the workspace dashboard
  hook              handle agent lifecycle hooks (pre/post)
  session           manage agent sessions (list/ship)
  ship-names        assign/release/list ship names
  with-clone-lock   serialize git operations via flock
  worktree          create/list/remove throwaway git worktrees
  ws-commit         commit workspace changes with locking

Bootstrap & setup:
  genesis-init      bootstrap a workspace from nothing (writes starfleet-bootstrap + runs bootstrap --fix)
  self-install      clone/pull starfleetctl source, build, and symlink into .starfleet-ai/bin/
  agents            install/update starfleet agent fragments and skills

GitHub PR commands:
  pr-view             view a pull request
  pr-ci               show PR CI status
  pr-comment          comment on a PR
  pr-label            add/remove PR labels
  pr-request-reviewers request PR reviewers
  pr-set-body         set PR body text
  pr-append-body      append text to PR body
  pr-checkout         checkout a PR into an agent clone
  pr-amend-push       amend and force-push a PR branch
  pr-claim            claim/unclaim a PR
  show-branch-file    show a file from a branch
  show-pr-conflict    show merge conflict details for a PR
  backport-applies    check if a commit applies to a release branch
  backport-commit     backport a commit to a release branch
  xx-make-pr          create a PR with commit-message conventions
  mk-agent-clone      create an isolated agent worktree clone

Run 'starfleetctl <subcommand> --help' for subcommand-specific help.
`

func printHelp() {
	fmt.Print(helpText)
}

func main() {
	if len(os.Args) < 2 {
		printHelp()
		os.Exit(2)
	}
	switch os.Args[1] {
	case "-h", "--help":
		printHelp()
		return
	}

	// with-clone-lock operates on whatever git working tree the CALLER's cwd
	// is in — an agent clone, a driver clone, anywhere — not just
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

	// self-install clones/pulls starfleetctl source, builds it, and
	// symlinks into .starfleet-ai/bin/ — works from cwd, no workspace
	// root needed (useful for updates, and for the bootstrap script).
	if os.Args[1] == "self-install" {
		dir, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, "starfleetctl:", err)
			os.Exit(1)
		}
		os.Exit(selfinstall.Run(dir, os.Args[2:]))
	}

	// genesis-init is the "stand up the whole fleet from nothing" entry
	// point — by definition it runs BEFORE AGENTS.md/scripts/ exist, so it
	// must NOT go through workspaceRoot() either; it takes its target
	// directory as an explicit argument (default cwd) instead.
	if os.Args[1] == "genesis-init" {
		os.Exit(genesis.Run(os.Args[2:]))
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
	case "agents":
		os.Exit(agents.Run(root, os.Args[2:]))
	case "mk-agent-clone":
		os.Exit(ghpr.RunMkAgentClone(root, os.Args[2:]))
	case "bridged":
		os.Exit(bridged.Run(root, os.Args[2:]))
	case "hook":
		os.Exit(hook.Run(root, os.Args[2:]))
	case "session":
		os.Exit(session.Run(root, os.Args[2:]))
	case "worktree":
		os.Exit(worktree.Run(root, os.Args[2:]))
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
		// Don't walk past a git repo/worktree boundary. A directory with its
		// own .git (a directory for a normal repo, a file for a linked
		// worktree) that lacks AGENTS.md + scripts/ is NOT this workspace —
		// continuing upward risks silently escaping into an unrelated outer
		// checkout. Concretely hit 2026-07-06: a worktree of THIS repo under
		// _WORK_/worktrees/mpbt-workspace/<name>/ (nested inside the real
		// checkout) had its AGENTS.md removed mid-migration; without this
		// guard, the walk continued past the worktree's own .git file and
		// resolved to the outer shared checkout instead, which then nearly
		// received a write meant for the isolated worktree.
		if exists(filepath.Join(dir, ".git")) {
			return "", fmt.Errorf("in a git working tree (%s) with no AGENTS.md + scripts/ — refusing to search further up past this repo/worktree boundary; set MPBT_WORKSPACE_ROOT if this is intentional", dir)
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not locate mpbt-workspace root (no AGENTS.md + scripts/ found walking up from cwd); set MPBT_WORKSPACE_ROOT")
		}
		dir = parent
	}
}

func exists(path string) bool {
	_, err := os.Stat(path)
	return err == nil
}

func isFile(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && !fi.IsDir()
}

func isDir(path string) bool {
	fi, err := os.Stat(path)
	return err == nil && fi.IsDir()
}
