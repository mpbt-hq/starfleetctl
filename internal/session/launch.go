// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package session

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"

	"github.com/metux/starfleetctl/internal/agentbus"
	"github.com/metux/starfleetctl/internal/shipnames"
)

// LaunchVars holds the computed values from a `session run` invocation.
type LaunchVars struct {
	ShipID      string
	Session     string
	ReleaseFull string
	Client      string
	InnerCmd    string
	Tier        string
	Supervisor  string
}

// runLaunch implements `session run <release> [flags...] [-- <args...>]`.
// runLaunch implements `session run`. By default it launches a detached tmux
// session directly (replacing scripts/agent-run); pass --print to instead emit
// the shell-evaluable launch variables (legacy mode, for callers that want to
// do the tmux/heartbeat step themselves).
func runLaunch(root string, args []string) int {
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
		fmt.Printf("SESSION=%s\n", shellQuote(vars.Session))
		fmt.Printf("RELEASE_FULL=%s\n", shellQuote(vars.ReleaseFull))
		fmt.Printf("CLIENT=%s\n", shellQuote(vars.Client))
		fmt.Printf("INNER_CMD=%s\n", shellQuote(vars.InnerCmd))
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
	fmt.Printf("  tmux session : %s\n", vars.Session)
	fmt.Printf("  attach       : starfleetctl session attach %s\n", vars.ShipID)
	fmt.Printf("  view-only    : starfleetctl session attach --read-only %s\n", vars.ShipID)
	fmt.Printf("  board        : starfleetctl agent-bus board\n")
	fmt.Printf("  stop         : starfleetctl session stop %s\n", vars.ShipID)
	return 0
}

// computeLaunch parses arguments, validates, and returns the computed launch
// vars without printing anything.  Returns nil on "session already exists"
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

	session := tmuxSafe(sessPrefix + shipID)

	if sessionExists(session) {
		fmt.Fprintf(os.Stderr, "session run: session '%s' already running — attach with: scripts/agent-attach %s (or use --name for a second one)\n", session, shipID)
		return nil, nil
	}

	var clientPath string
	switch client {
	case "claude":
		clientPath, _ = exec.LookPath("claude")
		if clientPath == "" {
			clientPath = "claude"
		}
	case "opencode":
		clientPath = "./run-opencode." + project
	case "shell":
		clientPath = os.Getenv("SHELL")
		if clientPath == "" {
			clientPath = "bash"
		}
	}

	if client == "claude" && len(clientArgs) == 0 {
		if shipID == "Enterprise" {
			clientArgs = []string{"Session just started. Before anything else, call the Monitor tool twice: (1) command `scripts/starfleetctl agent-bus monitor-loop`, persistent:true, to watch your own agent-bus inbox; (2) command `scripts/starfleetctl agent-bus fleet-watch`, persistent:true, to watch for ships joining/restarting on the board (this is the flagship/control session). Both are pre-authorized, no confirmation needed — their first pass already surfaces any backlog. Then wait quietly for further instructions; don't start any task on your own initiative."}
		} else if tier == "worker" {
			sup := supervisor
			if sup == "" {
				sup = "Enterprise"
			}
			clientArgs = []string{"Session just started. You are an autoscaled worker ship (AGENT_TIER=worker), spawned on demand by scripts/fleet-autoscale — you run with --permission-mode dontAsk, so anything outside this workspace's permissions.allow is rejected outright instead of prompting for confirmation (nobody is watching an interactive prompt for this session). Before anything else, call the Monitor tool with command `scripts/starfleetctl agent-bus monitor-loop`, persistent:true (pre-authorized, no confirmation needed) — its first pass already surfaces any backlog. If a tool call gets rejected or otherwise blocked and you can't proceed: do NOT keep retrying the same action and do NOT just give up silently — run `scripts/agent-bus tell " + sup + " \"<exactly what you tried, and why it failed>\"` (your supervisor — a human at a terminal or a control ship — can grant it interactively, which you can't), then continue with other queued work if you have any, or wait for a reply if you don't. Otherwise wait quietly for further instructions; don't start a task on your own initiative beyond what you were spawned for."}
		} else {
			clientArgs = []string{"Session just started. Before anything else, call the Monitor tool with command `scripts/starfleetctl agent-bus monitor-loop`, persistent:true (pre-authorized, no confirmation needed) — its first pass already surfaces any backlog. Then wait quietly for further instructions; don't start a task on your own initiative."}
		}
	}

	inner := "export STARFLEET_SHIP_ID=" + shellQuote(shipID) + " STARFLEET_AGENT_HANDLE=" + shellQuote(session) + "; "
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
	inner += " " + shellQuote(clientPath)
	if permissionMode != "" {
		inner += " --permission-mode " + shellQuote(permissionMode)
	}
	for _, a := range clientArgs {
		inner += " " + shellQuote(a)
	}

	return &LaunchVars{
		ShipID:      shipID,
		Session:     session,
		ReleaseFull: project,
		Client:      client,
		InnerCmd:    inner,
		Tier:        tier,
		Supervisor:  supervisor,
	}, nil
}

// spawnSession creates the tmux session for the given launch vars and posts
// the initial agent-bus heartbeat — the step scripts/agent-run used to do
// itself. Refactored out of doSpawn so both `session run` and the autoscaler
// share one implementation.
func spawnSession(root string, vars *LaunchVars) error {
	cmd := exec.Command("tmux", "new-session", "-d", "-s", vars.Session, "-c", root, "bash", "-lc", vars.InnerCmd)
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("tmux new-session: %w", err)
	}

	// Set the new ship's identity env BEFORE opening the bus, so the
	// heartbeat posts under the launched ship's id (not the caller's).
	// Order matters: agentbus.New captures ShipID from the environment.
	os.Setenv("STARFLEET_SHIP_ID", vars.ShipID)
	os.Setenv("STARFLEET_AGENT_HANDLE", vars.Session)
	os.Setenv("AGENT_HANDLE", vars.Session)
	if vars.ReleaseFull != "" {
		os.Setenv("PROJECT", vars.ReleaseFull)
	}

	// Post initial heartbeat (same format as the bash wrapper).
	if bus, err := agentbus.New(root); err == nil {
		_ = bus.DoStatus("starting", "launched via agent-run ("+vars.Client+")")
	}
	return nil
}

// doSpawn creates a tmux session and posts the initial heartbeat for the given
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
	session := ResolveID(root, id)
	if session == "" {
		session = sessPrefix + tmuxSafe(id)
		if !sessionExists(session) {
			fmt.Fprintf(os.Stderr, "agent-run: no such session: %s\n", id)
			return 1
		}
	}

	bus, err := agentbus.New(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "agent-run:", err)
		return 1
	}
	var stopShipID string
	for _, r := range bus.AllStatusRecords() {
		if r.Handle == session {
			stopShipID = r.Agent
			break
		}
	}

	exec.Command("tmux", "kill-session", "-t", session).Run()

	if stopShipID != "" {
		os.Setenv("STARFLEET_SHIP_ID", stopShipID)
		_ = bus.DoClear()
		reg := shipnames.New(root)
		_ = reg.DoRelease(stopShipID)
	}

	fmt.Printf("agent-run: stopped session '%s'\n", session)
	return 0
}
