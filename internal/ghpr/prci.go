// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Go port of scripts/pr-ci: quick CI status of a PR, classified BY
// CONCLUSION not by count (the CI matrix is fail-fast, so one real FAILURE
// cancels every still-running sibling — a big red number is usually mostly
// CANCELLED collateral, not the cause; see CLAUDE.md). Replaces the bash
// original's jq bucket-classification pipeline with native Go structs —
// same logic, no jq dependency.
package ghpr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"regexp"
	"strings"
)

const prCIUsage = `usage: starfleetctl pr-ci <pr#>
       starfleetctl pr-ci https://github.com/OWNER/REPO/pull/<n>
       starfleetctl pr-ci <pr#> --json
env:
  STARFLEET_GITHUB_REPO   repo slug (or $REPO for backward compat); a URL argument wins over it
`

type ciCheck struct {
	Typename   string `json:"__typename"`
	Name       string `json:"name"`
	Context    string `json:"context"`
	Status     string `json:"status"`
	Conclusion string `json:"conclusion"`
	State      string `json:"state"`
}

func (c ciCheck) bucket() string {
	if c.Typename == "CheckRun" {
		switch {
		case c.Status != "COMPLETED":
			return "pending"
		case c.Conclusion == "SUCCESS":
			return "pass"
		case c.Conclusion == "FAILURE", c.Conclusion == "TIMED_OUT", c.Conclusion == "STARTUP_FAILURE":
			return "fail"
		case c.Conclusion == "CANCELLED":
			return "cancelled"
		default:
			return "skip"
		}
	}
	switch c.State {
	case "SUCCESS":
		return "pass"
	case "FAILURE", "ERROR":
		return "fail"
	case "PENDING":
		return "pending"
	default:
		return "skip"
	}
}

func (c ciCheck) name() string {
	if c.Typename == "CheckRun" {
		return c.Name
	}
	return c.Context
}

func (c ciCheck) conclusionOrState() string {
	if c.Typename == "CheckRun" {
		if c.Conclusion != "" {
			return c.Conclusion
		}
		return c.Status
	}
	return c.State
}

type prCIView struct {
	Number            int       `json:"number"`
	Title             string    `json:"title"`
	HeadRefName       string    `json:"headRefName"`
	State             string    `json:"state"`
	Mergeable         string    `json:"mergeable"`
	URL               string    `json:"url"`
	StatusCheckRollup []ciCheck `json:"statusCheckRollup"`
}

var prURLRe = regexp.MustCompile(`^https?://[^/]+/([^/]+/[^/]+)/pull/(\d+)`)

var knownFlakeRe = regexp.MustCompile(`(?i)dragonfly|solaris|netbsd|openbsd|freebsd|xephyr-glamor|go-xts`)

// RunPRCi implements `starfleetctl pr-ci <pr#|URL> [--json]`.
func RunPRCi(args []string) int {
	repoVal := ""
	pr := ""
	wantJSON := false

	for _, a := range args {
		switch {
		case a == "-h" || a == "--help":
			fmt.Print(prCIUsage)
			return 0
		case a == "--json":
			wantJSON = true
		case strings.HasPrefix(a, "http://") || strings.HasPrefix(a, "https://"):
			m := prURLRe.FindStringSubmatch(a)
			if m == nil {
				fmt.Fprintln(os.Stderr, "pr-ci: could not parse PR URL:", a)
				return 2
			}
			repoVal, pr = m[1], m[2]
		case strings.HasPrefix(a, "-"):
			fmt.Fprintln(os.Stderr, "pr-ci: unknown arg:", a)
			return 2
		default:
			pr = a
		}
	}
	if pr == "" {
		fmt.Fprintln(os.Stderr, "pr-ci: need a PR number or URL (see --help)")
		return 2
	}
	if repoVal == "" {
		var err error
		repoVal, err = Repo()
		if err != nil {
			fmt.Fprintln(os.Stderr, "pr-ci:", err)
			return 2
		}
	}

	// bash pr-ci redirects gh's stderr to /dev/null on this call and prints
	// its own friendly "could not fetch" message instead — match that.
	raw, err := runGHQuiet("pr", "view", pr, "--repo", repoVal,
		"--json", "number,title,headRefName,state,mergeable,url,statusCheckRollup")
	if err != nil {
		fmt.Fprintf(os.Stderr, "pr-ci: could not fetch PR %s from %s\n", pr, repoVal)
		return 1
	}

	var view prCIView
	if err := json.Unmarshal(raw, &view); err != nil {
		fmt.Fprintln(os.Stderr, "pr-ci: could not parse gh output:", err)
		return 1
	}

	if wantJSON {
		// Re-emit statusCheckRollup verbatim (all of gh's fields, not just
		// the subset ciCheck models for classification) — matches
		// `jq '.statusCheckRollup'`'s raw passthrough exactly, just
		// re-indented.
		var wrap struct {
			StatusCheckRollup json.RawMessage `json:"statusCheckRollup"`
		}
		if err := json.Unmarshal(raw, &wrap); err != nil {
			fmt.Fprintln(os.Stderr, "pr-ci: could not parse gh output:", err)
			return 1
		}
		var buf bytes.Buffer
		if err := json.Indent(&buf, wrap.StatusCheckRollup, "", "  "); err != nil {
			fmt.Fprintln(os.Stderr, "pr-ci: could not format statusCheckRollup:", err)
			return 1
		}
		fmt.Println(buf.String())
		return 0
	}

	checks := view.StatusCheckRollup
	var pass, fail, cancelled, pending, skip []ciCheck
	for _, c := range checks {
		switch c.bucket() {
		case "pass":
			pass = append(pass, c)
		case "fail":
			fail = append(fail, c)
		case "cancelled":
			cancelled = append(cancelled, c)
		case "pending":
			pending = append(pending, c)
		default:
			skip = append(skip, c)
		}
	}

	fmt.Printf("PR #%d - %s\n", view.Number, view.Title)
	fmt.Printf("  %s\n", view.URL)
	fmt.Printf("  state=%s  mergeable=%s  head=%s\n", view.State, view.Mergeable, view.HeadRefName)
	fmt.Printf("  checks: %d  pass=%d  FAIL=%d  cancelled=%d  pending=%d  skip=%d\n",
		len(checks), len(pass), len(fail), len(cancelled), len(pending), len(skip))

	if len(fail) > 0 {
		fmt.Println("  -- real failures (act on these) --")
		for _, c := range fail {
			fmt.Printf("    FAIL  %s  [%s]\n", c.name(), c.conclusionOrState())
		}
	}
	if len(pending) > 0 {
		fmt.Println("  -- still pending --")
		for _, c := range pending {
			fmt.Printf("    ...   %s\n", c.name())
		}
	}

	switch {
	case len(fail) > 0 && len(pending) > 0:
		fmt.Printf("  VERDICT: %d real failure(s), %d pending\n", len(fail), len(pending))
	case len(fail) > 0:
		fmt.Printf("  VERDICT: %d real failure(s)\n", len(fail))
	case len(pending) > 0:
		fmt.Printf("  VERDICT: %d pending, no failures yet\n", len(pending))
	case len(checks) == 0:
		fmt.Println("  VERDICT: (no checks reported)")
	default:
		fmt.Println("  VERDICT: all green")
	}

	for _, c := range fail {
		if knownFlakeRe.MatchString(c.name()) {
			fmt.Println("  ⚠ note: some failing lanes match known flakes (BSD/Solaris VM boot, xephyr-glamor XTS")
			fmt.Println("    timeout, go-xts race). Confirm vs a sibling run / rerun before assuming breakage —")
			fmt.Printf("    see CLAUDE.md 'PR repair workflow'. Get logs with: starfleetctl pr-job-logs %s\n", pr)
			break
		}
	}

	return 0
}
