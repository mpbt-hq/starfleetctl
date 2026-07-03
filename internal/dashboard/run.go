// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package dashboard

import (
	"fmt"
	"os"
)

const usage = `dashboard <command> [args…]

  pull                          sync local DASHBOARD.md with origin
  show                          print current DASHBOARD.md (implies pull)
  write <file|->                replace DASHBOARD.md's content (no commit)
  commit -m "<msg>" [--no-push] stage + commit (+ pull --rebase + push)
`

// Run dispatches a `dashboard` invocation exactly like scripts/dashboard's
// case statement, given the resolved workspace root. Returns the process
// exit code.
func Run(root string, args []string) int {
	d, err := New(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "dashboard:", err)
		return 1
	}

	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}

	var cmdErr error
	switch args[0] {
	case "-h", "--help":
		fmt.Print(usage)
		return 0
	case "pull":
		cmdErr = d.DoPull()
	case "show":
		cmdErr = d.DoShow()
	case "write":
		if len(args) != 2 {
			fmt.Fprint(os.Stderr, usage)
			return 2
		}
		cmdErr = d.DoWrite(args[1])
	case "commit":
		msg, push, perr := parseCommitArgs(args[1:])
		if perr != nil {
			fmt.Fprintln(os.Stderr, "dashboard:", perr)
			return 2
		}
		cmdErr = d.DoCommit(msg, push)
	default:
		fmt.Fprintf(os.Stderr, "dashboard: unknown command: %s\n\n%s", args[0], usage)
		return 2
	}
	if cmdErr != nil {
		fmt.Fprintln(os.Stderr, "dashboard:", cmdErr)
		return 1
	}
	return 0
}

func parseCommitArgs(args []string) (msg string, push bool, err error) {
	push = true
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-m":
			if i+1 >= len(args) {
				return "", false, fmt.Errorf("-m requires an argument")
			}
			i++
			msg = args[i]
		case "--no-push":
			push = false
		default:
			return "", false, fmt.Errorf("unknown option: %s", args[i])
		}
	}
	if msg == "" {
		return "", false, fmt.Errorf("-m <message> is required")
	}
	return msg, push, nil
}
