// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Go port of scripts/show-pr-conflict: list all open PRs with merge
// conflicts.
package ghpr

import (
	"fmt"
)

type prConflictEntry struct {
	Number    int    `json:"number"`
	Title     string `json:"title"`
	Mergeable string `json:"mergeable"`
	Author    struct {
		Login string `json:"login"`
	} `json:"author"`
}

// RunShowPRConflict implements `starfleetctl show-pr-conflict`.
func RunShowPRConflict(args []string) int {
	if len(args) >= 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Println("usage: starfleetctl show-pr-conflict\nenv: REPO (default X11Libre/xserver)")
		return 0
	}

	var prs []prConflictEntry
	if err := ghJSON(&prs, "pr", "list", "--repo", repo(), "--limit", "100",
		"--json", "number,author,title,mergeable"); err != nil {
		fprintErr("show-pr-conflict", err)
		return 1
	}

	for _, pr := range prs {
		if pr.Mergeable == "CONFLICTING" {
			fmt.Printf("%d: (%s) %s - %s\n", pr.Number, pr.Author.Login, pr.Title, pr.Author.Login)
		}
	}
	return 0
}
