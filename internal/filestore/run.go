// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package filestore

import (
	"fmt"
	"os"
	"time"
)

const usage = `Usage: starfleetctl file <command> [args...]

Commands:
  put <path> [--ttl <minutes>]   upload a file, print web URL
  list                           list stored files
  rm <name>                      remove a file
  prune                          remove all expired files
`

// Run dispatches file subcommands.
func Run(root string, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}

	store, err := New(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "file:", err)
		return 1
	}

	switch args[0] {
	case "put":
		return runPut(store, args[1:])
	case "list", "ls":
		return runList(store)
	case "rm", "remove":
		return runRemove(store, args[1:])
	case "prune":
		return runPrune(store)
	default:
		fmt.Fprintf(os.Stderr, "file: unknown subcommand: %s\n", args[0])
		return 2
	}
}

func runPut(s *Store, args []string) int {
	ttl := defaultTTL
	src := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--ttl":
			i++
			if i < len(args) {
				m, err := fmt.Sscanf(args[i], "%d", &ttl)
				if err != nil || m != 1 {
					fmt.Fprintln(os.Stderr, "file put: invalid ttl:", args[i])
					return 1
				}
				ttl = time.Duration(ttl) * time.Minute
			}
		default:
			src = args[i]
		}
	}
	if src == "" {
		fmt.Fprintln(os.Stderr, "file put: need <path>")
		return 2
	}
	url, err := s.Put(src, ttl)
	if err != nil {
		fmt.Fprintln(os.Stderr, "file put:", err)
		return 1
	}
	fmt.Println(url)
	return 0
}

func runList(s *Store) int {
	entries, err := s.List()
	if err != nil {
		fmt.Fprintln(os.Stderr, "file list:", err)
		return 1
	}
	if len(entries) == 0 {
		fmt.Println("(no files)")
		return 0
	}
	for _, e := range entries {
		mark := ""
		if e.Expired {
			mark = " [expired]"
		}
		fmt.Printf("%s  %6d  %s%s\n", e.Name, e.Size, e.TTL, mark)
	}
	return 0
}

func runRemove(s *Store, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "file rm: need <name>")
		return 2
	}
	for _, name := range args {
		if err := s.Remove(name); err != nil {
			fmt.Fprintln(os.Stderr, "file rm:", err)
			return 1
		}
	}
	return 0
}

func runPrune(s *Store) int {
	count, err := s.Prune()
	if err != nil {
		fmt.Fprintln(os.Stderr, "file prune:", err)
		return 1
	}
	fmt.Printf("removed %d expired files\n", count)
	return 0
}
