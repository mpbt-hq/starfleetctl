// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Go port of scripts/pr-request-reviewers: request one or more reviewers on
// a PR via the REST API (the multi-flag `requested_reviewers` recipe from
// AGENTS.md's HW-domain-routing section).
package ghpr

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const prRequestReviewersUsage = `usage: starfleetctl pr-request-reviewers <pr#> <login> [login...]
env: REPO  override repo (default X11Libre/xserver)
`

// RunPRRequestReviewers implements `starfleetctl pr-request-reviewers`.
func RunPRRequestReviewers(args []string) int {
	if len(args) >= 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Print(prRequestReviewersUsage)
		return 0
	}
	if len(args) < 2 {
		fmt.Fprint(os.Stderr, prRequestReviewersUsage)
		return 2
	}

	pr := strings.TrimPrefix(args[0], "#")
	logins := args[1:]

	ghArgs := []string{"api", "--method", "POST", "repos/" + repo() + "/pulls/" + pr + "/requested_reviewers"}
	for _, l := range logins {
		ghArgs = append(ghArgs, "-f", "reviewers[]="+l)
	}
	ghArgs = append(ghArgs, "--jq", ".html_url")

	cmd := exec.Command("gh", ghArgs...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fprintErr("pr-request-reviewers", err)
		return 1
	}
	fmt.Printf("pr-request-reviewers: requested on #%s: %s\n", pr, strings.Join(logins, " "))
	return 0
}
