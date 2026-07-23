// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package shipnames

import (
	"fmt"
	"os"
)

const usage = `ship-names assign [flagship]  pick an unused name (flagship = Enterprise)
ship-names release <name>     free a reservation
ship-names list [--json]      show all names and current status
ship-names shell-env          output shell code to set up STARFLEET_SHIP_ID + PS1 + EXIT trap
ship-names gc                 remove reservations with no live comms entry
ship-names flagship           print the flagship name (Enterprise)
`

func hasJSON(args []string) bool {
	for _, a := range args {
		if a == "--json" {
			return true
		}
	}
	return false
}

// Run dispatches a `ship-names` invocation exactly like scripts/ship-names'
// case statement, given the resolved workspace root. Returns the process
// exit code. Default command (no args) is "list", matching the bash
// original's `cmd="${1:-list}"`.
func Run(root string, args []string) int {
	r := New(root)

	cmd := "list"
	rest := args
	if len(args) > 0 {
		cmd = args[0]
		rest = args[1:]
	}

	var err error
	switch cmd {
	case "-h", "--help":
		fmt.Print(usage)
		return 0
	case "assign":
		if len(rest) > 0 && rest[0] == "flagship" {
			err = r.DoAssignFlagship()
		} else {
			err = r.DoAssign()
		}
	case "release":
		err = r.DoRelease(arg(rest, 0))
	case "list":
		if hasJSON(rest) {
			err = r.DoListJSON()
		} else {
			err = r.DoList()
		}
	case "gc":
		err = r.DoGC()
	case "flagship":
		err = r.DoFlagship()
	case "shell-env":
		err = r.DoShellEnv()
	default:
		fmt.Fprintf(os.Stderr, "ship-names: unknown command: %s  (try --help)\n", cmd)
		return 1
	}

	if err != nil {
		fmt.Fprintln(os.Stderr, "ship-names:", err)
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
