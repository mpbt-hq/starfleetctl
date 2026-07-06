// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package prclaim

import (
	"fmt"
	"os"
)

const usage = `pr-claim <pr#> ["what you're doing"]   claim PR; exit 3 if held by another agent
pr-claim --list [--json]               show all active claims (the work log)
pr-claim --release <pr#>               drop your claim on PR
pr-claim --release-all                 drop all claims held by this agent
pr-claim --steal <pr#> ["what"]        take over someone else's claim (logged)
pr-claim --who <pr#>                   print holder; exit 3 if held by another
`

func hasJSON(args []string) bool {
	for _, a := range args {
		if a == "--json" {
			return true
		}
	}
	return false
}

// Run dispatches a `pr-claim` invocation exactly like scripts/pr-claim's
// case statement, given the resolved workspace root. Returns the process
// exit code. FORCE=1 in the environment mirrors bash's --release override.
func Run(root string, args []string) int {
	c, err := New(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pr-claim:", err)
		return 1
	}

	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 1
	}

	var cmdErr error
	switch args[0] {
	case "-h", "--help":
		fmt.Print(usage)
		return 0
	case "-l", "--list":
		if hasJSON(args[1:]) {
			cmdErr = c.DoListJSON()
		} else {
			cmdErr = c.DoList()
		}
	case "--release":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "pr-claim: --release needs <pr#>")
			return 2
		}
		cmdErr = c.DoRelease(args[1], os.Getenv("FORCE") == "1")
	case "--release-all":
		cmdErr = c.DoReleaseAll()
	case "--steal":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "pr-claim: --steal needs <pr#>")
			return 2
		}
		cmdErr = c.DoClaim(args[1], arg(args, 2), true)
	case "--who":
		if len(args) < 2 {
			fmt.Fprintln(os.Stderr, "pr-claim: --who needs <pr#>")
			return 2
		}
		cmdErr = c.DoWho(args[1])
	default:
		if len(args[0]) > 1 && args[0][0] == '-' {
			fmt.Fprintf(os.Stderr, "pr-claim: unknown option: %s\n", args[0])
			return 2
		}
		cmdErr = c.DoClaim(args[0], arg(args, 1), false)
	}

	if cmdErr != nil {
		if ee, ok := cmdErr.(exitError); ok {
			return ee.Code()
		}
		fmt.Fprintln(os.Stderr, "pr-claim:", cmdErr)
		return 1
	}
	return 0
}

func arg(args []string, i int) string {
	if i < len(args) {
		return args[i]
	}
	return ""
}
