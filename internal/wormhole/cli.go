// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Wormhole CLI commands for starfleetctl — handles incoming/outgoing messages
// between fleet ships via the comms bus.
package wormhole

import (
	"fmt"
	"os"

	"github.com/metux/starfleetctl/internal/comms"
)

// Run executes a wormhole subcommand.
func Run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: starfleetctl wormhole <status|sync|start|stop|restart>")
		return 1
	}

	root := os.Getenv("MPBT_WORKSPACE_ROOT")
	if root == "" {
		root, _ = os.Getwd()
	}

	switch args[0] {
	case "status":
		fmt.Print(Status())
		return 0
	case "sync":
		return runSync(root)
	case "start":
		return runStart(root)
	case "stop":
		return runStop()
	case "restart":
		return runRestart(root)
	default:
		fmt.Fprintf(os.Stderr, "wormhole: unknown command: %s\n", args[0])
		return 1
	}
}

func runStart(root string) int {
	if err := StartDaemon(root); err != nil {
		fmt.Fprintf(os.Stderr, "wormhole start failed: %v\n", err)
		return 1
	}
	fmt.Println("Wormhole daemon started")
	
	// Create comms bus and run the daemon loop
	bus, err := comms.New(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wormhole: comms init failed: %v\n", err)
		return 1
	}
	
	if err := RunLoop(root, bus); err != nil {
		fmt.Fprintf(os.Stderr, "wormhole run loop failed: %v\n", err)
		return 1
	}
	
	fmt.Println("Wormhole daemon stopped")
	return 0
}

func runStop() int {
	if err := StopDaemon(); err != nil {
		fmt.Fprintf(os.Stderr, "wormhole stop failed: %v\n", err)
		return 1
	}
	fmt.Println("Wormhole daemon stopped")
	return 0
}

func runRestart(root string) int {
	if err := StopDaemon(); err != nil {
		fmt.Fprintf(os.Stderr, "wormhole stop failed: %v\n", err)
		return 1
	}
	if err := StartDaemon(root); err != nil {
		fmt.Fprintf(os.Stderr, "wormhole start failed: %v\n", err)
		return 1
	}
	fmt.Println("Wormhole daemon restarted")
	return 0
}

func runSync(root string) int {
	fmt.Println("Wormhole sync: synchronising message state...")
	return 0
}
