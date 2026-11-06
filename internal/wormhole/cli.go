// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Wormhole CLI commands for starfleetctl — handles incoming/outgoing messages
// between fleet ships via the comms bus.
package wormhole

import (
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"time"

	"github.com/metux/starfleetctl/internal/config"
	"github.com/metux/starfleetctl/internal/identity"
)

// Run executes a wormhole subcommand.
func Run(args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "Usage: starfleetctl wormhole <status|sync|send|start|stop|restart>")
		return 1
	}

	root := os.Getenv("MPBT_WORKSPACE_ROOT")
	if root == "" {
		root, _ = os.Getwd()
	}

	switch args[0] {
	case "status":
		fmt.Print(Status(root))
		return 0
	case "sync":
		return runSync(root)
	case "send":
		return runSend(root, args[1:])
	case "start":
		return runStart(root)
	case "stop":
		return runStop(root)
	case "restart":
		return runRestart(root)
	case "run-internal":
		return runInternal(root)
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
	return 0
}

func runStop(root string) int {
	if err := StopDaemon(root); err != nil {
		fmt.Fprintf(os.Stderr, "wormhole stop failed: %v\n", err)
		return 1
	}
	fmt.Println("Wormhole daemon stopped")
	return 0
}

func runRestart(root string) int {
	if err := StopDaemon(root); err != nil {
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

// runInternal is the entry point for the wormhole worker process, started
// by StartDaemon() as a separate process (not a goroutine). It removes any
// stale "ready" file first (so readiness is uniquely tied to THIS instance),
// writes its own PID file, then runs RunLoop() which handles the sync cycle
// and graceful shutdown on SIGTERM/SIGINT. RunLoop creates a "ready" file
// only after successful startup to signal the parent.
func runInternal(root string) int {
	if err := EnsureDir(root); err != nil {
		fmt.Fprintf(os.Stderr, "wormhole: cannot ensure dir: %v\n", err)
		return 1
	}
	cfg, err := LoadConfig(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wormhole: config error: %v\n", err)
		return 1
	}

	// Remove a stale ready file from a previous instance BEFORE recording
	// our own PID: the parent's readiness handshake (pid file match AND
	// ready file present) can then only ever succeed for THIS instance,
	// even if PID values repeat across restarts.
	if err := os.Remove(filepath.Join(WorkDir(root), "ready")); err != nil && !os.IsNotExist(err) {
		fmt.Fprintf(os.Stderr, "wormhole: cannot remove stale ready file: %v\n", err)
		return 1
	}

	// The worker process writes its own PID to the PID file. This is the
	// authoritative source: StartDaemon() reads this file to confirm the
	// child started, and StopDaemon() reads it to know which process to kill.
	if err := writePIDFile(PIDFile(root), os.Getpid()); err != nil {
		fmt.Fprintf(os.Stderr, "wormhole: cannot write PID file: %v\n", err)
		return 1
	}

	// Mark as starting in the state file.
	st := daemonState{
		Status:  "starting",
		Started: time.Now().UTC().Format(time.RFC3339),
		PID:     os.Getpid(),
		Root:    root,
		Running: true,
	}
	if err := writeStateFile(StateFile(root), st); err != nil {
		fmt.Fprintf(os.Stderr, "wormhole: cannot write state file: %v\n", err)
		return 1
	}

	fmt.Fprintf(os.Stderr, "[wormhole] worker started, pid=%d\n", os.Getpid())

	// Run the main loop. RunLoop creates the ready file after successful startup.
	if err := RunLoop(root, cfg); err != nil {
		fmt.Fprintf(os.Stderr, "wormhole: RunLoop exited: %v\n", err)
		// Remove the PID file and any ready file, but KEEP the state file:
		// RunLoop recorded the failure reason there (e.g. a failed startup
		// sync), and `wormhole status` surfaces it. The next successful
		// start overwrites the state file.
		_ = os.Remove(PIDFile(root))
		_ = os.Remove(filepath.Join(WorkDir(root), "ready"))
		return 1
	}
	return 0
}

// runSync performs one full cycle: sync repositories and process the local
// mailbox, exactly like one daemon tick. The push retry count comes from the
// wormhole configuration (sync.push_retries), loaded once inside
// processPendingMessages.
func runSync(root string) int {
	if err := processPendingMessages(root); err != nil {
		fmt.Fprintf(os.Stderr, "wormhole sync failed: %v\n", err)
		return 1
	}
	fmt.Println("Wormhole sync complete")
	return 0
}

// runSend publishes one message into the durable Maildir spool of a configured
// repository and pushes it. It never writes local Comms records directly.
func runSend(root string, args []string) int {
	fs := flag.NewFlagSet("wormhole send", flag.ContinueOnError)
	fs.SetOutput(os.Stderr)
	to := fs.String("to", "", "recipient ship (mailbox owner)")
	repoID := fs.String("repo", "", "repository id (default: first enabled)")
	from := fs.String("from", "", "sender ship (default: local ship identity)")
	subject := fs.String("subject", "", "optional subject")
	msgType := fs.String("type", "message", "message type")
	text := fs.String("text", "", "message text")
	useStdin := fs.Bool("stdin", false, "read message text from stdin")
	if err := fs.Parse(args); err != nil {
		return 1
	}
	if *to == "" {
		fmt.Fprintln(os.Stderr, "wormhole send: --to is required")
		return 1
	}
	if *from == "" {
		*from = identity.ShipID()
	}
	if *from == "" {
		fmt.Fprintln(os.Stderr, "wormhole send: no sender identity (set STARFLEET_SHIP_ID or --from)")
		return 1
	}

	var body string
	switch {
	case *useStdin:
		data, err := io.ReadAll(os.Stdin)
		if err != nil {
			fmt.Fprintf(os.Stderr, "wormhole send: read stdin: %v\n", err)
			return 1
		}
		body = string(data)
	case *text != "":
		body = *text
	default:
		fmt.Fprintln(os.Stderr, "wormhole send: --text or --stdin is required")
		return 1
	}

	cfg, err := config.Load(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wormhole send: %v\n", err)
		return 1
	}
	if !cfg.Wormholes.Enabled {
		fmt.Fprintln(os.Stderr, "wormhole send: wormhole is disabled in configuration")
		return 1
	}

	repo, err := selectRepo(&cfg.Wormholes, *repoID)
	if err != nil {
		fmt.Fprintf(os.Stderr, "wormhole send: %v\n", err)
		return 1
	}

	msg := NewMessage(*from, *to, *msgType, body, "")
	msg.Subject = *subject
	if err := validateMessage(msg); err != nil {
		fmt.Fprintf(os.Stderr, "wormhole send: %v\n", err)
		return 1
	}

	cloneTimeout, _ := time.ParseDuration(cfg.Wormholes.Sync.CloneTimeout)
	commandTimeout, _ := time.ParseDuration(cfg.Wormholes.Sync.CommandTimeout)
	sp := newSpool(root, repo, cfg.Wormholes.Sync.PushRetries, *from, cloneTimeout, commandTimeout)
	if err := sp.Publish(msg); err != nil {
		fmt.Fprintf(os.Stderr, "wormhole send failed: %v\n", err)
		return 1
	}
	fmt.Printf("Wormhole message published: id=%s repo=%s to=%s\n", msg.ID, repo.ID, msg.To)
	return 0
}

// selectRepo picks an explicitly named enabled repository, or the first enabled
// one when no id was given.
func selectRepo(cfg *config.WormholesConfig, id string) (config.RepositoryConfig, error) {
	if id != "" {
		for _, repo := range cfg.Repositories {
			if repo.ID == id {
				if !repoEnabled(repo) {
					return config.RepositoryConfig{}, fmt.Errorf("repository %q is disabled", id)
				}
				return repo, nil
			}
		}
		return config.RepositoryConfig{}, fmt.Errorf("unknown repository %q", id)
	}
	for _, repo := range cfg.Repositories {
		if repoEnabled(repo) {
			return repo, nil
		}
	}
	return config.RepositoryConfig{}, fmt.Errorf("no enabled repository configured")
}
