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
  reindex                       regenerate the thin index from dashboard/topics/*.md

  topic list [--json]                        every topic's slug/title/status
  topic show <slug>                          print one topic file (implies pull)
  topic write <slug> <file|->                replace one topic file (no commit)
  topic new <slug> --title "<t>" [--status "<s>"] [--parked]
                                              scaffold a new topic file
  topic commit <slug> -m "<msg>" [--no-push] commit+push JUST that one file
`

const topicUsage = `dashboard topic <command> [args…]
  list [--json]
  show <slug>
  write <slug> <file|->
  new <slug> --title "<t>" [--status "<s>"] [--parked]
  commit <slug> -m "<msg>" [--no-push]
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
	case "reindex":
		cmdErr = d.DoReindex()
	case "topic":
		return runTopic(d, args[1:])
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

func runTopic(d *Dashboard, args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(topicUsage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	var cmdErr error
	switch args[0] {
	case "list":
		jsonOut := false
		for _, a := range args[1:] {
			if a == "--json" {
				jsonOut = true
			}
		}
		cmdErr = d.DoTopicList(jsonOut)
	case "show":
		if len(args) != 2 {
			fmt.Fprint(os.Stderr, topicUsage)
			return 2
		}
		cmdErr = d.DoTopicShow(args[1])
	case "write":
		if len(args) != 3 {
			fmt.Fprint(os.Stderr, topicUsage)
			return 2
		}
		cmdErr = d.DoTopicWrite(args[1], args[2])
	case "new":
		if len(args) < 2 {
			fmt.Fprint(os.Stderr, topicUsage)
			return 2
		}
		slug := args[1]
		title, status, category, perr := parseTopicNewArgs(args[2:])
		if perr != nil {
			fmt.Fprintln(os.Stderr, "dashboard topic new:", perr)
			return 2
		}
		cmdErr = d.DoTopicNew(slug, title, status, category)
	case "commit":
		if len(args) < 2 {
			fmt.Fprint(os.Stderr, topicUsage)
			return 2
		}
		slug := args[1]
		msg, push, perr := parseCommitArgs(args[2:])
		if perr != nil {
			fmt.Fprintln(os.Stderr, "dashboard topic commit:", perr)
			return 2
		}
		cmdErr = d.DoTopicCommit(slug, msg, push)
	default:
		fmt.Fprintf(os.Stderr, "dashboard topic: unknown command: %s\n\n%s", args[0], topicUsage)
		return 2
	}
	if cmdErr != nil {
		fmt.Fprintln(os.Stderr, "dashboard topic:", cmdErr)
		return 1
	}
	return 0
}

func parseTopicNewArgs(args []string) (title, status, category string, err error) {
	category = "active"
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--title":
			if i+1 >= len(args) {
				return "", "", "", fmt.Errorf("--title requires an argument")
			}
			i++
			title = args[i]
		case "--status":
			if i+1 >= len(args) {
				return "", "", "", fmt.Errorf("--status requires an argument")
			}
			i++
			status = args[i]
		case "--parked":
			category = "parked"
		default:
			return "", "", "", fmt.Errorf("unknown option: %s", args[i])
		}
	}
	if title == "" {
		return "", "", "", fmt.Errorf("--title is required")
	}
	return title, status, category, nil
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
