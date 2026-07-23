// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package main

import (
	"fmt"
	"os"

	"github.com/metux/starfleetctl/internal/ghpr"
	"github.com/metux/starfleetctl/internal/prclaim"
)

// RunGithub dispatches the `starfleetctl github …` command group, which
// subgroups all GitHub-interaction subcommands that were previously exposed as
// flat `pr-*`, `ci-*`, `backport-*`, `show-*` commands. The flat names remain
// supported as aliases (see main.go's delegating cases) so existing skills and
// scripts keep working during the transition.
//
// Structure:
//
//	starfleetctl github pr       view|ci|job-logs|comment|label|request-reviewers|
//	                             set-body|append-body|amend-push|checkout|claim|
//	                             show-branch-file|show-conflict|mk-agent-clone|make
//	starfleetctl github ci       cancel-stale|prune
//	starfleetctl github backport applies|commit
//	starfleetctl github issue    (not yet wired)
//	starfleetctl github release   (not yet wired)
func RunGithub(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		printGithubHelp()
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	switch args[0] {
	case "pr":
		return runGithubPR(args[1:])
	case "ci":
		return runGithubCI(args[1:])
	case "backport":
		return runGithubBackport(args[1:])
	case "issue":
		return runGithubIssue(args[1:])
	case "release":
		return runGithubRelease(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "github: unknown group: %s\n\n", args[0])
		printGithubHelp()
		return 2
	}
}

// runGithubPR dispatches `starfleetctl github pr <verb>`.
func runGithubPR(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		printGithubPRHelp()
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	verb := args[0]
	rest := args[1:]

	// Verbs that operate on the workspace need the resolved root.
	rootVerbs := map[string]bool{"checkout": true, "claim": true, "mk-agent-clone": true}
	if rootVerbs[verb] {
		root, err := workspaceRoot()
		if err != nil {
			fmt.Fprintln(os.Stderr, "github:", err)
			return 1
		}
		switch verb {
		case "checkout":
			return ghpr.RunPRCheckout(root, rest)
		case "claim":
			return prclaim.Run(root, rest)
		case "mk-agent-clone":
			return ghpr.RunMkAgentClone(root, rest)
		}
	}

	// `make` (formerly xx-make-pr) operates on cwd, like the bash original.
	if verb == "make" {
		dir, err := os.Getwd()
		if err != nil {
			fmt.Fprintln(os.Stderr, "github:", err)
			return 1
		}
		return ghpr.RunXXMakePR(dir, rest)
	}

	// Stateless `gh` wrappers — work from any cwd.
	switch verb {
	case "view":
		return ghpr.RunPRView(rest)
	case "ci":
		return ghpr.RunPRCi(rest)
	case "job-logs":
		return ghpr.RunPRJobLogs(rest)
	case "comment":
		return ghpr.RunPRComment(rest)
	case "label":
		return ghpr.RunPRLabel(rest)
	case "request-reviewers":
		return ghpr.RunPRRequestReviewers(rest)
	case "set-body":
		return ghpr.RunPRSetBody(rest)
	case "append-body":
		return ghpr.RunPRAppendBody(rest)
	case "amend-push":
		return ghpr.RunPRAmendPush(rest)
	case "show-branch-file":
		return ghpr.RunShowBranchFile(rest)
	case "show-conflict":
		return ghpr.RunShowPRConflict(rest)
	default:
		fmt.Fprintf(os.Stderr, "github pr: unknown verb: %s\n\n", verb)
		printGithubPRHelp()
		return 2
	}
}

// runGithubCI dispatches `starfleetctl github ci <verb>`.
func runGithubCI(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		printGithubCIHelp()
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	switch args[0] {
	case "cancel-stale":
		return ghpr.RunCICancelStale(args[1:])
	case "prune":
		return ghpr.RunCIPrune(args[1:])
	default:
		fmt.Fprintf(os.Stderr, "github ci: unknown verb: %s\n\n", args[0])
		printGithubCIHelp()
		return 2
	}
}

// runGithubBackport dispatches `starfleetctl github backport <verb>`.
func runGithubBackport(args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		printGithubBackportHelp()
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	verb := args[0]
	rest := args[1:]
	if verb == "commit" {
		root, err := workspaceRoot()
		if err != nil {
			fmt.Fprintln(os.Stderr, "github:", err)
			return 1
		}
		return ghpr.RunBackportCommit(root, rest)
	}
	if verb == "applies" {
		return ghpr.RunBackportApplies(rest)
	}
	fmt.Fprintf(os.Stderr, "github backport: unknown verb: %s\n\n", verb)
	printGithubBackportHelp()
	return 2
}

// runGithubIssue is a structural placeholder — the `github issue …` group does
// not have a backend yet. It exists so the command tree matches the intended
// shape (see DASHBOARD.md task); wiring it up is future work.
func runGithubIssue(args []string) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Print(githubIssueHelp)
		return 0
	}
	fmt.Fprintln(os.Stderr, "github issue: not yet wired — no implementation yet (tracked in DASHBOARD.md)")
	return 2
}

// runGithubRelease mirrors runGithubIssue for the `github release …` group.
func runGithubRelease(args []string) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Print(githubReleaseHelp)
		return 0
	}
	fmt.Fprintln(os.Stderr, "github release: not yet wired — no implementation yet (tracked in DASHBOARD.md)")
	return 2
}

const githubHelp = `github — GitHub-interaction command group (subgroups the former flat pr-*/ci-*/backport-* commands)

Usage: starfleetctl github <group> [args...]

Groups:
  pr        Pull request operations (view, ci, comment, label, checkout, claim, …)
  ci        CI run management (cancel-stale, prune)
  backport  release-branch backports (applies, commit)
  issue     (not yet wired)
  release   (not yet wired)

Run 'starfleetctl github <group> --help' for group-specific help.
The legacy flat names (pr-view, pr-ci, pr-claim, backport-applies, ci-cancel-stale,
xx-make-pr, …) remain available as aliases for now.
`

const githubPRHelp = `github pr — pull request operations

Usage: starfleetctl github pr <verb> [args...]

Verbs:
  view              view a pull request
  ci                show PR CI status
  job-logs          fetch CI logs for failing jobs of a PR
  comment           comment on a PR
  label             add/remove PR labels
  request-reviewers request PR reviewers
  set-body          set PR body text
  append-body       append text to PR body
  amend-push        amend and force-push a PR branch
  checkout          checkout a PR into a ship clone (needs workspace)
  claim             claim/unclaim a PR (needs workspace)
  show-branch-file  show a file from a branch
  show-conflict     show merge conflict details for a PR
  mk-agent-clone    create an isolated ship worktree clone (needs workspace)
  make              create a PR with commit-message conventions (formerly xx-make-pr)

Run 'starfleetctl github pr <verb> --help' for verb-specific help.
`

const githubCIHelp = `github ci — CI run management

Usage: starfleetctl github ci <verb> [args...]

Verbs:
  cancel-stale   cancel still-running superseded CI runs
  prune          delete completed stale CI runs
`

const githubBackportHelp = `github backport — release-branch backports

Usage: starfleetctl github backport <verb> [args...]

Verbs:
  applies   check if a commit applies to a release branch
  commit    backport a commit to a release branch (needs workspace)
`

const githubIssueHelp = `github issue — (not yet wired)

No implementation yet. Tracked in DASHBOARD.md.
`

const githubReleaseHelp = `github release — (not yet wired)

No implementation yet. Tracked in DASHBOARD.md.
`

func printGithubHelp()         { fmt.Print(githubHelp) }
func printGithubPRHelp()       { fmt.Print(githubPRHelp) }
func printGithubCIHelp()       { fmt.Print(githubCIHelp) }
func printGithubBackportHelp() { fmt.Print(githubBackportHelp) }
