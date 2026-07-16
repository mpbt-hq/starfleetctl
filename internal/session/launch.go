// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package session

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/X11Libre/go-x11proto/tk/term/termctl"
	"github.com/metux/starfleetctl/internal/agentbus"
	"github.com/metux/starfleetctl/internal/shipnames"
)

// LaunchVars holds the computed values from a `session run` invocation.
type LaunchVars struct {
	ShipID      string
	PipePath    string // termctl control pipe path
	ReleaseFull string
	Client      string
	Tier        string
	Supervisor  string
}

// runLaunch implements `session run <release> [flags...] [-- <args...>]`.
// By default it launches a detached termctl terminal (replacing tmux); pass --print
// to emit shell-evaluable launch variables instead (legacy mode).
func runLaunch(root string, args []string) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Print(`session run <release> [flags...] [-- <args...>]

Launch a detached terminal for an agent/CLI and post the initial
heartbeat (replaces scripts/agent-run).  Pass --print to emit the
shell-evaluable launch variables instead.

Flags:
  --client <claude|opencode|shell>   client to run (default: claude)
  --name <id>                        explicit ship ID (default: auto-assign)
  --permission-mode <mode>           claude permission mode (default: dontAsk for workers)
  --tier <name>                      agent tier (e.g. worker)
  --supervisor <id>                  supervisor ship ID
  --print                            emit shell variables instead of launching

Args after -- are passed to the client.
`)
		return 0
	}

	printVars := false
	launchArgs := make([]string, 0, len(args))
	for _, a := range args {
		if a == "--print" {
			printVars = true
			continue
		}
		launchArgs = append(launchArgs, a)
	}

	vars, err := computeLaunch(root, launchArgs)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}
	if vars == nil {
		return 1
	}

	if printVars {
		fmt.Printf("STARFLEET_SHIP_ID=%s\n", shellQuote(vars.ShipID))
		fmt.Printf("PIPE_PATH=%s\n", shellQuote(vars.PipePath))
		fmt.Printf("RELEASE_FULL=%s\n", shellQuote(vars.ReleaseFull))
		fmt.Printf("CLIENT=%s\n", shellQuote(vars.Client))
		return 0
	}

	if err := spawnSession(root, vars); err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 1
	}

	rel := ""
	if vars.ReleaseFull != "" {
		rel = ", " + vars.ReleaseFull
	}
	fmt.Printf("agent-run: launched '%s' (%s%s) detached.\n", vars.ShipID, vars.Client, rel)
	fmt.Printf("  pipe path    : %s\n", vars.PipePath)
	fmt.Printf("  attach       : starfleetctl session attach %s\n", vars.ShipID)
	fmt.Printf("  board        : starfleetctl agent-bus board\n")
	fmt.Printf("  stop         : starfleetctl session stop %s\n", vars.ShipID)
	return 0
}

// computeLaunch parses arguments, validates, and returns the computed launch
// vars without printing anything. Returns nil on "session already exists"
// (runLaunch passes that as exit 1) and an error on arg/validation failures.
// On success, the returned LaunchVars have all fields filled.
func computeLaunch(root string, args []string) (*LaunchVars, error) {
	if len(args) == 0 {
		return nil, fmt.Errorf("session run: need <release> (or '' for cross-project)")
	}

	release := args[0]
	args = args[1:]

	client := "claude"
	name := ""
	permissionMode := ""
	tier := ""
	supervisor := ""
	var clientArgs []string

	for len(args) > 0 {
		switch args[0] {
		case "--client":
			if len(args) < 2 {
				return nil, fmt.Errorf("session run: --client needs a value (claude|opencode|shell)")
			}
			client = args[1]
			args = args[2:]
		case "--name":
			if len(args) < 2 {
				return nil, fmt.Errorf("session run: --name needs a value")
			}
			name = args[1]
			args = args[2:]
		case "--permission-mode":
			if len(args) < 2 {
				return nil, fmt.Errorf("session run: --permission-mode needs a value")
			}
			permissionMode = args[1]
			args = args[2:]
		case "--tier":
			if len(args) < 2 {
				return nil, fmt.Errorf("session run: --tier needs a value")
			}
			tier = args[1]
			args = args[2:]
		case "--supervisor":
			if len(args) < 2 {
				return nil, fmt.Errorf("session run: --supervisor needs a value")
			}
			supervisor = args[1]
			args = args[2:]
		case "--":
			args = args[1:]
			clientArgs = args
			args = nil
		default:
			clientArgs = args
			args = nil
		}
	}

	if permissionMode != "" && client != "claude" {
		return nil, fmt.Errorf("session run: --permission-mode only applies to --client claude")
	}

	switch client {
	case "claude", "opencode", "shell":
	default:
		return nil, fmt.Errorf("session run: unknown --client '%s' (claude|opencode|shell)", client)
	}

	var project string
	if release != "" {
		project = release
		configPath := filepath.Join(root, "cf", project, "config.sh")
		if _, err := os.Stat(configPath); err != nil {
			return nil, fmt.Errorf("session run: no such release '%s' (missing cf/%s/config.sh)", release, project)
		}
	} else {
		if client == "opencode" {
			return nil, fmt.Errorf("session run: --client opencode needs a <release> (its wrapper sources cf/<release>/config.sh)")
		}
	}

	shipID := name
	if shipID == "" {
		reg := shipnames.New(root)
		assigned, err := reg.AssignName()
		if err == nil && assigned != "" {
			shipID = assigned
		}
	}
	if shipID == "" {
		if project != "" {
			shipID = project + "-" + client
		} else {
			return nil, fmt.Errorf("session run: empty <release> needs an explicit --name (nothing to derive STARFLEET_SHIP_ID from)")
		}
	}

	// Check if already running via registry
	reg := NewRegistry(root)
	if _, exists := reg.Get(shipID); exists {
		fmt.Fprintf(os.Stderr, "session run: session '%s' already running — attach with: starfleetctl session attach %s (or use --name for a second one)\n", shipID, shipID)
		return nil, nil
	}

	// termctl control pipe path (caller-chosen, under _WORK_)
	pipePath := filepath.Join(root, "_WORK_", "term-pipes", shipID+".pipe")

	var clientPath string
	switch client {
	case "claude":
		// Use shell to run claude with args
		clientPath = "/bin/sh"
	case "opencode":
		clientPath = "./run-opencode." + project
	case "shell":
		clientPath = os.Getenv("SHELL")
		if clientPath == "" {
			clientPath = "bash"
		}
	}

	// Build the shell command for termctl
	// termctl runs: exec.Command(shell) with no args, then the shell runs InnerCmd via PTY
	inner := "export STARFLEET_SHIP_ID=" + shellQuote(shipID) + "; "
	if tier != "" {
		inner += "export AGENT_TIER=" + shellQuote(tier) + "; "
	}
	if supervisor != "" {
		inner += "export AGENT_SUPERVISOR=" + shellQuote(supervisor) + "; "
	}
	if project != "" {
		inner += ". cf/" + shellQuote(project) + "/config.sh; export PROJECT; "
	}
	inner += "exec"
	if client == "claude" {
		inner += " claude"
		if permissionMode != "" {
			inner += " --permission-mode " + shellQuote(permissionMode)
		}
		for _, a := range clientArgs {
			inner += " " + shellQuote(a)
		}
	} else if client == "opencode" {
		inner += " " + shellQuote(clientPath)
		for _, a := range clientArgs {
			inner += " " + shellQuote(a)
		}
	} else { // shell
		inner += " " + shellQuote(clientPath)
		for _, a := range clientArgs {
			inner += " " + shellQuote(a)
		}
	}

	return &LaunchVars{
		ShipID:      shipID,
		PipePath:    pipePath,
		ReleaseFull: project,
		Client:      client,
		Tier:        tier,
		Supervisor:  supervisor,
	}, nil
}

// spawnSession creates the termctl terminal for the given launch vars and posts
// the initial agent-bus heartbeat.
func spawnSession(root string, vars *LaunchVars) error {
	// Register pipe path before starting (so attach/stop can find it)
	reg := NewRegistry(root)
	if err := reg.Put(vars.ShipID, vars.PipePath); err != nil {
		return fmt.Errorf("registry put: %w", err)
	}

	// Ensure pipe directory exists
	if err := os.MkdirAll(filepath.Dir(vars.PipePath), 0o755); err != nil {
		return fmt.Errorf("mkdir pipe dir: %w", err)
	}

	// Build termctl handle
	opts := []termctl.Opt{
		termctl.WithName(vars.ShipID),
		termctl.WithControlPipe(vars.PipePath),
		termctl.WithShell("/bin/sh"),
		termctl.WithExtraEnv([]string{
			"STARFLEET_SHIP_ID=" + vars.ShipID,
			"STARFLEET_AGENT_HANDLE=" + vars.ShipID, // termctl doesn't use this but agent inside will
		}),
		termctl.WithOnExit(func() {
			// Cleanup registry on shell exit
			_ = reg.Delete(vars.ShipID)
		}),
	}

	h, err := termctl.New(opts...)
	if err != nil {
		_ = reg.Delete(vars.ShipID)
		return fmt.Errorf("termctl.New: %w", err)
	}

	// Start detached (no initial attach). The shell runs in background.
	// Run() blocks until shell exits; we run it in a goroutine.
	go func() {
		_ = h.Run()
	}()

	// Post initial heartbeat
	os.Setenv("STARFLEET_SHIP_ID", vars.ShipID)
	if vars.ReleaseFull != "" {
		os.Setenv("PROJECT", vars.ReleaseFull)
	}
	if bus, err := agentbus.New(root); err == nil {
		_ = bus.DoStatus("starting", "launched via agent-run ("+vars.Client+")", agentbus.StatusPatch{})
	}
	return nil
}

// doSpawn creates a termctl session and posts the initial heartbeat for the given
// launch args. It calls computeLaunch internally, so it accepts the same args
// as `session run`. Used by the autoscaler; `session run` calls spawnSession
// directly after computing its own vars.
func doSpawn(root string, args []string) error {
	vars, err := computeLaunch(root, args)
	if err != nil {
		return err
	}
	if vars == nil {
		return fmt.Errorf("session already exists")
	}
	return spawnSession(root, vars)
}

// runStop implements `session stop <id>`.
func runStop(root string, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "session stop: need <id|session>")
		return 2
	}
	id := args[0]

	reg := NewRegistry(root)
	pipePath, ok := reg.Get(id)
	if !ok {
		// Fallback: check agent-bus status records
		bus, err := agentbus.New(root)
		if err == nil {
			for _, r := range bus.AllStatusRecords() {
				if r.Agent == id || r.Handle == id {
					if p, found := reg.Get(r.Agent); found {
						pipePath = p
						id = r.Agent
						ok = true
						break
					}
				}
			}
		}
	}

	if !ok {
		fmt.Fprintf(os.Stderr, "agent-run: no such session: %s\n", id)
		return 1
	}

	// Stop via termctl pipe
	rem, err := termctl.OpenPipe(pipePath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "agent-run: open pipe %s: %v\n", pipePath, err)
		return 1
	}
	if err := rem.Stop(); err != nil {
		fmt.Fprintf(os.Stderr, "agent-run: stop failed: %v\n", err)
		return 1
	}

	// Heartbeat cleanup + ship name release
	os.Setenv("STARFLEET_SHIP_ID", id)
	if bus, err := agentbus.New(root); err == nil {
		_ = bus.DoClear()
	}
	shipReg := shipnames.New(root)
	_ = shipReg.DoRelease(id)

	fmt.Printf("agent-run: stopped session '%s'\n", id)
	return 0
}