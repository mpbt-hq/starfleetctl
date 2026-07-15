// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Go port of scripts/prune-stale-ci: delete COMPLETED workflow runs that are
// outdated — runs whose head commit is no longer the tip of their branch.
package ghpr

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const pruneStaleCIUsage = `usage: starfleetctl ci-prune [--delete] [--keep-gone] [--verbose]
                     [--branch <name>]... [--workflow <name>]...
  --delete            actually delete (default: dry-run summary only)
  --keep-gone         spare runs whose branch no longer exists (404)
  --verbose, -v       list every stale run, not just the per-branch summary
  --branch <name>     only consider runs on this head branch (repeatable)
  --workflow <name>   only consider runs of this workflow name (repeatable)
env:
  STARFLEET_GITHUB_REPO   repo slug (or $REPO for backward compat)
`

type completedRun struct {
	ID         int    `json:"id"`
	Name       string `json:"name"`
	HeadBranch string `json:"head_branch"`
	HeadSHA    string `json:"head_sha"`
	CreatedAt  string `json:"created_at"`
	Conclusion string `json:"conclusion"`
	HeadRepo   struct {
		FullName string `json:"full_name"`
	} `json:"head_repository"`
}

func RunCIPrune(args []string) int {
	doDelete := false
	keepGone := false
	verbose := false
	var branchFilter, wfFilter []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			fmt.Print(pruneStaleCIUsage)
			return 0
		case "--delete":
			doDelete = true
		case "--keep-gone":
			keepGone = true
		case "--verbose", "-v":
			verbose = true
		case "--branch":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "ci-prune: --branch needs a value")
				return 2
			}
			branchFilter = append(branchFilter, args[i])
		case "--workflow":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "ci-prune: --workflow needs a value")
				return 2
			}
			wfFilter = append(wfFilter, args[i])
		default:
			fmt.Fprintf(os.Stderr, "ci-prune: unknown arg: %s\n", args[i])
			return 2
		}
	}

	repoVal, err := Repo()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ci-prune:", err)
		return 2
	}

	// Collect all completed runs
	seen := map[int]bool{}
	var runs []completedRun
	page := 1
	for {
		out, err := runGHQuiet("api", "--paginate",
			fmt.Sprintf("repos/%s/actions/runs?status=completed&per_page=100&exclude_pull_requests=false&page=%d", repoVal, page))
		if err != nil {
			break
		}
		var resp runsResponse
		if err := json.Unmarshal(out, &resp); err != nil {
			break
		}
		for _, r := range resp.WorkflowRuns {
			if !seen[r.ID] {
				seen[r.ID] = true
				runs = append(runs, completedRun{
					ID: r.ID, Name: r.Name, HeadBranch: r.HeadBranch,
					HeadSHA: r.HeadSHA, CreatedAt: r.CreatedAt,
					HeadRepo: r.HeadRepo,
				})
			}
		}
		if len(resp.WorkflowRuns) < 100 {
			break
		}
		page++
	}

	if len(runs) == 0 {
		fmt.Fprintf(os.Stderr, "ci-prune: no completed CI runs on %s.\n", repoVal)
		return 0
	}

	// Apply filters
	var filtered []completedRun
	for _, r := range runs {
		if len(branchFilter) > 0 && !containsStr(branchFilter, r.HeadBranch) {
			continue
		}
		if len(wfFilter) > 0 && !containsStr(wfFilter, r.Name) {
			continue
		}
		filtered = append(filtered, r)
	}

	// Resolve tips and classify
	type branchInfo struct {
		tip      string
		stale    int
		keep     int
		gone     int
	}
	branchData := map[string]*branchInfo{}
	var delIDs, goneIDs []int

	for _, r := range filtered {
		repo := r.HeadRepo.FullName
		if repo == "" {
			repo = repoVal
		}
		key := repo + "|" + r.HeadBranch
		if branchData[key] == nil {
			branchData[key] = &branchInfo{}
		}
		bi := branchData[key]

		// Resolve tip
		tip := resolveBranchTip(repo, r.HeadBranch, repoVal)
		bi.tip = tip

		if tip == "" {
			// Branch gone (404)
			goneIDs = append(goneIDs, r.ID)
			bi.gone++
			if verbose {
				fmt.Printf("GONE   run %-12s %-22s %-30s %s  (branch 404)\n",
					fmt.Sprintf("%d", r.ID), trunc(r.Name, 22), trunc(r.HeadBranch, 30), trunc(r.HeadSHA, 9))
			}
		} else if tip != r.HeadSHA {
			delIDs = append(delIDs, r.ID)
			bi.stale++
			if verbose {
				fmt.Printf("STALE  run %-12s %-22s %-30s %s -> tip %s (%s)\n",
					fmt.Sprintf("%d", r.ID), trunc(r.Name, 22), trunc(r.HeadBranch, 30),
					trunc(r.HeadSHA, 9), trunc(tip, 9), r.Conclusion)
			}
		} else {
			bi.keep++
		}
	}

	// Summary
	if verbose {
		fmt.Println()
	}
	fmt.Printf("=== ci-prune summary (%s) ===\n", repoVal)
	fmt.Printf("%-32s %6s %6s %6s  %s\n", "branch (head-repo)", "stale", "keep", "gone", "current tip")
	for key, bi := range branchData {
		if bi.stale == 0 && bi.gone == 0 {
			continue
		}
		parts := strings.SplitN(key, "|", 2)
		repo, br := parts[0], parts[1]
		label := br
		if repo != repoVal {
			label = br + " (" + repo + ")"
		}
		tipDisp := trunc(bi.tip, 9)
		if bi.tip == "" {
			tipDisp = "(gone)"
		}
		fmt.Printf("%-32s %6d %6d %6d  %s\n", trunc(label, 32), bi.stale, bi.keep, bi.gone, tipDisp)
	}

	fmt.Printf("\nscanned %d completed run(s): %d stale (branch moved), %d on gone branches, keeping the rest.\n",
		len(filtered), len(delIDs), len(goneIDs))

	targets := delIDs
	if !keepGone {
		targets = append(targets, goneIDs...)
	}

	if len(targets) == 0 {
		fmt.Println("Nothing to delete.")
		return 0
	}

	if !doDelete {
		fmt.Printf("Dry run — nothing deleted. Re-run with --delete to remove the %d run(s) above. (irreversible)\n", len(targets))
		return 0
	}

	// Delete
	ok, fail := 0, 0
	for _, id := range targets {
		idStr := fmt.Sprintf("%d", id)
		_, err := runGH("api", "--method", "DELETE", fmt.Sprintf("repos/%s/actions/runs/%s", repoVal, idStr))
		if err != nil {
			fmt.Fprintf(os.Stderr, "WARN: could not delete run %s\n", idStr)
			fail++
		} else {
			ok++
		}
	}
	fmt.Printf("ci-prune: deleted %d/%d run(s)", ok, len(targets))
	if fail > 0 {
		fmt.Printf(" (%d failed)", fail)
	}
	fmt.Println()
	return 0
}

func resolveBranchTip(repo, branch, fallbackRepo string) string {
	if branch == "" {
		return ""
	}
	r := repo
	if r == "" {
		r = fallbackRepo
	}
	out, err := runGHQuiet("api", fmt.Sprintf("repos/%s/commits/%s", r, branch), "-q", ".sha")
	if err != nil {
		// Check if it's a 404
		errOut := strings.TrimSpace(string(out))
		if strings.Contains(errOut, "404") || strings.Contains(strings.ToLower(errOut), "not found") {
			return ""
		}
		return "" // ambiguous — treat as gone rather than risk misclassification
	}
	return strings.TrimSpace(string(out))
}

func trunc(s string, n int) string {
	if len(s) > n {
		return s[:n]
	}
	return s
}
