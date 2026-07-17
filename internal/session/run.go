// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package session manages session lifecycle (attach, list, run, autoscale)
// for the fleet — using termctl for terminal management instead of tmux.
package session

import (
	"fmt"
	"os"
	"strings"

	"github.com/X11Libre/go-x11proto/tk/term/termctl"
	"github.com/metux/starfleetctl/internal/agentbus"
)

const usage = `session <command> [args…]

Fleet session lifecycle management.

Commands:
  attach <id> [--read-only] [--independent]
      Resolve <id> to a running terminal and attach the caller's terminal
      to it (replaces scripts/agent-attach).  <id> may be a ship ID,
      a bus-handle, or a unique substring.
  attach --list
      List running terminals and the agent-bus board.
  autoscale <command> [args...]
      On-demand fleet elasticity (status / need).  See 'session autoscale --help'.
  run <release> [flags...] [-- <args...>]
      Launch a detached terminal for an agent/CLI and post the initial
      heartbeat (replaces scripts/agent-run).  Pass --print to emit the
      shell-evaluable launch variables instead.  See 'session run --help'.
  stop <id|session>
      Kill a terminal, clear its agent-bus heartbeat, and release its
      ship name (used by scripts/agent-run --stop).
`

func Run(root string, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, usage)
		return 2
	}
	switch args[0] {
	case "-h", "--help":
		fmt.Print(usage)
		return 0
	case "attach":
		return runAttach(root, args[1:])
	case "autoscale":
		return runAutoscale(root, args[1:])
	case "run":
		return runLaunch(root, args[1:])
	case "stop":
		return runStop(root, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "session: unknown command '%s'\n", args[0])
		return 2
	}
}

func runAttach(root string, args []string) int {
	if len(args) == 0 {
		fmt.Fprint(os.Stderr, "session attach: need <id> or --list\n")
		return 2
	}

	if args[0] == "--list" {
		sessions := ListSessions(root)
		fmt.Println("== running terminals ==")
		if len(sessions) == 0 {
			fmt.Println("(none)")
		} else {
			for _, s := range sessions {
				fmt.Println(s)
			}
		}
		fmt.Println()
		fmt.Println("== agent-bus board ==")
		records := ListBoard(root)
		if len(records) == 0 {
			fmt.Println("(no agents reporting)")
		} else {
			fmt.Printf("%-18s  %-12s  %-10s  %-22s  %s\n", "AGENT", "PROJECT", "STATE", "ATTACH", "NOTE")
			for _, r := range records {
				p := r.Project
				if p == "" {
					p = "-"
				}
				h := r.Handle
				if h == "" {
					h = "-"
				}
				fmt.Printf("%-18s  %-12s  %-10s  %-22s  %s\n", r.Agent, p, r.State, h, r.Note)
			}
		}
		return 0
	}

	if strings.HasPrefix(args[0], "--") {
		fmt.Fprintf(os.Stderr, "session attach: unknown option '%s' (try --list, <id>, or --read-only/--independent)\n", args[0])
		return 2
	}

	id := args[0]
	mode := "shared"
	for _, a := range args[1:] {
		switch a {
		case "--read-only":
			mode = "ro"
		case "--independent":
			mode = "ind"
		case "-h", "--help":
			fmt.Print("session attach <id> [--read-only] [--independent]\n")
			return 0
		default:
			fmt.Fprintf(os.Stderr, "session attach: unknown flag '%s'\n", a)
			return 2
		}
	}

	pipePath, ok := resolvePipe(root, id)
	if !ok {
		fmt.Fprintf(os.Stderr, "agent-attach: no running session matches '%s' (try: session attach --list)\n", id)
		return 1
	}

	if err := attachPipe(pipePath, mode); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// attachPipe connects the caller's terminal to a running terminal via its
// termctl control pipe. mode is one of "shared" (rw attach), "ro" (read-only
// attach - not supported by termctl directly, falls back to shared), "ind"
// (independent - not supported, falls back to shared).
func attachPipe(pipePath, mode string) error {
	rem, err := termctl.OpenPipe(pipePath)
	if err != nil {
		return fmt.Errorf("open pipe %s: %w", pipePath, err)
	}

	// termctl only supports full attach; read-only/independent not available.
	if mode != "shared" {
		fmt.Fprintf(os.Stderr, "agent-attach: mode %q not supported by termctl, using shared\n", mode)
	}

	display := os.Getenv("DISPLAY")
	if display == "" {
		display = ":0"
	}

	fmt.Printf("agent-attach: attaching to terminal via pipe %s (mode: shared) on display %s. Detach with SIGUSR1 or 'detach' via control pipe; the agent keeps running.\n", pipePath, display)

	if err := rem.Attach(display); err != nil {
		return fmt.Errorf("attach: %w", err)
	}
	return nil
}

// resolvePipe finds the termctl pipe path for a given ID. ID can be a ship ID
// (registry key), a bus handle, or a substring.
func resolvePipe(root, id string) (string, bool) {
	// 1. Direct registry lookup by ship ID
	reg := NewRegistry(root)
	if pipe, ok := reg.Get(id); ok {
		return pipe, true
	}

	// 2. Check agent-bus board for matching agent/handle
	bus, err := agentbus.New(root)
	if err != nil {
		return "", false
	}
	for _, r := range bus.AllStatusRecords() {
		if r.Agent == id || r.Handle == id || strings.Contains(r.Agent, id) || strings.Contains(r.Handle, id) {
			if pipe, ok := reg.Get(r.Agent); ok {
				return pipe, true
			}
		}
	}
	return "", false
}