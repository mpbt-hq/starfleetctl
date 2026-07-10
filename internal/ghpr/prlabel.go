// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Go port of scripts/pr-label: add/remove GitHub PR labels via the REST API
// (gh pr edit --add-label fails on this repo with the "Projects classic
// deprecation" GraphQL error, same workaround as pr-set-body).
package ghpr

import (
	"fmt"
	"os"
	"strings"
)

const prLabelUsage = `usage: starfleetctl pr-label <pr#> add|remove <label...>
       starfleetctl pr-label <pr#> set-review passed|changes-requested
env: STARFLEET_GITHUB_REPO   repo slug (or $REPO for backward compat)
`

// RunPRLabel implements `starfleetctl pr-label`.
func RunPRLabel(args []string) int {
	if len(args) >= 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Print(prLabelUsage)
		return 0
	}
	if len(args) < 2 {
		fmt.Fprint(os.Stderr, prLabelUsage)
		return 2
	}

	pr := strings.TrimPrefix(args[0], "#")
	mode := args[1]
	rest := args[2:]

	switch mode {
	case "add":
		if len(rest) == 0 {
			fmt.Fprintln(os.Stderr, "pr-label: add needs at least one label")
			return 1
		}
		ghArgs := []string{"api", "--method", "POST", "repos/" + repo() + "/issues/" + pr + "/labels"}
		for _, l := range rest {
			ghArgs = append(ghArgs, "-f", "labels[]="+l)
		}
		if _, err := runGH(ghArgs...); err != nil {
			fprintErr("pr-label", err)
			return 1
		}
		fmt.Printf("pr-label: added to #%s: %s\n", pr, strings.Join(rest, " "))

	case "remove":
		if len(rest) == 0 {
			fmt.Fprintln(os.Stderr, "pr-label: remove needs at least one label")
			return 1
		}
		for _, l := range rest {
			if _, err := runGHQuiet("api", "--method", "DELETE", "repos/"+repo()+"/issues/"+pr+"/labels/"+l); err != nil {
				fmt.Fprintf(os.Stderr, "pr-label: #%s had no '%s' label (skipped)\n", pr, l)
			}
		}
		fmt.Printf("pr-label: removed from #%s: %s\n", pr, strings.Join(rest, " "))

	case "set-review":
		if len(rest) == 0 {
			fmt.Fprintln(os.Stderr, "usage: starfleetctl pr-label <pr#> set-review passed|changes-requested")
			return 1
		}
		var newLabel, oldLabel string
		switch rest[0] {
		case "passed":
			newLabel, oldLabel = "bot-review-passed", "bot-review-changes-requested"
		case "changes-requested":
			newLabel, oldLabel = "bot-review-changes-requested", "bot-review-passed"
		default:
			fmt.Fprintf(os.Stderr, "pr-label: set-review wants 'passed' or 'changes-requested', got '%s'\n", rest[0])
			return 1
		}
		_, _ = runGHQuiet("api", "--method", "DELETE", "repos/"+repo()+"/issues/"+pr+"/labels/"+oldLabel)
		if _, err := runGH("api", "--method", "POST", "repos/"+repo()+"/issues/"+pr+"/labels", "-f", "labels[]="+newLabel); err != nil {
			fprintErr("pr-label", err)
			return 1
		}
		fmt.Printf("pr-label: #%s -> %s\n", pr, newLabel)

	default:
		fmt.Fprintf(os.Stderr, "pr-label: unknown mode '%s' (want add|remove|set-review)\n", mode)
		return 1
	}
	return 0
}
