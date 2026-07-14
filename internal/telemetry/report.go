// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// `starfleetctl telemetry report` aggregates the permission-confirmation
// telemetry collected by the hook adapters (see telemetry.go) and ranks
// the most common command prefixes that would have needed an interactive
// permission prompt — i.e. candidates for a new scripts/* / starfleetctl
// wrapper + matching allowlist entry. Ported from scripts/confirm-log-report.
package telemetry

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// usage is shown for `starfleetctl telemetry --help` / `report --help`.
const usage = `telemetry <subcommand> [args…]

Subcommands:
  report [--top N] [--words N] [--category no-match|ask-match|deny-match]
        rank the most common command prefixes that would have needed a
        permission prompt, from the fleet-shared tooling-gaps log.

  --top N       show the N most common prefixes (default 20)
  --words N     leading whitespace-separated words per grouping key (default 2)
  --category    only count entries of this category (default: all)
`

// Run is the entry point for `starfleetctl telemetry …`.
func Run(root string, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	switch args[0] {
	case "-h", "--help":
		fmt.Print(usage)
		return 0
	case "report":
		return report(root, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "telemetry: unknown subcommand '%s'\n", args[0])
		return 2
	}
}

func report(root string, args []string) int {
	top := 20
	words := 2
	category := ""
	i := 0
	for i < len(args) {
		a := args[i]
		switch a {
		case "-h", "--help":
			fmt.Print(usage)
			return 0
		case "--top":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "telemetry report: --top needs an argument")
				return 2
			}
			if _, err := fmt.Sscanf(args[i], "%d", &top); err != nil {
				fmt.Fprintf(os.Stderr, "telemetry report: bad --top %q\n", args[i])
				return 2
			}
		case "--words":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "telemetry report: --words needs an argument")
				return 2
			}
			if _, err := fmt.Sscanf(args[i], "%d", &words); err != nil {
				fmt.Fprintf(os.Stderr, "telemetry report: bad --words %q\n", args[i])
				return 2
			}
		case "--category":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "telemetry report: --category needs an argument")
				return 2
			}
			category = args[i]
		default:
			fmt.Fprintf(os.Stderr, "telemetry report: unknown arg: %s\n", a)
			return 2
		}
		i++
	}

	logPath := LogPath(root)
	f, err := os.Open(logPath)
	if err != nil {
		fmt.Printf("telemetry report: no log yet at %s\n", logPath)
		return 0
	}
	defer f.Close()

	type count struct {
		n       int
		example string
	}
	counts := map[string]*count{}
	total := 0

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 1024*1024), 16*1024*1024)
	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" {
			continue
		}
		var e Event
		if err := json.Unmarshal([]byte(line), &e); err != nil {
			continue
		}
		if category != "" && e.Category != category {
			continue
		}
		total++
		key := strings.Join(strings.Fields(e.Subcommand)[:min(words, len(strings.Fields(e.Subcommand)))], " ")
		if key == "" {
			key = "(empty)"
		}
		c, ok := counts[key]
		if !ok {
			c = &count{example: e.Subcommand}
			counts[key] = c
		}
		c.n++
	}

	// Sort by count descending, stable on key for determinism.
	type kv struct {
		key string
		c   *count
	}
	ordered := make([]kv, 0, len(counts))
	for k, v := range counts {
		ordered = append(ordered, kv{k, v})
	}
	sort.Slice(ordered, func(i, j int) bool {
		if ordered[i].c.n != ordered[j].c.n {
			return ordered[i].c.n > ordered[j].c.n
		}
		return ordered[i].key < ordered[j].key
	})

	fmt.Printf("telemetry report: %d logged event(s), %d distinct prefix(es) (grouped by first %d word(s))\n\n",
		total, len(counts), words)
	limit := top
	if limit > len(ordered) {
		limit = len(ordered)
	}
	for _, kv := range ordered[:limit] {
		fmt.Printf("%6d  %s   (e.g. %q)\n", kv.c.n, kv.key, kv.c.example)
	}
	return 0
}

func min(a, b int) int {
	if a < b {
		return a
	}
	return b
}
