// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package agents

import (
	"fmt"
	"os"
	"strconv"
)

const usage = `agents <command> [args…]

  list [--json]                              every fragment's slug/title/order
  show <slug>                                print one fragment file
  write <slug> <file|->                      replace one fragment file (no commit), then reindex
  new <slug> --title "<t>" [--order <n>] [--owner "<tool>"]
                                              scaffold a new fragment file
  reindex [--inline|--no-inline]              regenerate agents.d/index.md from agents.d/**/*.md
                                              (--inline also writes a self-contained CLAUDE.md with no
                                              @-imports, for agents that don't resolve @, e.g. opencode)
  commit [<slug>] -m "<msg>" [--no-push]     commit+push one fragment, or (no slug) CLAUDE.md+index.md
  install-self [--order <n>]                 write/refresh agents.d/starfleet/starfleetctl.md from this
                                              binary's own embedded README.md (always overwrites —
                                              tool-owned, re-run after a starfleetctl update)
  install-starfleet [<subdir>]               install all embedded starfleet fragments from the binary
                                              (default subdir: "starfleet") — writes to
                                              agents.d/<slug>.md for each, always overwrites, then
                                              reindexes
  install-starfleet-skills                   install embedded starfleet skills from the binary
                                              (fragments/starfleet-skills/) — writes to
                                              .claude/skills/<name>/ for each, always overwrites
`

// Run dispatches an `agents` invocation, given the resolved workspace root.
// Returns the process exit code.
func Run(root string, args []string) int {
	a, err := New(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agents:", err)
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
	case "list":
		jsonOut := false
		for _, arg := range args[1:] {
			if arg == "--json" {
				jsonOut = true
			}
		}
		cmdErr = a.DoList(jsonOut)
	case "show":
		if len(args) != 2 {
			fmt.Fprint(os.Stderr, usage)
			return 2
		}
		cmdErr = a.DoShow(args[1])
	case "write":
		if len(args) != 3 {
			fmt.Fprint(os.Stderr, usage)
			return 2
		}
		cmdErr = a.DoWrite(args[1], args[2])
	case "new":
		if len(args) < 2 {
			fmt.Fprint(os.Stderr, usage)
			return 2
		}
		slug := args[1]
		title, order, owner, perr := parseNewArgs(args[2:])
		if perr != nil {
			fmt.Fprintln(os.Stderr, "agents new:", perr)
			return 2
		}
		cmdErr = a.DoNew(slug, title, order, owner)
	case "reindex":
		inline := a.Inline()
		for _, arg := range args[1:] {
			switch arg {
			case "--inline":
				inline = true
			case "--no-inline":
				inline = false
			}
		}
		if err := a.SetInline(inline); err != nil {
			cmdErr = err
			break
		}
		cmdErr = a.DoReindex(inline)
	case "install-self":
		order := 900 // default: near the end, after project-specific fragments
		for i := 1; i < len(args); i++ {
			if args[i] == "--order" && i+1 < len(args) {
				if n, perr := strconv.Atoi(args[i+1]); perr == nil {
					order = n
				}
				i++
			}
		}
		cmdErr = a.DoInstallSelf(order)
	case "install-starfleet":
		subdir := StarfleetSubdir
		if len(args) > 1 {
			subdir = args[1]
		}
		cmdErr = a.DoInstallStarfleet(subdir)
	case "install-starfleet-skills":
		cmdErr = a.DoInstallStarfleetSkills()
	case "commit":
		slug, msg, push, perr := parseCommitArgs(args[1:])
		if perr != nil {
			fmt.Fprintln(os.Stderr, "agents commit:", perr)
			return 2
		}
		cmdErr = a.DoCommit(slug, msg, push)
	default:
		fmt.Fprintf(os.Stderr, "agents: unknown command: %s\n\n%s", args[0], usage)
		return 2
	}
	if cmdErr != nil {
		fmt.Fprintln(os.Stderr, "agents:", cmdErr)
		return 1
	}
	return 0
}

func parseNewArgs(args []string) (title string, order int, owner string, err error) {
	order = 500 // default: sorts after typical hand-picked low numbers, before nothing in particular
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--title":
			if i+1 >= len(args) {
				return "", 0, "", fmt.Errorf("--title requires an argument")
			}
			i++
			title = args[i]
		case "--order":
			if i+1 >= len(args) {
				return "", 0, "", fmt.Errorf("--order requires an argument")
			}
			i++
			n, perr := strconv.Atoi(args[i])
			if perr != nil {
				return "", 0, "", fmt.Errorf("--order must be an integer: %w", perr)
			}
			order = n
		case "--owner":
			if i+1 >= len(args) {
				return "", 0, "", fmt.Errorf("--owner requires an argument")
			}
			i++
			owner = args[i]
		default:
			return "", 0, "", fmt.Errorf("unknown option: %s", args[i])
		}
	}
	if title == "" {
		return "", 0, "", fmt.Errorf("--title is required")
	}
	return title, order, owner, nil
}

// parseCommitArgs handles both `commit -m <msg>` (root files) and
// `commit <slug> -m <msg>` (one fragment) — the slug, if present, is
// whatever leading non-flag argument comes first.
func parseCommitArgs(args []string) (slug, msg string, push bool, err error) {
	push = true
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-m":
			if i+1 >= len(args) {
				return "", "", false, fmt.Errorf("-m requires an argument")
			}
			i++
			msg = args[i]
		case "--no-push":
			push = false
		default:
			if slug != "" {
				return "", "", false, fmt.Errorf("unexpected argument: %s", args[i])
			}
			slug = args[i]
		}
	}
	if msg == "" {
		return "", "", false, fmt.Errorf("-m <message> is required")
	}
	return slug, msg, push, nil
}
