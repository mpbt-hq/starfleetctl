// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package task is the Go port of scripts/task-capture — pure commandeering
// helper for the starfleet fleet. It captures a task into the workspace
// dashboard (as a dashboard/themes/*.md theme entry) via the sanctioned
// dashboard package calls ONLY, never touching the theme files as raw
// filesystem paths, and it NEVER executes the task itself. Optionally it
// commissions a free (idle, non-stale) ship by sending it an agent-bus
// directive. See scripts/task-capture (the bash original) for the full
// rationale; this is the consolidated, in-process equivalent.
package task

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/metux/starfleetctl/internal/agentbus"
	"github.com/metux/starfleetctl/internal/dashboard"
)

const usage = `task <command> [args…]

  capture --title "<t>" [options]   capture a task into the dashboard (as a
                                    dashboard/themes/<slug>.md theme) and
                                    optionally commission a free ship to work
                                    it. Never executes the task itself.

Run 'starfleetctl task <command> --help' for command-specific help.
`

// Run dispatches a `task` invocation, given the resolved workspace root.
// Returns the process exit code.
func Run(root string, args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(usage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	switch args[0] {
	case "capture":
		return runCapture(root, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "task: unknown command: %s\n\n%s", args[0], usage)
		return 2
	}
}

const captureUsage = `task capture --title "<t>" [options]

Captures a task into the dashboard (a dashboard/themes/<slug>.md theme entry,
showing up under "Aktive Themen") and optionally commissions a free ship.

Options:
  --title "<t>"        Task title (required).
  --desc  "<text>"     Free-form task description / acceptance criteria.
  --slug  "<slug>"     Override the auto-derived dashboard theme slug.
  --assign [<ship>]    Commission a ship. With no arg, pick the first idle,
                       non-stale ship from the agent-bus board. With a ship
                       name, commission that specific ship.
  --no-push            Stage + commit locally but do not push to origin.
  -h, --help           this help.

Exit codes:
  0  task captured (and assigned, if requested)
  2  bad arguments
  3  slug already exists (collision — pick a different title/slug)
  4  no free ship available for --assign (without explicit ship)
`

// runCapture implements `task capture` — the Go port of scripts/task-capture.
func runCapture(root string, args []string) int {
	title, desc, slug, assign, assignMode, noPush, err := parseCaptureArgs(args)
	if err != nil {
		fmt.Fprintln(os.Stderr, "task capture:", err)
		return 2
	}

	d, err := dashboard.New(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "task capture:", err)
		return 1
	}

	if slug == "" {
		slug = deriveSlug(title)
	}

	// Reserve the slug (refuses if it already exists — collision guard).
	if err := d.DoThemeNew(slug, title, "offen", ""); err != nil {
		fmt.Fprintf(os.Stderr, "task capture: slug already exists: %s\n", slug)
		return 3
	}

	status := "offen"
	assignedTo := "—"

	// Pick a free ship before we write the final frontmatter.
	if assignMode == "auto" {
		ship, perr := pickFreeShip(root)
		if perr != nil {
			fmt.Fprintln(os.Stderr, "task capture:", perr)
			return 1
		}
		if ship == "" {
			fmt.Fprintln(os.Stderr, "task capture: no free (idle, non-stale) ship available — capturing as offen")
			assignMode = ""
		} else {
			assign = ship
			assignMode = ship
		}
	}

	if assignMode != "" {
		status = "beauftragt"
		assignedTo = assign
	}

	// Build the theme file content (frontmatter + body) and write it via the
	// sanctioned dashboard path (never hand-edit the file directly).
	content := buildThemeFile(slug, title, status, assignedTo, desc)
	if err := writeThemeContent(d, slug, content); err != nil {
		fmt.Fprintln(os.Stderr, "task capture:", err)
		return 1
	}

	push := !noPush
	if err := d.DoThemeCommit(slug, "task: "+title, push); err != nil {
		fmt.Fprintln(os.Stderr, "task capture:", err)
		return 1
	}

	// Reindex so the new task shows up in DASHBOARD.md's "Aktive Themen"
	// table, then commit the regenerated index (sanctioned path — never
	// hand-edit it). Best-effort: a malformed sibling theme can break reindex
	// fleet-wide; the task itself is already captured + committed, so don't
	// fail the whole command on a reindex/commit problem — just warn.
	if err := d.DoReindex(); err != nil {
		fmt.Fprintf(os.Stderr, "task capture: dashboard reindex failed (%v) — task %s is captured but not yet in DASHBOARD.md index\n", err, slug)
	} else if err := d.DoCommit("reindex: add task "+slug, push); err != nil {
		fmt.Fprintf(os.Stderr, "task capture: dashboard reindex commit failed (%v) — task %s is captured but DASHBOARD.md index not updated\n", err, slug)
	}

	// Commission the ship (after the dashboard state is durable).
	if assignMode != "" && assign != "" {
		msg := "Neue Aufgabe für dich erfasst: " + title +
			" (Dashboard-Theme `" + slug + "`). Bitte dort Details lesen und abarbeiten. Status danach via agent-bus melden."
		b, berr := agentbus.New(root)
		if berr != nil {
			fmt.Fprintln(os.Stderr, "task capture:", berr)
			return 1
		}
		if _, terr := b.Tell(assign, msg); terr != nil {
			fmt.Fprintln(os.Stderr, "task capture:", terr)
			return 1
		}
	}

	fmt.Printf("task-captured: slug=%s status=%s assigned-to=%s\n", slug, status, assignedTo)
	if assignMode != "" && assign != "" {
		fmt.Printf("commissioned-ship: %s\n", assign)
	}
	return 0
}

// parseCaptureArgs parses `task capture` arguments, mirroring the bash
// original's getopts. Returns the parsed fields and an error on bad args.
func parseCaptureArgs(args []string) (title, desc, slug, assign, assignMode string, noPush bool, err error) {
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--title":
			if i+1 >= len(args) {
				err = fmt.Errorf("--title requires an argument")
				return
			}
			i++
			title = args[i]
		case "--desc":
			if i+1 >= len(args) {
				err = fmt.Errorf("--desc requires an argument")
				return
			}
			i++
			desc = args[i]
		case "--slug":
			if i+1 >= len(args) {
				err = fmt.Errorf("--slug requires an argument")
				return
			}
			i++
			slug = args[i]
		case "--assign":
			if i+1 < len(args) && !strings.HasPrefix(args[i+1], "--") {
				i++
				assign = args[i]
				assignMode = args[i]
			} else {
				assignMode = "auto"
			}
		case "--no-push":
			noPush = true
		case "-h", "--help":
			fmt.Print(captureUsage)
			os.Exit(0)
		default:
			err = fmt.Errorf("unknown argument: %s", args[i])
			return
		}
	}
	if title == "" {
		err = fmt.Errorf("--title is required")
		return
	}
	return
}

// deriveSlug turns a title into a dashboard theme slug: lowercase, ASCII
// alnum-only, dash-separated, namespaced with "task-". Non-ASCII (umlauts
// etc.) collapse to a dash. Mirrors scripts/task-capture's slug derivation.
func deriveSlug(title string) string {
	var core strings.Builder
	prevDash := false
	for _, r := range strings.ToLower(title) {
		if (r >= 'a' && r <= 'z') || (r >= '0' && r <= '9') {
			core.WriteRune(r)
			prevDash = false
		} else if core.Len() > 0 && !prevDash {
			core.WriteByte('-')
			prevDash = true
		}
	}
	s := strings.Trim(core.String(), "-")
	if s == "" {
		return "task-" + strconv.FormatInt(time.Now().Unix(), 10)
	}
	return "task-" + s
}

// pickFreeShip returns the first idle, non-stale ship from the agent-bus
// board, or "" if none is available. Mirrors scripts/task-capture's python
// one-liner (cand[0] of idle+!stale).
func pickFreeShip(root string) (string, error) {
	b, err := agentbus.New(root)
	if err != nil {
		return "", err
	}
	for _, e := range b.BoardEntries() {
		if e.State == "idle" && !e.Stale {
			return e.Agent, nil
		}
	}
	return "", nil
}

// buildThemeFile renders the theme file content (frontmatter + body), matching
// scripts/task-capture's output exactly.
func buildThemeFile(slug, title, status, assignedTo, desc string) string {
	createdBy := os.Getenv("STARFLEET_SHIP_ID")
	if createdBy == "" {
		createdBy = "unknown"
	}
	created := time.Now().UTC().Format(time.RFC3339)

	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "slug: %s\n", slug)
	fmt.Fprintf(&b, "title: \"%s\"\n", title)
	b.WriteString("category: active\n")
	b.WriteString("kind: task\n")
	fmt.Fprintf(&b, "status: %s\n", status)
	fmt.Fprintf(&b, "created-by: %s\n", createdBy)
	fmt.Fprintf(&b, "created: %s\n", created)
	fmt.Fprintf(&b, "assigned-to: %s\n", assignedTo)
	b.WriteString("doc_ref: \"—\"\n")
	b.WriteString("---\n\n")
	b.WriteString(desc)
	b.WriteString("\n")
	return b.String()
}

// writeThemeContent writes the generated theme content to a temp file and
// commits it via the dashboard package's sanctioned write path.
func writeThemeContent(d *dashboard.Dashboard, slug, content string) error {
	tmpDir := filepath.Join(d.Root, "_WORK_", ".tmp")
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return err
	}
	tmp, err := os.CreateTemp(tmpDir, "task.*.md")
	if err != nil {
		return err
	}
	tmpName := tmp.Name()
	defer os.Remove(tmpName)
	if _, err := tmp.WriteString(content); err != nil {
		tmp.Close()
		return err
	}
	if err := tmp.Close(); err != nil {
		return err
	}
	return d.DoThemeWrite(slug, tmpName)
}
