// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Go port of scripts/backport-applies: for each maintained release line,
// fetch the given (master-path) file and grep it for a marker ERE, so a
// backport applicability check ("vulnerable / already-fixed / N-A") is one
// call instead of one show-branch-file invocation per branch.
package ghpr

import (
	"fmt"
	"os"
	"regexp"
	"strings"
)

const backportAppliesUsage = `usage: starfleetctl backport-applies <master-path> <grep-ERE> [release ...]
  <master-path>  repo path as on master, e.g. Xext/glx/createcontext.c
  <grep-ERE>     extended regex of markers to look for
  [release ...]  release lines to check (default: 25.2 25.1 25.0)
`

// RunBackportApplies implements `starfleetctl backport-applies <path> <ere> [release ...]`.
func RunBackportApplies(args []string) int {
	if len(args) >= 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Print(backportAppliesUsage)
		return 0
	}
	if len(args) < 2 {
		fmt.Fprint(os.Stderr, backportAppliesUsage)
		return 2
	}
	pathIn, ere := args[0], args[1]
	rels := args[2:]
	if len(rels) == 0 {
		rels = []string{"25.2", "25.1", "25.0"}
	}

	re, err := regexp.Compile(ere)
	if err != nil {
		fmt.Fprintln(os.Stderr, "backport-applies: invalid ERE:", err)
		return 2
	}

	for _, r := range rels {
		fmt.Printf("########## release/%s ##########\n", r)
		content, used, ferr := fetchBranchFile("release/"+r, pathIn)
		if ferr != nil {
			fmt.Printf("  file not found on release/%s (%s)\n", r, pathIn)
			continue
		}
		// Mirrors show-branch-file's own resolved-path stderr line, since
		// bash backport-applies calls it as a subprocess and inherits that
		// diagnostic on the terminal (only its stdout is captured).
		fmt.Fprintf(os.Stderr, "show-branch-file: %s@release/%s :: %s\n", repo(), r, used)
		matched := false
		for i, line := range strings.Split(content, "\n") {
			if re.MatchString(line) {
				fmt.Printf("%d:%s\n", i+1, line)
				matched = true
			}
		}
		if !matched {
			fmt.Printf("  (no line matched: %s)\n", ere)
		}
	}
	return 0
}
