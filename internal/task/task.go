// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package task is the Go port of scripts/task-capture — pure commandeering
// helper for the starfleet fleet. It captures a task into the workspace
// dashboard (as a dashboard/topics/*.md topic entry) via the sanctioned
// dashboard package calls ONLY, never touching the topic files as raw
// filesystem paths, and it NEVER executes the task itself. Optionally it
// commissions a ship by sending it a comms directive; auto-assignment routes
// to the flagship, which may delegate the work to a worker ship (or execute
// it itself). See scripts/task-capture (the bash original) for the full
// rationale; this is the consolidated, in-process equivalent.
package task

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/metux/starfleetctl/internal/comms"
	"github.com/metux/starfleetctl/internal/config"
	"github.com/metux/starfleetctl/internal/dashboard"
	"github.com/metux/starfleetctl/internal/shipnames"
)

const usage = `task <command> [args…]

  capture --title "<t>" [options]   capture a task into the dashboard (as a
                                      dashboard/topics/<slug>.md topic) and
                                      optionally commission a ship to work
                                      it. Never executes the task itself.
  assign <slug> [<ship>]            assign an existing task to a ship (or,
                                      with no ship, to the flagship, which
                                      delegates or executes it). Updates status
                                      + assigned-to via the sanctioned
                                      dashboard path and commissions the ship.
  unassign <slug>                   clear a task's assignment (status ->
                                      open, assigned-to -> —).
  status <slug> <status>            set an existing task's status field.
  begin <slug>                      start working on a task: verifies
                                      assignment to this ship, sets status
                                      in-progress, logs timestamp, updates
                                      comms status (working + task + progress).
  log <slug> <text>                 append timestamped work-log entry to task
                                      body, commit + reindex.
  progress <slug> <0-100> [note]    update progress in frontmatter + log +
                                      comms status (working + task + progress).
  done <slug>                       complete a task: status=done + log +
                                      comms status idle.
  rm <slug>                         delete a task topic from the dashboard.
  purge [--no-push]                 delete ALL tasks with status "done".

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
	case "begin":
		return runBegin(root, args[1:])
	case "log":
		return runLog(root, args[1:])
	case "progress":
		return runProgress(root, args[1:])
	case "done":
		return runDone(root, args[1:])
	case "rm", "remove", "delete":
		return runRm(root, args[1:])
	case "purge":
		return runPurge(root, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "task: unknown command: %s\n\n%s", args[0], usage)
		return 2
	}
}

const captureUsage = `task capture --title "<t>" [options]

Captures a task into the dashboard (a dashboard/topics/<slug>.md topic entry,
showing up under "Active Topics") and optionally commissions a ship.

Options:
  --title "<t>"        Task title (required).
  --desc  "<text>"     Free-form task description / acceptance criteria.
  --slug  "<slug>"     Override the auto-derived dashboard topic slug.
  --assign [<ship>]    Commission a ship. With no arg, route the task to the
                       flagship, which delegates it to a worker (or executes
                       it itself). With a ship name, commission that specific
                       ship.
  --category <cat>     Dashboard topic category (default: active).
  --no-push            Stage + commit locally but do not push to origin.
  -h, --help           this help.

Exit codes:
  0  task captured (and assigned, if requested)
  2  bad arguments
  3  slug already exists (collision — pick a different title/slug)
`

// runCapture implements `task capture` — the Go port of scripts/task-capture.
func runCapture(root string, args []string) int {
	title, desc, slug, assign, assignMode, category, noPush, err := parseCaptureArgs(args)
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

	// Route the task to the flagship before we write the final frontmatter.
	if assignMode == "auto" {
		// Auto-assign targets the flagship, which then delegates the work to
		// a suitable worker (or executes it itself). Never blind-pick the
		// first idle board entry: that could pick the calling ship/console
		// itself (a self-assignment) and skips the coordinator.
		assign = shipnames.FlagshipName(root)
		assignMode = assign
	}

	if assignMode != "" {
		status = "assigned"
		assignedTo = assign
	}

	// Build the topic file content (frontmatter + body) and write it via the
	// sanctioned dashboard path (never hand-edit the file directly).
	content := buildTopicFile(slug, title, status, assignedTo, desc, category)
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
		if cerr := commissionShip(root, slug, title, assign, false); cerr != nil {
			fmt.Fprintln(os.Stderr, "task capture:", cerr)
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
func parseCaptureArgs(args []string) (title, desc, slug, assign, assignMode, category string, noPush bool, err error) {
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
		case "--category":
			if i+1 >= len(args) {
				err = fmt.Errorf("--category requires an argument")
				return
			}
			i++
			category = args[i]
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

// buildTopicFile renders the topic file content (frontmatter + body), matching
// scripts/task-capture's output exactly.
func buildTopicFile(slug, title, status, assignedTo, desc, category string) string {
	createdBy := os.Getenv("STARFLEET_SHIP_ID")
	if createdBy == "" {
		createdBy = "unknown"
	}
	created := time.Now().UTC().Format(time.RFC3339)

	if category == "" {
		category = "active"
	}

	var b strings.Builder
	b.WriteString("---\n")
	fmt.Fprintf(&b, "slug: %s\n", slug)
	fmt.Fprintf(&b, "title: \"%s\"\n", title)
	fmt.Fprintf(&b, "category: %s\n", category)
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

Assign an existing task to a ship. With no <ship>, route the task to the
flagship, which delegates it to a worker (or executes it itself). Updates the
topic's status + assigned-to via the sanctioned dashboard path (no raw file
access) and commissions the ship with an comms directive.

Exit codes:
  0  task assigned + commissioned
  2  bad arguments / unknown option
  3  no such task (slug not found)
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
// call, so the message reads as a fresh assignment vs. a reassignment. When
// the target is the flagship, the message explicitly notes that it may
// delegate the task to a worker ship instead of executing it itself.
func commissionShip(root, slug, title, ship string, wasAssigned bool) error {
	delegate := " (Dashboard-Topic `" + slug + "`). Du kannst sie selbst bearbeiten" +
		" oder an einen freien Worker delegieren (z.B. via `task assign " + slug + " <ship>`)." +
		" Status danach via comms melden."
	var msg string
	if ship == shipnames.FlagshipName(root) {
		if wasAssigned {
			msg = "Dir wurde die Aufgabe neu zugewiesen: " + title + delegate
		} else {
			msg = "Neue Aufgabe für dich erfasst: " + title + delegate
		}
	} else if wasAssigned {
		msg = "Dir wurde die Aufgabe neu zugewiesen: " + title +
			" (Dashboard-Topic `" + slug + "`). Bitte dort Details lesen und abarbeiten. Status danach via comms melden."
	} else {
		msg = "Neue Aufgabe für dich erfasst: " + title +
			" (Dashboard-Topic `" + slug + "`). Bitte dort Details lesen und abarbeiten. Status danach via comms melden."
	}
	b, err := comms.New(root)
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

	assignMode := ship // explicit ship, or "" -> flagship for delegation
	if assignMode == "" {
		// Auto-assign targets the flagship, which then delegates the work to
		// a suitable worker (or executes it itself). Never blind-pick the
		// first idle board entry: that could pick the calling ship/console
		// itself (a self-assignment) and skips the coordinator.
		ship = shipnames.FlagshipName(root)
		assignMode = ship
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

const rmUsage = `task rm <slug>

Delete a task topic from the dashboard. Removes the topic file under
dashboard/topics/ and reindexes DASHBOARD.md.

Exit codes:
  0  task removed
  2  bad arguments
  3  no such task (slug not found)
`

const purgeUsage = `task purge [--no-push]

Delete ALL tasks with status "done" from the dashboard. Removes topic files
and reindexes DASHBOARD.md. Shows which tasks were deleted.

Exit codes:
  0  purge completed (may be zero tasks deleted)
  2  bad arguments
`

// runRm implements `task rm <slug>` — remove a single topic file.
func runRm(root string, args []string) int {
	noPush := false
	slug := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--no-push":
			noPush = true
		case "-h", "--help":
			fmt.Print(rmUsage)
			return 0
		default:
			if slug == "" {
				slug = args[i]
			} else {
				fmt.Fprintln(os.Stderr, "task rm: too many arguments")
				return 2
			}
		}
	}
	if slug == "" {
		fmt.Fprintln(os.Stderr, "task rm: <slug> required")
		return 2
	}

	d, err := dashboard.New(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "task rm:", err)
		return 1
	}

	// Verify existence (file on disk or in git index).
	if err := d.DoTopicVerify(slug); err != nil {
		fmt.Fprintf(os.Stderr, "task rm: no such task: %s\n", slug)
		return 3
	}

	if err := d.DoTopicDelete(slug); err != nil {
		fmt.Fprintf(os.Stderr, "task rm: %v\n", err)
		return 1
	}

	commitAndReindex(d, slug, "task: rm "+slug, !noPush)
	fmt.Printf("task-removed: slug=%s\n", slug)
	return 0
}

// runPurge implements `task purge` — remove all done tasks.
func runPurge(root string, args []string) int {
	noPush := false
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--no-push":
			noPush = true
		case "-h", "--help":
			fmt.Print(purgeUsage)
			return 0
		default:
			fmt.Fprintln(os.Stderr, "task purge: unknown argument:", args[i])
			return 2
		}
	}

	d, err := dashboard.New(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "task purge:", err)
		return 1
	}

	metas, err := d.LoadAllTopics()
	if err != nil {
		fmt.Fprintln(os.Stderr, "task purge:", err)
		return 1
	}

	var toDelete []string
	for _, m := range metas {
		if m.Kind == "task" && m.Status == "done" {
			toDelete = append(toDelete, m.Slug)
		}
	}

	if len(toDelete) == 0 {
		fmt.Println("task purge: no done tasks to remove")
		return 0
	}

	fmt.Printf("Removing %d done task(s):\n", len(toDelete))
	for _, slug := range toDelete {
		if err := d.DoTopicDelete(slug); err != nil {
			fmt.Fprintf(os.Stderr, "task purge: failed to remove %s: %v\n", slug, err)
			return 1
		}
		fmt.Printf("  — %s\n", slug)
	}

	// Single batch commit + reindex for all deletions.
	push := !noPush
	msg := fmt.Sprintf("task: purge %d done tasks", len(toDelete))
	if err := d.DoTopicCommitAll(msg, push); err != nil {
		fmt.Fprintf(os.Stderr, "task purge: commit failed: %v\n", err)
		return 1
	}
	if err := d.DoReindex(); err != nil {
		fmt.Fprintf(os.Stderr, "task purge: reindex failed: %v\n", err)
		return 1
	}

	fmt.Println("task-purge: done")
	return 0
}

// --- Programmatic wrappers (no os.Exit) for the web UI / in-process callers.
// Each builds the same argument vector a CLI invocation would and routes it
// through the existing run* logic, so the web interface and `starfleetctl task`
// stay behaviour-identical. "__auto__" as the ship means "route to the
// flagship for delegation" (mirrors `--assign` with no arg).

// RunCaptureOnly captures a task. assign == "" means unassigned; "auto" routes
// to the flagship; any other value commissions that specific ship. noPush
// suppresses the git push (local-only capture) — used by the web UI so a LAN
// viewer never blocks on a (possibly offline) remote. category is the dashboard
// topic category (default "active"). Returns the exit code (0 == ok) and any
// fatal error.
func RunCaptureOnly(root, title, desc, assign, category string, noPush bool) (int, error) {
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
	if category != "" {
		args = append(args, "--category", category)
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

// RunAssignOnly assigns an existing task to ship ("" / "__auto__" => flagship,
// which delegates or executes it). noPush suppresses the git push.
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

// --- Task lifecycle commands (begin/log/progress/done) ---

const beginUsage = `task begin <slug> [--no-push]

Start working on a task. Verifies the task is assigned to this ship (or unassigned
and this ship claims it), sets status=in-progress, appends a timestamped log
entry, and updates comms status to working with --task and --progress 0.

Exit codes:
  0  task started
  2  bad arguments
  3  no such task / not assigned to this ship
`

func runBegin(root string, args []string) int {
	noPush := false
	slug := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--no-push":
			noPush = true
		case "-h", "--help":
			fmt.Print(beginUsage)
			return 0
		default:
			if slug == "" {
				slug = args[i]
			} else {
				fmt.Fprintln(os.Stderr, "task begin: too many arguments")
				return 2
			}
		}
	}
	if slug == "" {
		fmt.Fprintln(os.Stderr, "task begin: <slug> required")
		return 2
	}

	d, err := dashboard.New(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "task begin:", err)
		return 1
	}

	m, body, err := d.DoTopicLoad(slug)
	if err != nil {
		fmt.Fprintf(os.Stderr, "task begin: no such task: %s\n", slug)
		return 3
	}

	shipID := os.Getenv("STARFLEET_SHIP_ID")
	if shipID == "" {
		fmt.Fprintln(os.Stderr, "task begin: STARFLEET_SHIP_ID not set")
		return 1
	}

	if m.AssignedTo == "" || m.AssignedTo == "—" {
		// Unassigned: this ship claims it
		m.AssignedTo = shipID
	} else if m.AssignedTo != shipID {
		fmt.Fprintf(os.Stderr, "task begin: task assigned to %s, not %s\n", m.AssignedTo, shipID)
		return 3
	}

	m.Status = "in-progress"
	now := time.Now().UTC().Format(time.RFC3339)
	body = fmt.Sprintf("%s\n- %s %s: began work\n", body, now, shipID)

	if err := d.DoTopicUpdate(slug, m, body); err != nil {
		fmt.Fprintln(os.Stderr, "task begin:", err)
		return 1
	}
	commitAndReindex(d, slug, "task: begin "+slug, !noPush)

	// Update comms status
	b, err := comms.New(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "task begin: comms:", err)
		return 1
	}
	if err := b.DoStatus("working", "task: "+m.Title, comms.StatusPatch{
		Task:       slug,
		Progress:   0,
		LaunchType: "task",
	}); err != nil {
		fmt.Fprintln(os.Stderr, "task begin: comms status:", err)
		return 1
	}

	fmt.Printf("task-begin: slug=%s status=in-progress ship=%s\n", slug, shipID)
	return 0
}

const logUsage = `task log <slug> <text> [--no-push]

Append a timestamped work-log entry to the task body. Commits and reindexes.

Exit codes:
  0  log entry added
  2  bad arguments
  3  no such task
`

func runLog(root string, args []string) int {
	noPush := false
	slug := ""
	text := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--no-push":
			noPush = true
		case "-h", "--help":
			fmt.Print(logUsage)
			return 0
		default:
			if slug == "" {
				slug = args[i]
			} else if text == "" {
				text = args[i]
			} else {
				text += " " + args[i]
			}
		}
	}
	if slug == "" || text == "" {
		fmt.Fprintln(os.Stderr, "task log: <slug> and <text> required")
		return 2
	}

	d, err := dashboard.New(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "task log:", err)
		return 1
	}

	m, body, err := d.DoTopicLoad(slug)
	if err != nil {
		fmt.Fprintf(os.Stderr, "task log: no such task: %s\n", slug)
		return 3
	}

	shipID := os.Getenv("STARFLEET_SHIP_ID")
	if shipID == "" {
		shipID = "unknown"
	}
	now := time.Now().UTC().Format(time.RFC3339)
	body = fmt.Sprintf("%s\n- %s %s: %s\n", body, now, shipID, text)

	if err := d.DoTopicUpdate(slug, m, body); err != nil {
		fmt.Fprintln(os.Stderr, "task log:", err)
		return 1
	}
	commitAndReindex(d, slug, "task: log "+slug, !noPush)

	fmt.Printf("task-log: slug=%s\n", slug)
	return 0
}

const progressUsage = `task progress <slug> <0-100> [note] [--no-push]

Update progress percentage in frontmatter, append log entry, update comms status.

Exit codes:
  0  progress updated
  2  bad arguments
  3  no such task
`

func runProgress(root string, args []string) int {
	noPush := false
	slug := ""
	progress := ""
	note := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--no-push":
			noPush = true
		case "-h", "--help":
			fmt.Print(progressUsage)
			return 0
		default:
			if slug == "" {
				slug = args[i]
			} else if progress == "" {
				progress = args[i]
			} else {
				if note == "" {
					note = args[i]
				} else {
					note += " " + args[i]
				}
			}
		}
	}
	if slug == "" || progress == "" {
		fmt.Fprintln(os.Stderr, "task progress: <slug> <0-100> required")
		return 2
	}

	prog, err := strconv.Atoi(progress)
	if err != nil || prog < 0 || prog > 100 {
		fmt.Fprintln(os.Stderr, "task progress: progress must be 0-100")
		return 2
	}

	d, err := dashboard.New(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "task progress:", err)
		return 1
	}

	m, body, err := d.DoTopicLoad(slug)
	if err != nil {
		fmt.Fprintf(os.Stderr, "task progress: no such task: %s\n", slug)
		return 3
	}

	shipID := os.Getenv("STARFLEET_SHIP_ID")
	if shipID == "" {
		shipID = "unknown"
	}
	now := time.Now().UTC().Format(time.RFC3339)
	logEntry := fmt.Sprintf("- %s %s: progress %d%%", now, shipID, prog)
	if note != "" {
		logEntry += " (" + note + ")"
	}
	body = fmt.Sprintf("%s\n%s\n", body, logEntry)

	if err := d.DoTopicUpdate(slug, m, body); err != nil {
		fmt.Fprintln(os.Stderr, "task progress:", err)
		return 1
	}
	commitAndReindex(d, slug, fmt.Sprintf("task: progress %s %d%%", slug, prog), !noPush)

	// Update comms status
	b, err := comms.New(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "task progress: comms:", err)
		return 1
	}
	if err := b.DoStatus("working", "task: "+m.Title, comms.StatusPatch{
		Task:       slug,
		Progress:   prog,
		LaunchType: "task",
	}); err != nil {
		fmt.Fprintln(os.Stderr, "task progress: comms status:", err)
		return 1
	}

	fmt.Printf("task-progress: slug=%s progress=%d%%\n", slug, prog)
	return 0
}

const doneUsage = `task done <slug> [--no-push]

Complete a task: sets status=done, appends completion log, updates comms status to idle.

Exit codes:
  0  task completed
  2  bad arguments
  3  no such task
`

func runDone(root string, args []string) int {
	noPush := false
	slug := ""
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--no-push":
			noPush = true
		case "-h", "--help":
			fmt.Print(doneUsage)
			return 0
		default:
			if slug == "" {
				slug = args[i]
			} else {
				fmt.Fprintln(os.Stderr, "task done: too many arguments")
				return 2
			}
		}
	}
	if slug == "" {
		fmt.Fprintln(os.Stderr, "task done: <slug> required")
		return 2
	}

	d, err := dashboard.New(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "task done:", err)
		return 1
	}

	m, body, err := d.DoTopicLoad(slug)
	if err != nil {
		fmt.Fprintf(os.Stderr, "task done: no such task: %s\n", slug)
		return 3
	}

	shipID := os.Getenv("STARFLEET_SHIP_ID")
	if shipID == "" {
		shipID = "unknown"
	}
	now := time.Now().UTC().Format(time.RFC3339)
	body = fmt.Sprintf("%s\n- %s %s: completed\n", body, now, shipID)

	m.Status = "done"
	if err := d.DoTopicUpdate(slug, m, body); err != nil {
		fmt.Fprintln(os.Stderr, "task done:", err)
		return 1
	}
	commitAndReindex(d, slug, "task: done "+slug, !noPush)

	// Update comms status to idle
	b, err := comms.New(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "task done: comms:", err)
		return 1
	}
	if err := b.DoStatus("idle", "task done: "+m.Title, comms.StatusPatch{
		Task:       "",
		Progress:   -1,
		LaunchType: "task",
	}); err != nil {
		fmt.Fprintln(os.Stderr, "task done: comms status:", err)
		return 1
	}

	fmt.Printf("task-done: slug=%s status=done\n", slug)
	return 0
}

// RunBeginOnly starts working on a task.
func RunBeginOnly(root, slug string, noPush bool) (int, error) {
	args := []string{slug}
	if noPush {
		args = append(args, "--no-push")
	}
	code := runBegin(root, args)
	if code != 0 {
		return code, fmt.Errorf("task begin exited with code %d", code)
	}
	return 0, nil
}

// RunLogOnly appends a log entry to a task.
func RunLogOnly(root, slug, text string, noPush bool) (int, error) {
	args := []string{slug, text}
	if noPush {
		args = append(args, "--no-push")
	}
	code := runLog(root, args)
	if code != 0 {
		return code, fmt.Errorf("task log exited with code %d", code)
	}
	return 0, nil
}

// RunProgressOnly updates task progress.
func RunProgressOnly(root, slug string, progress int, note string, noPush bool) (int, error) {
	args := []string{slug, strconv.Itoa(progress)}
	if note != "" {
		args = append(args, note)
	}
	if noPush {
		args = append(args, "--no-push")
	}
	code := runProgress(root, args)
	if code != 0 {
		return code, fmt.Errorf("task progress exited with code %d", code)
	}
	return 0, nil
}

// RunDoneOnly completes a task.
func RunDoneOnly(root, slug string, noPush bool) (int, error) {
	args := []string{slug}
	if noPush {
		args = append(args, "--no-push")
	}
	code := runDone(root, args)
	if code != 0 {
		return code, fmt.Errorf("task done exited with code %d", code)
	}
	return 0, nil
}
