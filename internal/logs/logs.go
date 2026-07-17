// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package logs is the `starfleetctl logs` subcommand — the manual side of the
// automatic feedback loop. `logs scan` walks the ship logs + agent-bus audit
// trail, extracts recurring failures (see internal/logscan), and either prints
// them (--dry-run, default) or captures each new finding as a dashboard task.
package logs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/metux/starfleetctl/internal/logscan"
	"github.com/metux/starfleetctl/internal/task"
)

const usage = `logs scan [--capture] [--min-count N] [--min-severity N] [--reset-seen]

Scan ship logs + agent-bus events for recurring failures and extract them as
findings. By default prints them (dry-run). With --capture, each NEW finding
(not seen before) is captured as a dashboard task.

Flags:
  --capture         capture new findings as dashboard tasks (else dry-run)
  --min-count N     only consider findings seen at least N times (default 1)
  --min-severity N  only consider findings with severity >= N (1..3, default 1)
  --reset-seen      forget previously captured signatures (re-capture all)
  --no-push         with --capture, commit locally but do not push

Exit: 0 ok, 2 bad args.
`

// Run implements `starfleetctl logs`.
func Run(root string, args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(usage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	if args[0] != "scan" {
		fmt.Fprintf(os.Stderr, "logs: unknown command: %s\n\n%s", args[0], usage)
		return 2
	}

	capture := false
	minCount := 1
	minSev := 1
	resetSeen := false
	noPush := false
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--capture":
			capture = true
		case "--no-push":
			noPush = true
		case "--reset-seen":
			resetSeen = true
		case "--min-count":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "logs: --min-count needs a value")
				return 2
			}
			fmt.Sscanf(args[i+1], "%d", &minCount)
			i++
		case "--min-severity":
			if i+1 >= len(args) {
				fmt.Fprintln(os.Stderr, "logs: --min-severity needs a value")
				return 2
			}
			fmt.Sscanf(args[i+1], "%d", &minSev)
			i++
		default:
			fmt.Fprintf(os.Stderr, "logs: unknown flag: %s\n", args[i])
			return 2
		}
	}

	files := logscan.LogFiles(root)
	findings := logscan.Scan(files)

	seen := logscan.LoadSeen(root)
	if resetSeen {
		for k := range seen.SeenMap() {
			_ = seen.Forget(k)
		}
	}

	var captured, printed int
	for _, f := range findings {
		if f.Count < minCount || f.Severity < minSev {
			continue
		}
		printed++
		fmt.Printf("[%s] sev=%d count=%d src=%s comp=%s\n  %s\n",
			f.Category, f.Severity, f.Count, strings.Join(f.Sources, ","), f.Component, f.Title)
		if f.Detail != "" {
			fmt.Printf("  sample: %s\n", f.Detail)
		}
		if !capture {
			continue
		}
		key := f.Key()
		if seen.Contains(key) {
			continue
		}
		desc := buildDesc(f)
		title := "[logscan] " + f.Title
		code, err := task.RunCaptureOnly(root, title, desc, "", noPush)
		// task capture refuses duplicate slugs and commits the topic locally
		// even when the dashboard's push/pull --rebase fails (e.g. no git
		// upstream configured). The reliable success check is whether the
		// topic file now exists on disk — so an unattended scan marks it seen
		// even if the push warned, and never re-captures a duplicate.
		slug := "task-" + deriveSlug(title)
		exists := topicExists(root, slug)
		if (err != nil || code != 0) && !exists {
			fmt.Fprintf(os.Stderr, "logs: capture failed for %q: %v (code %d)\n", f.Title, err, code)
			continue
		}
		if err != nil {
			fmt.Fprintf(os.Stderr, "logs: note: topic captured but dashboard push failed: %v\n", err)
		}
		if err := seen.Mark(key); err != nil {
			fmt.Fprintf(os.Stderr, "logs: warning: could not persist seen marker: %v\n", err)
		}
		captured++
		fmt.Printf("  -> captured as dashboard task: %s\n", slug)
	}

	if !capture {
		fmt.Printf("\n%d finding(s) printed (dry-run; pass --capture to create tasks)\n", printed)
	} else {
		fmt.Printf("\n%d finding(s) printed, %d new captured as tasks\n", printed, captured)
	}
	return 0
}

func buildDesc(f logscan.Finding) string {
	var b strings.Builder
	b.WriteString(fmt.Sprintf("Auto-extracted by `starfleetctl logs scan` from fleet logs.\n\n"))
	b.WriteString(fmt.Sprintf("Category : %s\n", f.Category))
	b.WriteString(fmt.Sprintf("Component: %s\n", f.Component))
	b.WriteString(fmt.Sprintf("Severity : %d\n", f.Severity))
	b.WriteString(fmt.Sprintf("Count    : %d\n", f.Count))
	b.WriteString(fmt.Sprintf("Sources  : %s\n", strings.Join(f.Sources, ", ")))
	if f.FirstSeen != "" {
		b.WriteString(fmt.Sprintf("First seen: %s\n", f.FirstSeen))
	}
	if f.LastSeen != "" {
		b.WriteString(fmt.Sprintf("Last seen : %s\n", f.LastSeen))
	}
	if f.Detail != "" {
		b.WriteString(fmt.Sprintf("\nSample:\n%s\n", f.Detail))
	}
	return b.String()
}

// topicExists reports whether a dashboard topic file <slug>.md exists. Used to
// confirm a capture succeeded even when task capture's git push warns (e.g. no
// upstream configured) — the file is committed locally regardless.
func topicExists(root, slug string) bool {
	p := filepath.Join(root, ".starfleet-ai", "dashboard", "topics", slug+".md")
	_, err := os.Stat(p)
	return err == nil
}

// deriveSlug mirrors internal/task.deriveSlug: lowercases, keeps [a-z0-9],
// replaces any other run with a single dash. Used to predict the topic slug
// task capture will write for a given title.
func deriveSlug(title string) string {
	var b strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(title) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			b.WriteRune(r)
			prevDash = false
		} else if b.Len() > 0 && !prevDash {
			b.WriteByte('-')
			prevDash = true
		}
	}
	return strings.Trim(b.String(), "-")
}
