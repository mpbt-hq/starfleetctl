// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package bridged

import (
	"fmt"
	"os"
	"path/filepath"
	"time"

	"github.com/metux/starfleetctl/internal/config"
)

const usage = `bridged <command> [args…]

  run [--socket <path>]      listen and serve (foreground — daemonize it
                              yourself via your preferred method: systemd,
                              nohup, screen, etc.; bridged does not
                              self-daemonize)
  status [--socket <path>]   connect and report whether a daemon is up

Default socket: .starfleet-ai/var/comms/bridged.sock

NOT wired into any hook/script/existing caller — this is a new, additive,
opt-in access path only. See DASHBOARD.md "Long-term fleet architecture
vision" for the full design writeup.
`

// DefaultSockPath returns the default bridged socket path for a workspace root.
func DefaultSockPath(root string) string {
	return filepath.Join(config.BusDir(root), "bridged.sock")
}

// Run implements `starfleetctl bridged <command>`.
func Run(root string, args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(usage)
		if len(args) == 0 {
			return 2
		}
		return 0
	}

	sockPath := DefaultSockPath(root)
	rest := args[1:]
	for i := 0; i < len(rest); i++ {
		if rest[i] == "--socket" && i+1 < len(rest) {
			sockPath = rest[i+1]
			i++
		}
	}

	switch args[0] {
	case "run":
		if dir := filepath.Dir(sockPath); dir != "." {
			if err := os.MkdirAll(dir, 0o755); err != nil {
				fmt.Fprintln(os.Stderr, "bridged:", err)
				return 1
			}
		}
		if err := ListenAndServe(root, sockPath); err != nil {
			fmt.Fprintln(os.Stderr, "bridged:", err)
			return 1
		}
		return 0

	case "status":
		resp, err := Call(sockPath, Request{Cmd: "ping"}, 2*time.Second)
		if err != nil {
			fmt.Printf("bridged: down (%s): %v\n", sockPath, err)
			return 1
		}
		if resp.ExitCode == 0 && resp.Stdout == "pong\n" {
			fmt.Printf("bridged: up (%s)\n", sockPath)
			return 0
		}
		fmt.Printf("bridged: unexpected response from %s: %+v\n", sockPath, resp)
		return 1

	default:
		fmt.Fprintf(os.Stderr, "bridged: unknown command: %s\n", args[0])
		return 2
	}
}
