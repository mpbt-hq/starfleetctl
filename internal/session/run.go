// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package session manages session lifecycle (attach, list, run, autoscale)
// for the fleet — including the tmux launch/attach steps that scripts/agent-run
// and scripts/agent-attach used to perform.  See also scripts/fleet-autoscale.
package session

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const usage = `session <command> [args…]

Fleet session lifecycle management.

Commands:
  attach <id> [--read-only] [--independent]
      Resolve <id> to a running tmux session and attach the caller's terminal
      to it (replaces scripts/agent-attach).  <id> may be an exact tmux
      session, an agent ID, a bus-handle, or a unique substring.
  attach --list
      List running mpbt- tmux sessions and the agent-bus board.
  autoscale <command> [args...]
      On-demand fleet elasticity (status / need).  See 'session autoscale --help'.
  run <release> [flags...] [-- <args...>]
      Launch a detached tmux session for an agent/CLI and post the initial
      heartbeat (replaces scripts/agent-run).  Pass --print to emit the
      shell-evaluable launch variables instead.  See 'session run --help'.
  stop <id|session>
      Kill a tmux session, clear its agent-bus heartbeat, and release its
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
		sessions := ListSessions()
		fmt.Println("== running mpbt tmux sessions ==")
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

	session := ResolveID(root, id)
	if session == "" {
		fmt.Fprintf(os.Stderr, "agent-attach: no running session matches '%s' (try: session attach --list)\n", id)
		return 1
	}

	if err := attachSession(session, mode); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}
	return 0
}

// attachSession connects the caller's terminal to a running tmux session,
// replacing what scripts/agent-attach did via `exec tmux attach`. mode is one
// of "shared" (rw attach), "ro" (read-only attach), "ind" (own grouped window
// with an independent size).
func attachSession(session, mode string) error {
	if _, err := exec.LookPath("tmux"); err != nil {
		return fmt.Errorf("tmux not found — install it (e.g. 'sudo apt install tmux')")
	}
	if fi, err := os.Stdout.Stat(); err != nil || fi.Mode()&os.ModeCharDevice == 0 {
		return fmt.Errorf("agent-attach: not a terminal — attach from an interactive shell")
	}

	var targs []string
	switch mode {
	case "ro":
		targs = []string{"attach", "-r", "-t", session}
	case "ind":
		targs = []string{"new-session", "-t", session, "-s", fmt.Sprintf("%s-view-%d", session, os.Getpid())}
	default: // shared
		targs = []string{"attach", "-t", session}
	}

	fmt.Printf("agent-attach: attaching to '%s' (mode: %s). Detach with Ctrl-b d; the agent keeps running.\n", session, mode)

	cmd := exec.Command("tmux", targs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}
