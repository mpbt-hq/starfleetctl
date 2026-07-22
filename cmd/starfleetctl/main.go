// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// starfleetctl consolidates the flock/race-prone mpbt-workspace
// fleet-coordination scripts (agent-bus, pr-claim, ws-commit — in that
// order) into one Go CLI, one subcommand per script. See
// mpbt-workspace/DASHBOARD.md ("mpbtctl" row) and .starfleet-ai/agents.d/index.md for the full
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
	"github.com/metux/starfleetctl/internal/hook"
	"github.com/metux/starfleetctl/internal/jsonutil"
	"github.com/metux/starfleetctl/internal/logs"
	"github.com/metux/starfleetctl/internal/models"
	"github.com/metux/starfleetctl/internal/selfinstall"
	"github.com/metux/starfleetctl/internal/session"
	"github.com/metux/starfleetctl/internal/shipnames"
	"github.com/metux/starfleetctl/internal/task"
	"github.com/metux/starfleetctl/internal/timer"
	"github.com/metux/starfleetctl/internal/web"
	"github.com/metux/starfleetctl/internal/telemetry"
	"github.com/metux/starfleetctl/internal/withclonelock"
	"github.com/metux/starfleetctl/internal/worktree"
	"github.com/metux/starfleetctl/internal/wscommit"
)

const helpText = `starfleetctl — fleet-coordination tool for the mpbt-workspace agent-bus.

Usage:  starfleetctl <subcommand> [args...]

Fleet management:
  agent-bus         operate the session bus (read/write/ack/notify/status/health)
  bootstrap         verify/fix workspace structure (dirs, allowlist, fragments)
  bridged           manage bridged agent sessions (exec/status/log)
  dashboard         render the workspace dashboard
  hook              handle agent lifecycle hooks (pre/post)
  run               start an AI agent session (replaces run-opencode/run-claude scripts)
  session           manage agent sessions (list/ship)
  ship-names        assign/release/list ship names
  with-clone-lock   serialize git operations via flock
  worktree          create/list/remove throwaway git worktrees
  ws-commit         commit workspace changes with locking
  task              capture fleet tasks into the dashboard (+ optional ship commission)
  timer             fleet scheduling: one-time, interval, cron (with worker daemon)
  logs              scan ship logs + bus events, extract failures as tasks (feedback loop)
  web               minimalist mobile-first fleet web console (status / tasks / bus / talk)

Bootstrap & setup:
  genesis-init      bootstrap a workspace from nothing (writes starfleet-bootstrap + runs bootstrap --fix)
  self-install      clone/pull starfleetctl source, build, and symlink into .starfleet-ai/bin/
  agents            install/update starfleet agent fragments and skills

GitHub commands (grouped under 'github'):
  github pr          view|ci|job-logs|comment|label|request-reviewers|
                     set-body|append-body|amend-push|checkout|claim|
                     show-branch-file|show-conflict|mk-agent-clone|make
  github ci          cancel-stale|prune
  github backport    applies|commit
  github issue       (not yet wired)
  github release     (not yet wired)
  Legacy flat aliases (pr-view, pr-ci, pr-claim, backport-applies, ci-cancel-stale,
  xx-make-pr, …) still work for now — they delegate into 'github'.

Utilities:
  json              JSON helper (validate/pretty/get) — no python3 needed

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
	// .starfleet-ai/agents.d/index.md+scripts/ discovery (which would fail outside this checkout).
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
	// point — by definition it runs BEFORE .starfleet-ai/agents.d/index.md/scripts/ exist, so it
	// must NOT go through workspaceRoot() either; it takes its target
	// directory as an explicit argument (default cwd) instead.
	if os.Args[1] == "genesis-init" {
		os.Exit(genesis.Run(os.Args[2:]))
	}

	// bootstrap verifies/fixes workspace structure — by definition it may
	// run BEFORE .starfleet-ai/agents.d/index.md exists (that's what it creates), so it must NOT
	// go through workspaceRoot(); cwd is the workspace root.
	if os.Args[1] == "bootstrap" {
		dir, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, "starfleetctl:", err)
			os.Exit(1)
		}
		os.Exit(bootstrap.Run(dir, os.Args[2:]))
	}

	// termctl-run runs a termctl terminal in the foreground (blocking on h.Run()).
// This is meant to be spawned as a child process by `session run`.
// It gets workspace root from MPBT_WORKSPACE_ROOT env var.
	if os.Args[1] == "termctl-run" {
		os.Exit(session.RunTermctl("", os.Args[2:]))
	}

	// json is a stateless utility — no workspace root needed.
	if os.Args[1] == "json" {
		os.Exit(jsonutil.Run(os.Args[2:]))
	}

	// The GitHub-interaction subcommands are now grouped under `github …`
	// (see cmd/starfleetctl/github.go). The legacy flat names below remain as
	// aliases that delegate into that dispatcher, so existing skills/scripts
	// keep working during the transition. Each verb resolves its own root
	// (via workspaceRoot) internally where needed, so they are safe here in
	// the stateless block.
	switch os.Args[1] {
	case "github":
		os.Exit(RunGithub(os.Args[2:]))

	// --- legacy flat aliases (delegate to `github`) ---
	case "pr-view":
		os.Exit(RunGithub(append([]string{"pr", "view"}, os.Args[2:]...)))
	case "pr-ci":
		os.Exit(RunGithub(append([]string{"pr", "ci"}, os.Args[2:]...)))
	case "pr-job-logs":
		os.Exit(RunGithub(append([]string{"pr", "job-logs"}, os.Args[2:]...)))
	case "show-branch-file":
		os.Exit(RunGithub(append([]string{"pr", "show-branch-file"}, os.Args[2:]...)))
	case "backport-applies":
		os.Exit(RunGithub(append([]string{"backport", "applies"}, os.Args[2:]...)))
	case "show-pr-conflict":
		os.Exit(RunGithub(append([]string{"pr", "show-conflict"}, os.Args[2:]...)))
	case "pr-comment":
		os.Exit(RunGithub(append([]string{"pr", "comment"}, os.Args[2:]...)))
	case "pr-label":
		os.Exit(RunGithub(append([]string{"pr", "label"}, os.Args[2:]...)))
	case "pr-request-reviewers":
		os.Exit(RunGithub(append([]string{"pr", "request-reviewers"}, os.Args[2:]...)))
	case "pr-set-body":
		os.Exit(RunGithub(append([]string{"pr", "set-body"}, os.Args[2:]...)))
	case "pr-append-body":
		os.Exit(RunGithub(append([]string{"pr", "append-body"}, os.Args[2:]...)))
	case "pr-amend-push":
		os.Exit(RunGithub(append([]string{"pr", "amend-push"}, os.Args[2:]...)))
	case "ci-cancel-stale":
		os.Exit(RunGithub(append([]string{"ci", "cancel-stale"}, os.Args[2:]...)))
	case "ci-prune":
		os.Exit(RunGithub(append([]string{"ci", "prune"}, os.Args[2:]...)))
	case "xx-make-pr":
		os.Exit(RunGithub(append([]string{"pr", "make"}, os.Args[2:]...)))
	case "pr-claim":
		os.Exit(RunGithub(append([]string{"pr", "claim"}, os.Args[2:]...)))
	case "pr-checkout":
		os.Exit(RunGithub(append([]string{"pr", "checkout"}, os.Args[2:]...)))
	case "backport-commit":
		os.Exit(RunGithub(append([]string{"backport", "commit"}, os.Args[2:]...)))
	case "mk-agent-clone":
		os.Exit(RunGithub(append([]string{"pr", "mk-agent-clone"}, os.Args[2:]...)))
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
	case "ws-commit":
		os.Exit(wscommit.Run(root, os.Args[2:]))
	case "ship-names":
		os.Exit(shipnames.Run(root, os.Args[2:]))
	case "telemetry":
		os.Exit(telemetry.Run(root, os.Args[2:]))
	case "agents":
		os.Exit(agents.Run(root, os.Args[2:]))
	case "bridged":
		os.Exit(bridged.Run(root, os.Args[2:]))
	case "hook":
		os.Exit(hook.Run(root, os.Args[2:]))
	case "session":
		os.Exit(session.Run(root, os.Args[2:]))
	case "run":
		os.Exit(session.RunCmd(root, os.Args[2:]))
  case "worktree":
		os.Exit(worktree.Run(root, os.Args[2:]))
	case "task":
		os.Exit(task.Run(root, os.Args[2:]))
	case "timer":
		os.Exit(timer.Run(root, os.Args[2:]))
	case "web":
		os.Exit(web.Run(root, os.Args[2:]))
	case "logs":
		os.Exit(logs.Run(root, os.Args[2:]))
	case "models":
		if err := models.New(root).Run(os.Args[2:]); err != nil {
			fmt.Fprintln(os.Stderr, "models:", err)
			os.Exit(1)
		}
	default:
		fmt.Fprintf(os.Stderr, "starfleetctl: unknown subcommand: %s\n", os.Args[1])
		os.Exit(2)
	}
}

// workspaceRoot resolves the mpbt-workspace root: MPBT_WORKSPACE_ROOT if set
// (e.g. for an installed binary that no longer lives under any checkout),
// otherwise walk up from the current directory looking for the same
// landmarks a human would (.starfleet-ai/agents.d/index.md + scripts/), so `starfleetctl` behaves
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
		if isDir(filepath.Join(dir, ".starfleet-ai")) && isDir(filepath.Join(dir, "scripts")) {
			return dir, nil
		}
		// Don't walk past a git repo/worktree boundary — UNLESS we're inside
		// a .starfleet-ai/ subdirectory, which means this is the starfleetctl
		// source checkout nested inside the workspace. In that case the real
		// workspace root is further up and we must keep walking.
		if exists(filepath.Join(dir, ".git")) {
			inStarfleetAI := false
			for p := dir; p != filepath.Dir(p); p = filepath.Dir(p) {
				if filepath.Base(p) == ".starfleet-ai" {
					inStarfleetAI = true
					break
				}
			}
			if !inStarfleetAI {
				return "", fmt.Errorf("in a git working tree (%s) with no .starfleet-ai/ + scripts/ — refusing to search further up past this repo/worktree boundary; set MPBT_WORKSPACE_ROOT if this is intentional", dir)
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", fmt.Errorf("could not locate mpbt-workspace root (no .starfleet-ai/ + scripts/ found walking up from cwd); set MPBT_WORKSPACE_ROOT")
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
