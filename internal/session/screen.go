// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package session manages session lifecycle (attach, list, run, autoscale)
// for the fleet — using termctl for terminal management.
package session

import (
	"encoding/json"
	"fmt"
	"os"
	"time"

	"github.com/X11Libre/go-x11proto/tk/term/termctl"
)

const screenUsage = `session screen <command> [args...]

Terminal screen dump commands.

Commands:
  dump <id>             Dump the current visible screen content as plain text
  dump-json <id>        Dump the current visible screen content as JSON array
  scrollback <id> [n]   Dump the n most recent scrollback lines (default: 100)
  watch <id> [interval] Watch screen content with auto-refresh (default: 1s)
  status <id>           Show terminal dimensions and attachment status
`

func runScreen(root string, args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(screenUsage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}

	switch args[0] {
	case "dump":
		return runScreenDump(root, args[1:], false)
	case "dump-json":
		return runScreenDump(root, args[1:], true)
	case "scrollback":
		return runScreenScrollback(root, args[1:])
	case "watch":
		return runScreenWatch(root, args[1:])
	case "status":
		return runScreenStatus(root, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "session screen: unknown command '%s'\n", args[0])
		return 2
	}
}

func runScreenDump(root string, args []string, asJSON bool) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "session screen dump: need <id>")
		return 2
	}

	id := args[0]
	pipePath, ok := resolvePipe(root, id)
	if !ok {
		fmt.Fprintf(os.Stderr, "session screen dump: no running session matches '%s'\n", id)
		return 1
	}

	lines, err := screenDump(pipePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "session screen dump: %v\n", err)
		return 1
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(lines); err != nil {
			fmt.Fprintf(os.Stderr, "session screen dump: JSON encode: %v\n", err)
			return 1
		}
	} else {
		for _, line := range lines {
			fmt.Println(line)
		}
	}
	return 0
}

func runScreenScrollback(root string, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "session screen scrollback: need <id>")
		return 2
	}

	id := args[0]
	n := 100
	if len(args) > 1 {
		if _, err := fmt.Sscanf(args[1], "%d", &n); err != nil {
			fmt.Fprintf(os.Stderr, "session screen scrollback: invalid count '%s'\n", args[1])
			return 2
		}
	}

	pipePath, ok := resolvePipe(root, id)
	if !ok {
		fmt.Fprintf(os.Stderr, "session screen scrollback: no running session matches '%s'\n", id)
		return 1
	}

	lines, err := screenDumpScrollback(pipePath, n)
	if err != nil {
		fmt.Fprintf(os.Stderr, "session screen scrollback: %v\n", err)
		return 1
	}

	for _, line := range lines {
		fmt.Println(line)
	}
	return 0
}

func runScreenWatch(root string, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "session screen watch: need <id>")
		return 2
	}

	id := args[0]
	interval := time.Second
	if len(args) > 1 {
		var err error
		interval, err = time.ParseDuration(args[1])
		if err != nil {
			fmt.Fprintf(os.Stderr, "session screen watch: invalid interval '%s': %v\n", args[1], err)
			return 2
		}
	}

	pipePath, ok := resolvePipe(root, id)
	if !ok {
		fmt.Fprintf(os.Stderr, "session screen watch: no running session matches '%s'\n", id)
		return 1
	}

	fmt.Fprintf(os.Stderr, "Watching screen for %s (Ctrl+C to stop)...\n", id)
	for {
		lines, err := screenDump(pipePath)
		if err != nil {
			fmt.Fprintf(os.Stderr, "\nError: %v\n", err)
			return 1
		}

		// Clear screen and move cursor to top-left
		fmt.Print("\033[2J\033[H")
		for _, line := range lines {
			fmt.Println(line)
		}
		time.Sleep(interval)
	}
}

func runScreenStatus(root string, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "session screen status: need <id>")
		return 2
	}

	id := args[0]
	pipePath, ok := resolvePipe(root, id)
	if !ok {
		fmt.Fprintf(os.Stderr, "session screen status: no running session matches '%s'\n", id)
		return 1
	}

	// Note: Status() doesn't work over FIFO pipes, but we can still show the pipe path
	fmt.Printf("Ship:     %s\n", id)
	fmt.Printf("Pipe:     %s\n", pipePath)
	fmt.Printf("Terminal: (use 'session attach %s' to attach)\n", id)
	return 0
}

// screenDump sends a 'dump' command to the termctl pipe and reads the response.
// Since FIFO pipes don't support responses, we use a temporary file approach.
func screenDump(pipePath string) ([]string, error) {
	rem, err := termctl.OpenPipe(pipePath)
	if err != nil {
		return nil, fmt.Errorf("open pipe: %w", err)
	}

	// Create a temporary file for the response. Remove it first so the
	// read loop below does not pick up a stale/empty file from a previous
	// (failed) call — os.CreateTemp would leave it empty and the first
	// read would fail with "unexpected end of JSON input".
	tmpFile, err := os.CreateTemp("", "screen-dump-*.json")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	os.Remove(tmpPath)
	defer os.Remove(tmpPath)

	// Send dump command with response path
	if err := rem.Send(fmt.Sprintf("dump-to %s", tmpPath)); err != nil {
		return nil, fmt.Errorf("send dump command: %w", err)
	}

	// Wait for the response file to appear with non-empty content (timeout).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(tmpPath)
		if err == nil && len(data) > 0 {
			var lines []string
			if err := json.Unmarshal(data, &lines); err != nil {
				return nil, fmt.Errorf("parse response: %w", err)
			}
			return lines, nil
		}
		time.Sleep(50 * time.Millisecond)
	}

	return nil, fmt.Errorf("timeout waiting for screen dump response")
}

// screenDumpScrollback sends a 'dump-scrollback' command to the termctl pipe.
func screenDumpScrollback(pipePath string, n int) ([]string, error) {
	rem, err := termctl.OpenPipe(pipePath)
	if err != nil {
		return nil, fmt.Errorf("open pipe: %w", err)
	}

	// Create a temporary file for the response. Remove it first so the read
	// loop does not pick up a stale/empty file from a previous failed call.
	tmpFile, err := os.CreateTemp("", "screen-scrollback-*.json")
	if err != nil {
		return nil, fmt.Errorf("create temp file: %w", err)
	}
	tmpPath := tmpFile.Name()
	tmpFile.Close()
	os.Remove(tmpPath)
	defer os.Remove(tmpPath)

	// Send dump-scrollback command with response path
	if err := rem.Send(fmt.Sprintf("dump-scrollback %d %s", n, tmpPath)); err != nil {
		return nil, fmt.Errorf("send dump-scrollback command: %w", err)
	}

	// Wait for the response file to appear with non-empty content (timeout).
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		data, err := os.ReadFile(tmpPath)
		if err == nil && len(data) > 0 {
			var lines []string
			if err := json.Unmarshal(data, &lines); err != nil {
				return nil, fmt.Errorf("parse response: %w", err)
			}
			return lines, nil
		}
		time.Sleep(50 * time.Millisecond)
	}

	return nil, fmt.Errorf("timeout waiting for scrollback dump response")
}

// ScreenDump is the exported version of screenDump for use by other packages.
func ScreenDump(pipePath string) ([]string, error) {
	return screenDump(pipePath)
}

// ScreenDumpScrollback is the exported version of screenDumpScrollback for use by other packages.
func ScreenDumpScrollback(pipePath string, n int) ([]string, error) {
	return screenDumpScrollback(pipePath, n)
}
