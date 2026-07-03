// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package wscommit

import (
	"fmt"
	"os"
	"strings"
)

const usage = `ws-commit -m "<message>" <path> [<path>...]   add the given paths, commit, push
ws-commit -m "<message>" -a                    add all tracked changes (git add -u)
ws-commit --no-push -m "<message>" <path>      commit only, don't push
`

// Run dispatches a `ws-commit` invocation exactly like scripts/ws-commit's
// argument parsing, given the resolved workspace root. Returns the process
// exit code.
func Run(root string, args []string) int {
	msg, paths, push, err := parseArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ws-commit:", err)
		fmt.Fprint(os.Stderr, usage)
		return 2
	}

	w, err := New(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "ws-commit:", err)
		return 1
	}

	if err := w.DoCommit(msg, paths, push); err != nil {
		fmt.Fprintln(os.Stderr, "ws-commit:", err)
		return 1
	}
	return 0
}

// parseArgs mirrors scripts/ws-commit's `while [ $# -gt 0 ]; case "$1" in …`
// loop, including the bash quirk that -a/--all replaces (not appends to)
// paths with the literal element "-u".
func parseArgs(args []string) (msg string, paths []string, push bool, err error) {
	push = true
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-m":
			if i+1 >= len(args) {
				return "", nil, false, fmt.Errorf("-m requires an argument")
			}
			i++
			msg = args[i]
		case "--no-push":
			push = false
		case "-a", "--all":
			paths = []string{"-u"}
		case "--":
			paths = append(paths, args[i+1:]...)
			i = len(args)
		default:
			if strings.HasPrefix(args[i], "-") {
				return "", nil, false, fmt.Errorf("unknown option: %s", args[i])
			}
			paths = append(paths, args[i])
		}
	}
	if msg == "" {
		return "", nil, false, fmt.Errorf("-m <message> is required")
	}
	return msg, paths, push, nil
}
