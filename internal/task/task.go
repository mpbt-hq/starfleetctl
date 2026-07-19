// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package task is the Go port of scripts/task-capture — pure commandeering
// helper for the starfleet fleet. It captures a task into the workspace
// dashboard (as a dashboard/topics/*.md topic entry) via the sanctioned
// dashboard package calls ONLY, never touching the topic files as raw
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
	"github.com/metux/starfleetctl/internal/config"
	"github.com/metux/starfleetctl/internal/dashboard"
)

const usage = `task <command> [args…]

  capture --title "<t>" [options]   capture a task into the dashboard (as a
                                     dashboard/topics/<slug>.md topic) and
                                     optionally commission a free ship to work
                                     it. Never executes the task itself.
  assign <slug> [<ship>]            assign an existing task to a ship (or,
                                     with no ship, to the first idle
                                     non-stale ship). Updates status +
                                     assigned-to via the sanctioned
                                     dashboard path and commissions the ship.
  unassign <slug>                   clear a task's assignment (status ->
                                     open, assigned-to -> —).
  status <slug> <status>            set an existing task's status field.

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
	case "assign":
		return runAssign(root, args[1:])
	case "unassign":
		return runUnassign(root, args[1:])
	case "status":
		return runStatus(root, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "task: unknown command: %s\n\n%s", args[0], usage)
		return 2
	}
}

const captureUsage = `task capture --title "<t>" [options]

Captures a task into the dashboard (a dashboard/topics/<slug>.md topic entry,
showing up under "Active Topics") and optionally commissions a free ship.

Options:
  --title "<t>"        Task title (required).
  --desc  "<text>"     Free-form task description / acceptance criteria.
  --slug  "<slug>"     Override the auto-derived dashboard topic slug.
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
	if err := d.DoTopicNew(slug, title, "open", ""); err != nil {
		fmt.Fprintf(os.Stderr, "task capture: slug already exists: %s\n", slug)
		return 3
	}

	status := "open"
	assignedTo := "—"

	// Pick a free ship before we write the final frontmatter.
	if assignMode == "auto" {
		ship, perr := pickFreeShip(root)
		if perr != nil {
			fmt.Fprintln(os.Stderr, "task capture:", perr)
			return 1
		}
		if ship == "" {
			fmt.Fprintln(os.Stderr, "task capture: no free (idle, non-stale) ship available — capturing as open")
			assignMode = ""
		} else {
			assign = ship
			assignMode = ship
		}
	}

	if assignMode != "" {
		status = "assigned"
		assignedTo = assign
	}

	// Build the topic file content (frontmatter + body) and write it via the
	// sanctioned dashboard path (never hand-edit the file directly).
	content := buildTopicFile(slug, title, status, assignedTo, desc)
	if err := writeTopicContent(d, slug, content); err != nil {
		fmt.Fprintln(os.Stderr, "task capture:", err)
		return 1
	}

	push := !noPush
	if err := d.DoTopicCommit(slug, "task: "+title, push); err != nil {
		fmt.Fprintln(os.Stderr, "task capture:", err)
		return 1
	}

	// Reindex so the new task shows up in DASHBOARD.md's "Active Topics"
	// table, then commit the regenerated index (sanctioned path — never
	// hand-edit it). Best-effort: a malformed sibling topic can break reindex
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
			" (Dashboard-Topic `" + slug + "`). Bitte dort Details lesen und abarbeiten. Status danach via agent-bus melden."
		b, berr := agentbus.New(root)
		if berr != nil {
			fmt.Fprintln(os.Stderr, "task capture:", berr)
			return 1
		}
		if _, terr := b.Tell(assign, msg, ""); terr != nil {
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

// deriveSlug turns a title into a dashboard topic slug: lowercase, ASCII
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

// buildTopicFile renders the topic file content (frontmatter + body), matching
// scripts/task-capture's output exactly.
func buildTopicFile(slug, title, status, assignedTo, desc string) string {
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

// writeTopicContent writes the generated topic content to a temp file and
// commits it via the dashboard package's sanctioned write path.
func writeTopicContent(d *dashboard.Dashboard, slug, content string) error {
	tmpDir := filepath.Join(config.WorkDir(d.Root), "tmp")
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
	return d.DoTopicWrite(slug, tmpName)
}

const assignUsage = `task assign <slug> [<ship>] [--no-push]

Assign an existing task to a ship. With no <ship>, commission the first idle,
non-stale ship from the agent-bus board. Updates the topic's status +
assigned-to via the sanctioned dashboard path (no raw file access) and
commissions the ship with an agent-bus directive.

Exit codes:
  0  task assigned + commissioned
  2  bad arguments / unknown option
  3  no such task (slug not found)
  4  no free ship available (with no explicit ship)
`

const unassignUsage = `task unassign <slug> [--no-push]

Clear a task's assignment: status -> open, assigned-to -> —. The topic is
updated via the sanctioned dashboard path.

Exit codes:
  0  task unassigned
  2  bad arguments
  3  no such task (slug not found)
`

const statusUsage = `task status <slug> <status> [--no-push]

Set an existing task's status field (e.g. open, assigned, done) via the
sanctioned dashboard path.

Exit codes:
  0  status updated
  2  bad arguments
  3  no such task (slug not found)
`

// commitAndReindex commits the single topic file (and, best-effort, the
// regenerated DASHBOARD.md index) the same way runCapture does — shared so the
// assign/unassign/status paths keep identical fleet-visibility behaviour.
func commitAndReindex(d *dashboard.Dashboard, slug, msg string, push bool) {
	if err := d.DoTopicCommit(slug, msg, push); err != nil {
		fmt.Fprintf(os.Stderr, "task: topic commit failed: %v\n", err)
		return
	}
	if err := d.DoReindex(); err != nil {
		fmt.Fprintf(os.Stderr, "task: dashboard reindex failed (%v) — task %s updated but not yet in DASHBOARD.md index\n", err, slug)
		return
	}
	if err := d.DoCommit("reindex: update task "+slug, push); err != nil {
		fmt.Fprintf(os.Stderr, "task: dashboard reindex commit failed (%v) — task %s updated but DASHBOARD.md index not updated\n", err, slug)
	}
}

// commissionShip sends the assignment directive to the assigned ship.
// wasAssigned reports whether the task already had an assignee before this
// call, so the message reads as a fresh assignment vs. a reassignment.
func commissionShip(root, slug, title, ship string, wasAssigned bool) error {
	var msg string
	if wasAssigned {
		msg = "Dir wurde die Aufgabe neu zugewiesen: " + title +
			" (Dashboard-Topic `" + slug + "`). Bitte dort Details lesen und abarbeiten. Status danach via agent-bus melden."
	} else {
		msg = "Neue Aufgabe für dich erfasst: " + title +
			" (Dashboard-Topic `" + slug + "`). Bitte dort Details lesen und abarbeiten. Status danach via agent-bus melden."
	}
	b, err := agentbus.New(root)
	if err != nil {
		return err
	}
	if _, err := b.Tell(ship, msg, ""); err != nil {
		return err
	}
	return nil
}

// runAssign implements `task assign <slug> [<ship>] [--no-push]`.
func runAssign(root string, args []string) int {
	noPush := false
	ship := ""
	slug := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--no-push":
			noPush = true
		case "-h", "--help":
			fmt.Print(assignUsage)
			return 0
		default:
			if slug == "" {
				slug = args[i]
			} else if ship == "" {
				ship = args[i]
			} else {
				fmt.Fprintln(os.Stderr, "task assign: too many arguments")
				return 2
			}
		}
	}
	if slug == "" {
		fmt.Fprintln(os.Stderr, "task assign: <slug> required")
		return 2
	}

	d, err := dashboard.New(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "task assign:", err)
		return 1
	}

	// Load the existing topic (sanctioned read path — no raw file access).
	m, body, err := d.DoTopicLoad(slug)
	if err != nil {
		fmt.Fprintf(os.Stderr, "task assign: no such task: %s\n", slug)
		return 3
	}

	assignMode := ship // explicit ship, or "" -> auto-pick
	if assignMode == "" {
		picked, perr := pickFreeShip(root)
		if perr != nil {
			fmt.Fprintln(os.Stderr, "task assign:", perr)
			return 1
		}
		if picked == "" {
			fmt.Fprintln(os.Stderr, "task assign: no free (idle, non-stale) ship available — leaving unassigned")
			return 4
		}
		ship = picked
	}

	wasAssigned := m.AssignedTo != "" && m.AssignedTo != "—"
	m.Status = "assigned"
	m.AssignedTo = ship

	if err := d.DoTopicUpdate(slug, m, body); err != nil {
		fmt.Fprintln(os.Stderr, "task assign:", err)
		return 1
	}
	commitAndReindex(d, slug, "task: assign "+slug+" -> "+ship, !noPush)

	if err := commissionShip(root, slug, m.Title, ship, wasAssigned); err != nil {
		fmt.Fprintln(os.Stderr, "task assign: commission failed:", err)
		return 1
	}

	fmt.Printf("task-assigned: slug=%s status=assigned assigned-to=%s\n", slug, ship)
	return 0
}

// runUnassign implements `task unassign <slug> [--no-push]`.
func runUnassign(root string, args []string) int {
	noPush := false
	slug := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--no-push":
			noPush = true
		case "-h", "--help":
			fmt.Print(unassignUsage)
			return 0
		default:
			if slug == "" {
				slug = args[i]
			} else {
				fmt.Fprintln(os.Stderr, "task unassign: too many arguments")
				return 2
			}
		}
	}
	if slug == "" {
		fmt.Fprintln(os.Stderr, "task unassign: <slug> required")
		return 2
	}

	d, err := dashboard.New(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "task unassign:", err)
		return 1
	}
	m, body, err := d.DoTopicLoad(slug)
	if err != nil {
		fmt.Fprintf(os.Stderr, "task unassign: no such task: %s\n", slug)
		return 3
	}

	m.Status = "open"
	m.AssignedTo = "—"
	if err := d.DoTopicUpdate(slug, m, body); err != nil {
		fmt.Fprintln(os.Stderr, "task unassign:", err)
		return 1
	}
	commitAndReindex(d, slug, "task: unassign "+slug, !noPush)
	fmt.Printf("task-unassigned: slug=%s status=open assigned-to=—\n", slug)
	return 0
}

// runStatus implements `task status <slug> <status> [--no-push]`.
func runStatus(root string, args []string) int {
	noPush := false
	slug := ""
	status := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--no-push":
			noPush = true
		case "-h", "--help":
			fmt.Print(statusUsage)
			return 0
		default:
			if slug == "" {
				slug = args[i]
			} else if status == "" {
				status = args[i]
			} else {
				fmt.Fprintln(os.Stderr, "task status: too many arguments")
				return 2
			}
		}
	}
	if slug == "" || status == "" {
		fmt.Fprintln(os.Stderr, "task status: <slug> and <status> required")
		return 2
	}

	d, err := dashboard.New(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "task status:", err)
		return 1
	}
	m, body, err := d.DoTopicLoad(slug)
	if err != nil {
		fmt.Fprintf(os.Stderr, "task status: no such task: %s\n", slug)
		return 3
	}

	m.Status = status
	if err := d.DoTopicUpdate(slug, m, body); err != nil {
		fmt.Fprintln(os.Stderr, "task status:", err)
		return 1
	}
	commitAndReindex(d, slug, "task: status "+slug+" -> "+status, !noPush)
	fmt.Printf("task-status: slug=%s status=%s\n", slug, status)
	return 0
}

// --- Programmatic wrappers (no os.Exit) for the web UI / in-process callers.
// Each builds the same argument vector a CLI invocation would and routes it
// through the existing run* logic, so the web interface and `starfleetctl task`
// stay behaviour-identical. "__auto__" as the ship means "pick the first idle,
// non-stale ship" (mirrors `--assign` with no arg).

// RunCaptureOnly captures a task. assign == "" means unassigned; "auto" picks a
// free ship; any other value commissions that specific ship. noPush suppresses
// the git push (local-only capture) — used by the web UI so a LAN viewer never
// blocks on a (possibly offline) remote. Returns the exit code (0 == ok) and
// any fatal error.
func RunCaptureOnly(root, title, desc, assign string, noPush bool) (int, error) {
	args := []string{"--title", title}
	if desc != "" {
		args = append(args, "--desc", desc)
	}
	switch assign {
	case "__auto__":
		args = append(args, "--assign")
	case "":
		// unassigned
	default:
		args = append(args, "--assign", assign)
	}
	if noPush {
		args = append(args, "--no-push")
	}
	code := runCapture(root, args)
	if code != 0 {
		return code, fmt.Errorf("task capture exited with code %d", code)
	}
	return 0, nil
}

// RunAssignOnly assigns an existing task to ship ("" / "__auto__" => first free
// ship). noPush suppresses the git push.
func RunAssignOnly(root, slug, ship string, noPush bool) (int, error) {
	args := []string{slug}
	if ship != "" && ship != "__auto__" {
		args = append(args, ship)
	}
	if noPush {
		args = append(args, "--no-push")
	}
	code := runAssign(root, args)
	if code != 0 {
		return code, fmt.Errorf("task assign exited with code %d", code)
	}
	return 0, nil
}

// RunUnassignOnly clears a task's assignment. noPush suppresses the git push.
func RunUnassignOnly(root, slug string, noPush bool) (int, error) {
	args := []string{slug}
	if noPush {
		args = append(args, "--no-push")
	}
	code := runUnassign(root, args)
	if code != 0 {
		return code, fmt.Errorf("task unassign exited with code %d", code)
	}
	return 0, nil
}

// RunCaptureStatus sets an existing task's status field. noPush suppresses the
// git push.
func RunCaptureStatus(root, slug, status string, noPush bool) (int, error) {
	args := []string{slug, status}
	if noPush {
		args = append(args, "--no-push")
	}
	code := runStatus(root, args)
	if code != 0 {
		return code, fmt.Errorf("task status exited with code %d", code)
	}
	return 0, nil
}
