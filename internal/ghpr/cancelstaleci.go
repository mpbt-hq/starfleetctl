// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Go port of scripts/cancel-stale-ci: cancel still-running CI runs that have
// been superseded (their branch moved on since the run started).
package ghpr

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

const cancelStaleCIUsage = `usage: starfleetctl ci-cancel-stale [--cancel|-y] [--branch <name>]... [--workflow <name>]...
  --cancel, -y        actually cancel (default: dry-run preview only)
  --branch <name>     only consider runs on this head branch (repeatable)
  --workflow <name>   only consider runs of this workflow name (repeatable)
env:
  STARFLEET_GITHUB_REPO   repo slug (or $REPO for backward compat)
`

type actionRun struct {
	ID          int    `json:"id"`
	Name        string `json:"name"`
	HeadBranch  string `json:"head_branch"`
	HeadSHA     string `json:"head_sha"`
	CreatedAt   string `json:"created_at"`
	Event       string `json:"event"`
	HTMLURL     string `json:"html_url"`
	DisplayTitle string `json:"display_title"`
	HeadRepo    struct {
		FullName string `json:"full_name"`
	} `json:"head_repository"`
}

type runsResponse struct {
	WorkflowRuns []actionRun `json:"workflow_runs"`
}

func RunCICancelStale(args []string) int {
	doCancel := false
	var branchFilter, wfFilter []string

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			fmt.Print(cancelStaleCIUsage)
			return 0
		case "--cancel", "-y":
			doCancel = true
		case "--branch":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "ci-cancel-stale: --branch needs a value")
				return 2
			}
			branchFilter = append(branchFilter, args[i])
		case "--workflow":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "ci-cancel-stale: --workflow needs a value")
				return 2
			}
			wfFilter = append(wfFilter, args[i])
		default:
			fmt.Fprintf(os.Stderr, "ci-cancel-stale: unknown arg: %s\n", args[i])
			return 2
		}
	}

	repoVal, err := Repo()
	if err != nil {
		fmt.Fprintln(os.Stderr, "ci-cancel-stale:", err)
		return 2
	}

	// Collect all active runs
	activeStates := []string{"queued", "in_progress", "waiting", "requested", "pending"}
	seen := map[int]bool{}
	var runs []actionRun

	for _, state := range activeStates {
		page := 1
		for {
			out, err := runGHQuiet("api", "--paginate",
				fmt.Sprintf("repos/%s/actions/runs?status=%s&per_page=100&exclude_pull_requests=false", repoVal, state))
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
					runs = append(runs, r)
				}
			}
			if len(resp.WorkflowRuns) < 100 {
				break
			}
			page++
			_ = page
		}
	}

	if len(runs) == 0 {
		fmt.Fprintf(os.Stderr, "ci-cancel-stale: no active CI runs on %s.\n", repoVal)
		return 0
	}

	// Apply filters
	var filtered []actionRun
	for _, r := range runs {
		if len(branchFilter) > 0 && !containsStr(branchFilter, r.HeadBranch) {
			continue
		}
		if len(wfFilter) > 0 && !containsStr(wfFilter, r.Name) {
			continue
		}
		filtered = append(filtered, r)
	}

	if len(filtered) == 0 {
		fmt.Fprintln(os.Stderr, "ci-cancel-stale: no active runs matched the filters.")
		return 0
	}

	// Resolve branch tips
	tipCache := map[string]string{}

	for _, r := range filtered {
		key := r.HeadRepo.FullName + "|" + r.HeadBranch
		if _, ok := tipCache[key]; ok {
			continue
		}
		if r.HeadBranch == "" {
			tipCache[key] = "?"
			continue
		}
		repo := r.HeadRepo.FullName
		if repo == "" {
			repo = repoVal
		}
		out, err := runGHQuiet("api", fmt.Sprintf("repos/%s/commits/%s", repo, r.HeadBranch), "-q", ".sha")
		sha := strings.TrimSpace(string(out))
		if err != nil || sha == "" {
			tipCache[key] = "?"
		} else {
			tipCache[key] = sha
		}
	}

	// Classify stale runs
	type staleRun struct {
		run    actionRun
		reason string
	}
	var stale []staleRun

	for _, r := range filtered {
		key := r.HeadRepo.FullName + "|" + r.HeadBranch
		tip := tipCache[key]
		repo := r.HeadRepo.FullName
		if repo == "" {
			repo = repoVal
		}
		gkey := r.Name + "|" + repo + "|" + r.HeadBranch

		var reason string
		if tip != "?" {
			if tip != r.HeadSHA {
				reason = fmt.Sprintf("branch moved to %s", tip[:min(9, len(tip))])
			}
		} else {
			// Supersession fallback: find the newest run in the same group
			newest := r.CreatedAt
			for _, other := range filtered {
				ogkey := other.Name + "|" + repo + "|" + other.HeadBranch
				if ogkey == gkey && other.CreatedAt > newest {
					newest = other.CreatedAt
				}
			}
			if r.CreatedAt != newest {
				reason = "superseded (newer active run on branch)"
			}
		}
		if reason != "" {
			sha := r.HeadSHA
			if len(sha) > 9 {
				sha = sha[:9]
			}
			fmt.Printf("STALE  run %-12s %-22s %-28s %s @ %s\n      -> %s — %s\n",
				fmt.Sprintf("%d", r.ID), r.Name[:min(22, len(r.Name))],
				r.HeadBranch[:min(28, len(r.HeadBranch))],
				sha, r.CreatedAt, reason, r.HTMLURL)
			stale = append(stale, staleRun{run: r, reason: reason})
		}
	}

	if len(stale) == 0 {
		fmt.Printf("ci-cancel-stale: scanned %d active run(s); none are outdated.\n", len(filtered))
		return 0
	}

	fmt.Printf("\nci-cancel-stale: %d active run(s) scanned, %d outdated.\n", len(filtered), len(stale))

	if !doCancel {
		fmt.Println("Dry run — nothing cancelled. Re-run with --cancel (or -y) to cancel.")
		return 0
	}

	// Cancel
	fail := 0
	for _, s := range stale {
		id := fmt.Sprintf("%d", s.run.ID)
		_, err := runGH("api", "--method", "POST", fmt.Sprintf("repos/%s/actions/runs/%s/cancel", repoVal, id))
		if err != nil {
			fmt.Fprintf(os.Stderr, "WARN: could not cancel run %s (already finishing?) — %s\n", id, s.run.HTMLURL)
			fail++
		} else {
			fmt.Printf("cancelled run %s (%s)\n", id, s.run.HeadBranch)
		}
	}
	fmt.Printf("ci-cancel-stale: requested cancellation of %d/%d run(s).\n", len(stale)-fail, len(stale))
	return 0
}

func containsStr(slice []string, val string) bool {
	for _, s := range slice {
		if s == val {
			return true
		}
	}
	return false
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
