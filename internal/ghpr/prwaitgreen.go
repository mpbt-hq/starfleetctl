// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Wait for a PR's CI checks to complete and report green/red.
package ghpr

import (
	"encoding/json"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

type checkRun struct {
	Conclusion string `json:"conclusion"`
	Status     string `json:"status"`
	Name       string `json:"name"`
}

type statusRollup struct {
	Conclusion string    `json:"conclusion"`
	Status     string    `json:"status"`
	Context    string    `json:"context"`
	CheckRun   *checkRun `json:"checkRun"`
}

type prStatusData struct {
	StatusCheckRollup []statusRollup `json:"statusCheckRollup"`
}

const prWaitGreenUsage = `usage: starfleetctl pr wait-green <pr#> [--timeout <sec>] [--interval <sec>]
  Polls the PR's CI status until every check is done.
  Exit 0 = all green; 1 = some failed; 2 = timeout; 3 = bad args.

  --timeout <sec>   give up after this many seconds (default: 1800 / 30 min)
  --interval <sec>  poll interval (default: 30)
`

func RunPRWaitGreen(args []string) int {
	pr := ""
	timeout := 1800
	interval := 30

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			fmt.Print(prWaitGreenUsage)
			return 0
		case "--timeout":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "pr wait-green: --timeout needs a number")
				return 3
			}
			v, err := strconv.Atoi(args[i])
			if err != nil || v <= 0 {
				fmt.Fprintln(os.Stderr, "pr wait-green: invalid --timeout:", args[i])
				return 3
			}
			timeout = v
		case "--interval":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "pr wait-green: --interval needs a number")
				return 3
			}
			v, err := strconv.Atoi(args[i])
			if err != nil || v <= 0 {
				fmt.Fprintln(os.Stderr, "pr wait-green: invalid --interval:", args[i])
				return 3
			}
			interval = v
		default:
			if strings.HasPrefix(args[i], "-") {
				fmt.Fprintln(os.Stderr, "pr wait-green: unknown arg:", args[i])
				return 3
			}
			pr = args[i]
		}
	}
	if pr == "" {
		fmt.Fprintln(os.Stderr, "pr wait-green: need a <pr#>")
		return 3
	}

	repoVal, err := Repo()
	if err != nil {
		fmt.Fprintln(os.Stderr, "pr wait-green:", err)
		return 3
	}

	deadline := time.Now().Add(time.Duration(timeout) * time.Second)
	for {
		status, failed, pending, err := pollStatus(repoVal, pr)
		if err != nil {
			fmt.Fprintf(os.Stderr, "pr wait-green: %v\n", err)
			return 1
		}
		if pending == 0 {
			if failed == 0 {
				fmt.Println("All CI checks passed.")
				return 0
			}
			fmt.Printf("CI checks completed with %d failure(s).\n", failed)
			return 1
		}
		if time.Now().After(deadline) {
			fmt.Printf("Timeout after %ds — %d check(s) still pending.\n", timeout, pending)
			return 2
		}
		fmt.Printf("%d SUCCESS, %d FAIL, %d pending — waiting %ds ...\n", status, failed, pending, interval)
		time.Sleep(time.Duration(interval) * time.Second)
	}
}

func pollStatus(repo, pr string) (success, failed, pending int, err error) {
	out, err := runGH("pr", "view", pr, "--repo", repo, "--json", "statusCheckRollup")
	if err != nil {
		return 0, 0, 0, fmt.Errorf("gh pr view failed: %v", err)
	}
	var data prStatusData
	if err := json.Unmarshal(out, &data); err != nil {
		return 0, 0, 0, fmt.Errorf("parsing status: %v", err)
	}
	for _, c := range data.StatusCheckRollup {
		status := c.Conclusion
		if status == "" {
			status = c.Status
		}
		// Use checkRun fields if populated (modern gh returns node-based rollup)
		if c.CheckRun != nil {
			status = c.CheckRun.Conclusion
			if status == "" {
				status = c.CheckRun.Status
			}
		}
		switch status {
		case "SUCCESS", "NEUTRAL", "SKIPPED":
			success++
		case "FAILURE", "ERROR", "CANCELLED", "TIMED_OUT":
			failed++
		default:
			// QUEUED, IN_PROGRESS, PENDING, REQUESTED, WAITING, …
			pending++
		}
	}
	return
}
