// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Go port of scripts/pr-checkout: check out an open PR's head branch into an
// isolated, object-shared agent clone, ready to edit (pairs with
// pr-amend-push). Needs the workspace root (unlike the stateless ghpr
// subcommands) to locate the agent-clone tree, so it's wired through the
// workspaceRoot() dispatch group in main.go, not the any-cwd one.
package ghpr

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
)

const prCheckoutUsage = `usage: starfleetctl pr-checkout <pr#> [agent-name]
agent-name defaults to "repair". Prints the clone dir on stdout on success;
progress goes to stderr.
`

// RunPRCheckout implements `starfleetctl pr-checkout`.
func RunPRCheckout(root string, args []string) int {
	if len(args) >= 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Print(prCheckoutUsage)
		return 0
	}
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, prCheckoutUsage)
		return 2
	}
	pr, err := validPR(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	name := "repair"
	if len(args) >= 2 {
		name = args[1]
	}
	dest := filepath.Join(root, "_WORK_", "xserver-master", "agent", name, "xserver")

	// Advisory cross-agent check (non-fatal): warn if another agent already
	// claimed this PR. Re-invokes our own `pr-claim --who` subcommand
	// (already the preferred, cut-over implementation) rather than
	// duplicating its claim-file-reading logic here.
	if holder := prClaimWho(pr); holder != "" {
		fmt.Fprintf(os.Stderr, "pr-checkout: WARNING: PR #%s is already claimed by another agent: %s\n", pr, holder)
		fmt.Fprintf(os.Stderr, "pr-checkout: coordinate, pick another PR, or 'starfleetctl pr-claim --steal %s' if that agent is gone.\n", pr)
	}

	// isolated master clone (object-shared); EnsureAgentClone's own `fetch
	// origin --prune` already pulls every origin branch, including the PR
	// head branch. Progress -> stderr, matching the bash original's `>&2`
	// on this call (pr-checkout's own stdout contract is just the final
	// clone dir, for command-substitution capture).
	if _, err := EnsureAgentClone(root, "master", name, os.Stderr); err != nil {
		fprintErr("pr-checkout", err)
		return 1
	}

	originURL := gitConfigGet(dest, "remote.origin.url")
	repoSlug := parseGitHubRepoSlug(originURL)

	tsv, err := runGH("pr", "view", pr, "-R", repoSlug,
		"--json", "headRefName,isCrossRepository,headRepositoryOwner,headRepository,maintainerCanModify",
		"--jq", `[.headRefName, .isCrossRepository, .headRepositoryOwner.login, .headRepository.name, .maintainerCanModify] | @tsv`)
	if err != nil {
		fprintErr("pr-checkout", err)
		return 1
	}
	fields := strings.Split(trimTrailingNewline(string(tsv)), "\t")
	for len(fields) < 5 {
		fields = append(fields, "")
	}
	br, cross, fowner, frepo, mcm := fields[0], fields[1], fields[2], fields[3], fields[4]
	if br == "" {
		fmt.Fprintf(os.Stderr, "pr-checkout: cannot resolve head branch for PR #%s\n", pr)
		return 1
	}

	var pushRemote string
	if cross == "true" {
		forkURL := rewriteRepoSlugInURL(originURL, fowner, frepo)
		fmt.Fprintf(os.Stderr, "pr-checkout: PR #%s is from fork %s/%s (branch '%s') -> %s\n", pr, fowner, frepo, br, forkURL)
		if mcm != "true" {
			fmt.Fprintf(os.Stderr, "pr-checkout: WARNING: maintainerCanModify is '%s' — push-back to the fork branch may be rejected.\n", mcm)
		}
		_ = gitRunSilent(dest, "remote", "remove", "fork")
		if err := gitRunErr(dest, "remote", "add", "fork", forkURL); err != nil {
			fprintErr("pr-checkout", err)
			return 1
		}
		if err := gitRunErr(dest, "fetch", "fork", br); err != nil {
			fprintErr("pr-checkout", err)
			return 1
		}
		if err := gitRunErr(dest, "checkout", "-B", br, "fork/"+br); err != nil {
			fprintErr("pr-checkout", err)
			return 1
		}
		pushRemote = "fork"
	} else {
		if err := gitRunErr(dest, "fetch", "origin", br); err != nil {
			fprintErr("pr-checkout", err)
			return 1
		}
		if err := gitRunErr(dest, "checkout", "-B", br, "origin/"+br); err != nil {
			fprintErr("pr-checkout", err)
			return 1
		}
		pushRemote = "origin"
	}

	fmt.Fprintf(os.Stderr, "pr-checkout: PR #%s head branch '%s' checked out (push remote: %s).\n", pr, br, pushRemote)
	fmt.Fprintln(os.Stderr, "  edit files in the clone, then:")
	fmt.Fprintf(os.Stderr, "    starfleetctl pr-amend-push %s [files...]\n", dest)
	fmt.Println(dest)
	return 0
}

// prClaimWho re-invokes our own `pr-claim --who <pr>` subcommand and returns
// its stdout (the holder line), or "" if unclaimed/stale/an error occurred —
// mirrors bash's `pr-claim --who "$PR" 2>/dev/null || true`.
func prClaimWho(pr string) string {
	self, err := os.Executable()
	if err != nil {
		return ""
	}
	cmd := exec.Command(self, "pr-claim", "--who", pr)
	out, _ := cmd.Output()
	return trimTrailingNewline(string(out))
}

var githubURLPrefix = regexp.MustCompile(`^(git@github\.com:|https://github\.com/)`)

// parseGitHubRepoSlug mirrors bash's
// `sed -E 's#(git@github.com:|https://github.com/)##; s#\.git$##'`.
func parseGitHubRepoSlug(url string) string {
	s := githubURLPrefix.ReplaceAllString(url, "")
	s = strings.TrimSuffix(s, ".git")
	return s
}

var trailingOwnerRepo = regexp.MustCompile(`[^/:]+/[^/]+(\.git)?$`)

// rewriteRepoSlugInURL mirrors bash's
// `sed -E "s#[^/:]+/[^/]+(\.git)?\$#$FOWNER/$FREPO.git#"` — replaces the
// trailing owner/repo(.git)? segment of a git remote URL with the fork's.
func rewriteRepoSlugInURL(url, owner, repoName string) string {
	return trailingOwnerRepo.ReplaceAllString(url, owner+"/"+repoName+".git")
}
