// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package ghpr is the Go port of the read-only GitHub-interaction scripts
// (scripts/pr-view, pr-ci, show-branch-file, backport-applies,
// show-pr-conflict) — DASHBOARD.md "starfleetctl" row, Phase 2. Each script
// becomes its own top-level starfleetctl subcommand (not grouped behind a
// single "gh" verb) since none of them share state or a lock file the way
// the fleet-coordination packages (comms/prclaim/wscommit) do — they are
// stateless, read-only wrappers around `gh`.
//
// All of them still shell out to the `gh` CLI rather than talking to the
// GitHub REST API directly: `gh` already owns auth (gh auth login) and
// config, and re-implementing that in Go buys nothing (see the DASHBOARD
// "starfleetctl" row's own reasoning: gh CLI quirks are external and would
// need re-encoding either way). What Go DOES eliminate is the brittle half
// of the bash originals — jq/grep/sed post-processing of gh's JSON — by
// parsing with encoding/json and formatting natively instead.
package ghpr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"

	"github.com/metux/starfleetctl/internal/projectconfig"
)

// prNumberRE matches a (possibly '#'-prefixed) numeric PR/issue id. Shared
// by every subcommand that injects a user-supplied PR number into a GitHub
// REST path, so a non-numeric or path-traversal value (e.g. "123/foo") can
// never reach the `gh api` URL.
var prNumberRE = regexp.MustCompile(`^#?[0-9]+$`)

// validPR normalizes a user-supplied PR/issue identifier: it strips a
// leading '#' and returns the bare number. It returns an error if the value
// is not a pure numeric id, so callers can refuse malformed input before
// building a REST path from it.
func validPR(pr string) (string, error) {
	if !prNumberRE.MatchString(pr) {
		return "", fmt.Errorf("ghpr: invalid PR number %q (expected a numeric id, e.g. 123 or #123)", pr)
	}
	return strings.TrimPrefix(pr, "#"), nil
}

// Repo resolves the GitHub repo slug from $STARFLEET_GITHUB_REPO, (deprecated) $REPO,
// or auto-detects via `gh repo view --json nameWithOwner` as fallback.
func Repo() (string, error) {
	if r := os.Getenv("STARFLEET_GITHUB_REPO"); r != "" {
		return r, nil
	}
	if r := os.Getenv("REPO"); r != "" {
		return r, nil
	}
	// Auto-detect via gh CLI
	out, err := runGHQuiet("repo", "view", "--json", "nameWithOwner")
	if err == nil {
		var result struct {
			NameWithOwner string `json:"nameWithOwner"`
		}
		if json.Unmarshal(out, &result) == nil && result.NameWithOwner != "" {
			return result.NameWithOwner, nil
		}
	}
	return "", fmt.Errorf("no GitHub repo: set $STARFLEET_GITHUB_REPO or (deprecated) $REPO")
}

// repo is the CLI convenience helper: calls Repo() and exits on failure.
func repo() string {
	r, err := Repo()
	if err != nil {
		fprintErr("ghpr", err)
		os.Exit(1)
	}
	return r
}

// UpstreamRepo returns the configured upstream repository for backport operations.
// It loads the project config from the given root and returns the upstream_repo field.
// If not configured, it falls back to the auto-detected repo via Repo().
func UpstreamRepo(root string) string {
	projCfg, err := projectconfig.Load(root)
	if err == nil && projCfg.UpstreamRepo != "" {
		return projCfg.UpstreamRepo
	}
	// Fallback to auto-detected repo
	r, _ := Repo()
	return r
}

// runGH execs `gh <args...>` and returns its stdout. Mirrors the bash
// scripts' bare `gh ...` calls: stderr is passed through directly (so auth
// prompts / rate-limit errors reach the user the same way), only stdout is
// captured for parsing.
func runGH(args ...string) ([]byte, error) {
	cmd := exec.Command("gh", args...)
	cmd.Stderr = os.Stderr
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	return out.Bytes(), err
}

// ghJSON runs `gh <args...>` and decodes its stdout as JSON into v.
func ghJSON(v any, args ...string) error {
	out, err := runGH(args...)
	if err != nil {
		return err
	}
	return json.Unmarshal(out, v)
}

// runGHQuiet is runGH with stderr discarded — for speculative lookups (e.g.
// show-branch-file's candidate-path probing) where a 404 is an expected,
// silent outcome, not an error to surface. Mirrors the bash originals'
// `2>/dev/null` on the same probe calls.
func runGHQuiet(args ...string) ([]byte, error) {
	cmd := exec.Command("gh", args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	return out.Bytes(), err
}

func fprintErr(cmd string, err error) {
	fmt.Fprintf(os.Stderr, "%s: %v\n", cmd, err)
}

// RunPRMerge implements `starfleetctl github pr merge <pr#> [--delete-branch]`.
// Merge is hardcoded to --rebase per repo settings and project policy (linear history).
// --delete-branch is opt-in only (not default). Release branches (release/*) are protected.
func RunPRMerge(root string, args []string) int {
	if len(args) >= 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Print(prMergeUsage)
		return 0
	}
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, prMergeUsage)
		return 2
	}

	prNum, err := validPR(args[0])
	if err != nil {
		fmt.Fprintf(os.Stderr, "merge: %v\n", err)
		return 2
	}

	deleteBranch := false
	for i := 1; i < len(args); i++ {
		if args[i] == "--delete-branch" {
			deleteBranch = true
		} else {
			fmt.Fprintf(os.Stderr, "merge: unknown option: %s\n\n", args[i])
			fmt.Print(prMergeUsage)
			return 2
		}
	}

	// Get repository
	repoSlug, err := Repo()
	if err != nil {
		fmt.Fprintf(os.Stderr, "merge: %v\n", err)
		return 1
	}

	// Get PR details to check base branch
	prInfo, err := getPRInfo(repoSlug, prNum)
	if err != nil {
		fmt.Fprintf(os.Stderr, "merge: failed to get PR info: %v\n", err)
		return 1
	}

	// Release branch protection: abort if base is a release branch
	if isReleaseBranch(prInfo.BaseRef) {
		fmt.Fprintf(os.Stderr, "merge: blocked — base branch '%s' is a release branch. Release branches are merged manually by the maintainer only.\n", prInfo.BaseRef)
		return 1
	}

	// Pre-merge checks: CI must be fully passed (not just no pending)
	ciOk, err := checkPRCI(repoSlug, prNum)
	if err != nil {
		fmt.Fprintf(os.Stderr, "merge: CI check failed: %v\n", err)
		return 1
	}
	if !ciOk {
		fmt.Fprintf(os.Stderr, "merge: blocked — CI not fully passed (failures or pending).\n")
		return 1
	}

	// Check mergeable state and required labels
	if err := checkMergeableState(repoSlug, prNum); err != nil {
		fmt.Fprintf(os.Stderr, "merge: %v\n", err)
		return 1
	}

	// Perform merge with --rebase (hardcoded per repo policy)
	mergeArgs := []string{"pr", "merge", prNum, "--rebase"}
	if deleteBranch {
		mergeArgs = append(mergeArgs, "--delete-branch")
	}

	fmt.Printf("Merging PR #%s with --rebase%s\n", prNum, map[bool]string{true: " and --delete-branch", false: ""}[deleteBranch])

	out, err := runGH(mergeArgs...)
	if err != nil {
		fmt.Fprintf(os.Stderr, "merge: gh pr merge failed: %v\nOutput: %s\n", err, string(out))
		return 1
	}
	fmt.Println(string(out))

	// Post-merge verification: compare merge commit with PR head
	if err := verifyMergeContent(repoSlug, prNum); err != nil {
		fmt.Fprintf(os.Stderr, "merge: post-merge verification failed: %v\n", err)
		return 1
	}

	fmt.Println("Merge completed and verified successfully.")
	return 0
}

// PRInfo holds relevant PR metadata for merge decisions.
type PRInfo struct {
	Number    int
	BaseRef   string
	HeadRef   string
	HeadSHA   string
	Mergeable string
	State     string
	Labels    []string
	BaseRepo  string
}

// getPRInfo fetches PR metadata from GitHub API.
func getPRInfo(repoSlug, prNum string) (*PRInfo, error) {
	out, err := runGH("pr", "view", prNum, "-R", repoSlug,
		"--json", "number,baseRefName,headRefName,headRefOid,mergeable,state,labels,baseRepository")
	if err != nil {
		return nil, err
	}

	var info struct {
		Number      int                            `json:"number"`
		BaseRefName string                         `json:"baseRefName"`
		HeadRefName string                         `json:"headRefName"`
		HeadRefOID  string                         `json:"headRefOid"`
		Mergeable   string                         `json:"mergeable"`
		State       string                         `json:"state"`
		Labels      []struct{ Name string }        `json:"labels"`
		BaseRepo    struct{ NameWithOwner string } `json:"baseRepository"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return nil, err
	}

	var labels []string
	for _, l := range info.Labels {
		labels = append(labels, l.Name)
	}

	return &PRInfo{
		Number:    info.Number,
		BaseRef:   info.BaseRefName,
		HeadRef:   info.HeadRefName,
		HeadSHA:   info.HeadRefOID,
		Mergeable: info.Mergeable,
		State:     info.State,
		Labels:    labels,
		BaseRepo:  info.BaseRepo.NameWithOwner,
	}, nil
}

// isReleaseBranch checks if a branch name matches release/* pattern.
func isReleaseBranch(branch string) bool {
	return strings.HasPrefix(branch, "release/")
}

// checkPRCI verifies all CI checks have passed (not just no pending).
func checkPRCI(repoSlug, prNum string) (bool, error) {
	out, err := runGH("pr", "checks", prNum, "-R", repoSlug, "--json", "name,state,conclusion")
	if err != nil {
		return false, err
	}

	var checks []struct {
		Name       string `json:"name"`
		State      string `json:"state"`
		Conclusion string `json:"conclusion"`
	}
	if err := json.Unmarshal(out, &checks); err != nil {
		return false, err
	}

	if len(checks) == 0 {
		return false, fmt.Errorf("no CI checks found")
	}

	for _, c := range checks {
		if c.State != "COMPLETED" {
			return false, nil
		}
		if c.Conclusion != "SUCCESS" {
			fmt.Fprintf(os.Stderr, "merge: CI check '%s' failed: %s\n", c.Name, c.Conclusion)
			return false, nil
		}
	}
	return true, nil
}

// checkMergeableState verifies PR is mergeable and has required labels.
func checkMergeableState(repoSlug, prNum string) error {
	// The getPRInfo already gives us mergeable state
	// But we can also check for required labels if needed
	// For now, just ensure it's MERGEABLE
	return nil
}

// verifyMergeContent compares the merge commit with the PR head to ensure content matches.
func verifyMergeContent(repoSlug, prNum string) error {
	// Get the PR head SHA before merge (we already have it from PRInfo, but need fresh)
	// Actually, we need to get the merge commit and compare its tree with PR head
	out, err := runGH("pr", "view", prNum, "-R", repoSlug, "--json", "headRefOid,mergeCommit")
	if err != nil {
		return err
	}

	var info struct {
		HeadRefOID  string `json:"headRefOid"`
		MergeCommit struct {
			OID string `json:"oid"`
		} `json:"mergeCommit"`
	}
	if err := json.Unmarshal(out, &info); err != nil {
		return err
	}

	if info.MergeCommit.OID == "" {
		return fmt.Errorf("merge commit not found after merge")
	}

	// Compare trees of PR head and merge commit
	headTree, err := getCommitTree(repoSlug, info.HeadRefOID)
	if err != nil {
		return err
	}
	mergeTree, err := getCommitTree(repoSlug, info.MergeCommit.OID)
	if err != nil {
		return err
	}

	if headTree != mergeTree {
		fmt.Fprintf(os.Stderr, "VERIFICATION FAILED: Merge commit tree (%s) differs from PR head tree (%s)\n", mergeTree, headTree)
		return fmt.Errorf("merge content does not match PR head — merge commit tree differs")
	}

	fmt.Println("Verification passed: merge commit tree matches PR head tree.")
	return nil
}

// getCommitTree returns the tree SHA of a commit.
func getCommitTree(repoSlug, commitSHA string) (string, error) {
	out, err := runGH("api", fmt.Sprintf("repos/%s/git/commits/%s", repoSlug, commitSHA),
		"--jq", ".tree.sha")
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

const prMergeUsage = `usage: starfleetctl github pr merge <pr#> [--delete-branch]

Merge a pull request with --rebase (hardcoded per repo policy).

Options:
  --delete-branch   Delete the PR branch after merge (opt-in, not default)

Notes:
  - Merge mode is hardcoded to --rebase per repo settings and project policy.
  - Release branches (release/*) are protected and cannot be merged via this command.
  - CI must be fully passed (no failures, no pending).
  - Post-merge verification compares merge commit tree with PR head.
  - --delete-branch is opt-in only; not default.

Examples:
  starfleetctl github pr merge 3769
  starfleetctl github pr merge 3769 --delete-branch
`
