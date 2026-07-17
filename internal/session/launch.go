// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package session

import (
	"fmt"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"syscall"

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
	ShellCmd    string // the full shell command to run in the terminal
	Tier        string
	Supervisor  string
	// Launch metadata recorded in the initial agent-bus heartbeat so the
	// board/web console can show the command hierarchy and provider/model.
	LaunchType string // "terminal" | "background" | "auto"
	Parent     string // ship this one was launched under ("" = flagship)
	Provider   string // model provider (derived from Model when empty)
	Model      string // model id (opencode --model value)
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
	model := ""
	parent := ""
	launchType := "background"
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
		case "--model":
			if len(args) < 2 {
				return nil, fmt.Errorf("session run: --model needs a value")
			}
			model = args[1]
			args = args[2:]
		case "--parent":
			if len(args) < 2 {
				return nil, fmt.Errorf("session run: --parent needs a value")
			}
			parent = args[1]
			args = args[2:]
		case "--launch-type":
			if len(args) < 2 {
				return nil, fmt.Errorf("session run: --launch-type needs a value")
			}
			launchType = args[1]
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

	// termctl control pipe path (caller-chosen, under .starfleet-ai)
	pipePath := filepath.Join(root, ".starfleet-ai", "term-pipes", shipID+".pipe")

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
	if client == "claude" {
		inner += "exec claude"
		if permissionMode != "" {
			inner += " --permission-mode " + shellQuote(permissionMode)
		}
		for _, a := range clientArgs {
			inner += " " + shellQuote(a)
		}
	} else if client == "opencode" {
		inner += "exec " + shellQuote(clientPath)
		for _, a := range clientArgs {
			inner += " " + shellQuote(a)
		}
	} else { // shell
		// Run the user's command(s) directly. RunTermctl already wraps the
		// inner command via /bin/sh -c, so we must not prepend another shell
		// here (that would interpret the first clientArg as a script name).
		inner += "exec"
		for _, a := range clientArgs {
			inner += " " + shellQuote(a)
		}
		if len(clientArgs) == 0 {
			// No command given: fall back to an interactive login shell.
			inner += " " + shellQuote(clientPath) + " -l"
		}
	}

	return &LaunchVars{
		ShipID:      shipID,
		PipePath:    pipePath,
		ReleaseFull: project,
		Client:      client,
		ShellCmd:    inner,
		Tier:        tier,
		Supervisor:  supervisor,
		LaunchType:  launchType,
		Parent:      parent,
		Provider:    providerFromModel(model),
		Model:       model,
	}, nil
}

// runShipRun implements `session ship-run [--name <id>] [--model <model>] [-- <args...>]`.
// It starts an opencode control-agent session in ship role (mirroring
// run-opencode.ship) as a detached termctl terminal and returns immediately.
// The caller may pre-allocate the ship name; otherwise a free name is assigned
// and reserved here. State files (control pipe, log) live under
// .starfleet-ai/var/ships/.
func runShipRun(root string, args []string) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Print(`session ship-run [--name <id>] [--model <model>] [-- <args...>]

Start an opencode control-agent session in ship role, detached in the
background (like run-opencode.ship, but as a detachable termctl terminal).
Returns immediately; the terminal keeps running until stopped via
` + "`session stop <id>`" + `.

Flags:
  --name <id>     explicit ship ID (default: next free ship name). The caller
                   may pre-allocate the name; if given, it is reserved here.
  --model <model> opencode model (e.g. nvidia/nvidia/nemotron-3-nano-30b-a3b).
                   The provider is derived from the model id (the part before
                   the first '/'); pass --provider to override.
  --parent <ship> ship this one is launched under (default: flagship Enterprise).
                   Auto-launches from the web GUI hang under the flagship; a ship
                   spawned by another AI lists that ship as parent.
  --launch-type <t>  how the ship was started: "terminal" (direct at a terminal),
                   "background" (detached, the default here), or "auto" (web/timer).
  --              everything after is passed verbatim to opencode

Example:
  starfleetctl session ship-run --name Voyager --model my/model -- --workspace /foo
`)
		return 0
	}

	name := ""
	model := ""
	parent := ""
	launchType := "background"
	var oaArgs []string
	for len(args) > 0 {
		switch args[0] {
		case "--name":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "session ship-run: --name needs a value")
				return 2
			}
			name = args[1]
			args = args[2:]
		case "--model":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "session ship-run: --model needs a value")
				return 2
			}
			model = args[1]
			args = args[2:]
		case "--parent":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "session ship-run: --parent needs a value")
				return 2
			}
			parent = args[1]
			args = args[2:]
		case "--launch-type":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "session ship-run: --launch-type needs a value")
				return 2
			}
			launchType = args[1]
			args = args[2:]
		case "--":
			oaArgs = args[1:]
			args = nil
		default:
			oaArgs = args
			args = nil
		}
	}

	shipID, err := LaunchShip(root, LaunchShipOpts{
		Name:      name,
		Model:     model,
		Parent:    parent,
		LaunchType: launchType,
		ExtraArgs: oaArgs,
	})
	if err != nil {
		fmt.Fprintln(os.Stderr, "session ship-run:", err)
		return 1
	}

	fmt.Printf("agent-run: launched ship '%s' (opencode, role=ship) detached.\n", shipID)
	fmt.Printf("  pipe path    : (see agent-bus board / session attach %s)\n", shipID)
	fmt.Printf("  attach       : starfleetctl session attach %s\n", shipID)
	fmt.Printf("  stop         : starfleetctl session stop %s\n", shipID)
	return 0
}

// LaunchShipOpts are the inputs to LaunchShip.
type LaunchShipOpts struct {
	Name       string   // explicit ship ID; empty => next free name
	Model      string   // opencode --model value (provider derived from it)
	Provider   string   // explicit provider override (when the model id has none)
	Parent     string   // ship launched under; empty => flagship
	LaunchType string   // "terminal" | "background" | "auto"; empty => "background"
	ExtraArgs  []string // passed verbatim to opencode after --prompt
}

// LaunchShip starts a detached opencode control-agent ship and returns its
// assigned ship ID. It is the in-process core of `session ship-run`, also used
// by the web console's "new ship" action. The terminal survives the caller's
// return (it runs in a detached child process).
func LaunchShip(root string, o LaunchShipOpts) (string, error) {
	name := o.Name
	model := o.Model
	parent := o.Parent
	launchType := o.LaunchType
	if launchType == "" {
		launchType = "background"
	}

	// Resolve / reserve the ship name. The caller may pass an already
	// allocated name (reserved elsewhere); otherwise assign the next free one.
	shipReg := shipnames.New(root)
	if name == "" {
		assigned, err := shipReg.AssignName()
		if err != nil || assigned == "" {
			return "", fmt.Errorf("failed to assign ship name: %w", err)
		}
		name = assigned
	} else {
		// Ensure a reservation exists for the given name so stop/gc are
		// consistent (idempotent: keeps an existing reservation).
		if err := shipReg.Reserve(name); err != nil {
			return "", fmt.Errorf("failed to reserve name: %w", err)
		}
	}

	// Refuse if a terminal with this ship ID is already registered.
	reg := NewRegistry(root)
	if _, exists := reg.Get(name); exists {
		return "", fmt.Errorf("'%s' already running — stop it first (session stop %s)", name, name)
	}

	// State files under .starfleet-ai/var/ships/
	stateDir := filepath.Join(root, ".starfleet-ai", "var", "ships")
	pipePath := filepath.Join(stateDir, name+".pipe")
	logPath := filepath.Join(stateDir, name+".log")

	// Build the opencode ship command, mirroring run-opencode.ship.
	const flagship = "Enterprise"
	inner := "export STARFLEET_SHIP_ID=" + shellQuote(name) + "; "
	inner += "export STARFLEET_ROLE=" + shellQuote("ship") + "; "
	inner += "export STARFLEET_TARGET=" + shellQuote(flagship) + "; "
	inner += "export OPENCODE_CONFIG_CONTENT=" + shellQuote(
		`{"username":"`+name+`","instructions":[".starfleet-ai/agents.d/index.md"]}`) + "; "
	inner += "cd " + shellQuote(root) + "; "
	inner += "exec opencode"
	if model != "" {
		inner += " --model " + shellQuote(model)
	}
	inner += " --prompt " + shellQuote(
		"You are fleet ship "+name+", report to flagship "+flagship+
			". Fleet identity loaded via OPENCODE_CONFIG_CONTENT.") + " "
	for _, a := range o.ExtraArgs {
		inner += shellQuote(a) + " "
	}

	vars := &LaunchVars{
		ShipID:      name,
		PipePath:    pipePath,
		ReleaseFull: "",
		Client:      "opencode-ship",
		ShellCmd:    inner,
		LaunchType:  launchType,
		Parent:      parent,
		Provider:    o.Provider,
		Model:       model,
	}
	if vars.Provider == "" {
		vars.Provider = providerFromModel(model)
	}
	// Override the log path to live under var/ships/.
	if err := spawnSessionAt(root, vars, logPath); err != nil {
		return "", err
	}
	return name, nil
}

// spawnSession creates the termctl terminal for the given launch vars and posts
// the initial agent-bus heartbeat. It spawns a child process that runs the
// terminal and blocks on h.Run(), so the terminal survives after this function
// returns.
func spawnSession(root string, vars *LaunchVars) error {
	return spawnSessionAt(root, vars, "")
}

// spawnSessionAt is spawnSession with an explicit log path. An empty logPath
// defaults to .starfleet-ai/logs/<shipID>.log (the shared location used by
// `session run`); callers that group state under .starfleet-ai/var/ pass their
// own path (e.g. ship-run uses var/ships/<id>.log).
func spawnSessionAt(root string, vars *LaunchVars, logPath string) error {
	// Register pipe path before starting (so attach/stop can find it)
	reg := NewRegistry(root)
	if err := reg.Put(vars.ShipID, vars.PipePath); err != nil {
		return fmt.Errorf("registry put: %w", err)
	}

	// Ensure pipe directory exists
	if err := os.MkdirAll(filepath.Dir(vars.PipePath), 0o755); err != nil {
		return fmt.Errorf("mkdir pipe dir: %w", err)
	}

	// Spawn child process that runs termctl-run and blocks on h.Run()
	// The child inherits the workspace root via MPBT_WORKSPACE_ROOT
	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("os.Executable: %w", err)
	}

	// Build environment for child: include workspace root and ship ID
	childEnv := append(os.Environ(),
		"MPBT_WORKSPACE_ROOT="+root,
		"STARFLEET_SHIP_ID="+vars.ShipID,
	)

	// Open log file for child output (so it doesn't inherit parent's stdout/stderr
	// which get closed when starfleetctl exits)
	if logPath == "" {
		logDir := filepath.Join(root, ".starfleet-ai", "logs")
		_ = os.MkdirAll(logDir, 0o755)
		logPath = filepath.Join(logDir, vars.ShipID+".log")
	}
	_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}

	cmd := exec.Command(self, "termctl-run", vars.ShipID, vars.PipePath, vars.ShellCmd)
	cmd.Env = childEnv
	cmd.Stdin = nil // detach from parent's stdin (Go maps nil to /dev/null)
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	// NOTE: do NOT set Setsid here — termctl's PTY spawn (term.Spawn) needs to
	// be the session leader itself (Setsid+Setctty). A double setsid fails
	// with EPERM. The child owns its own session via the PTY; we just detach
	// stdio above and SIGHUP is ignored inside RunTermctl.
	if err := cmd.Start(); err != nil {
		logFile.Close()
		_ = reg.Delete(vars.ShipID)
		return fmt.Errorf("spawn termctl-run: %w", err)
	}

	// Don't wait - the child process runs independently and cleans up
	// registry on exit via its own OnExit callback.
	// The log file will be closed when the child exits (we don't close it here).

	// Post initial heartbeat
	os.Setenv("STARFLEET_SHIP_ID", vars.ShipID)
	if vars.ReleaseFull != "" {
		os.Setenv("PROJECT", vars.ReleaseFull)
	}
	if bus, err := agentbus.New(root); err == nil {
		provider := vars.Provider
		if provider == "" {
			provider = providerFromModel(vars.Model)
		}
		launchType := vars.LaunchType
		if launchType == "" {
			launchType = "background"
		}
		parent := vars.Parent
		if parent == "" {
			parent = shipnames.Flagship
		}
		_ = bus.DoStatus("starting", "launched via agent-run ("+vars.Client+")", agentbus.StatusPatch{
			LaunchType: launchType,
			Parent:     parent,
			Provider:   provider,
			Model:      vars.Model,
		})
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

	// Stop via termctl pipe (best effort). If the terminal already exited,
	// its control pipe is gone and OpenPipe fails — that's fine, we still
	// clean up the registry, heartbeat and ship-name reservation below.
	if ok {
		if rem, err := termctl.OpenPipe(pipePath); err == nil {
			if err := rem.Stop(); err != nil {
				fmt.Fprintf(os.Stderr, "agent-run: stop (pipe): %v\n", err)
			}
		}
	}

	// Heartbeat cleanup + ship name release. A session may already be dead
	// (pipe gone, registry entry cleared by the terminal's OnExit) while the
	// ship-name reservation still lingers; treat that as "already stopped"
	// and clean up rather than erroring.
	shipReg := shipnames.New(root)
	_, nameReserved := shipReg.Lookup(id)
	_ = reg.Delete(id)
	os.Setenv("STARFLEET_SHIP_ID", id)
	if bus, err := agentbus.New(root); err == nil {
		_ = bus.DoClear()
	}
	_ = shipReg.DoRelease(id)

	if !ok && !nameReserved {
		fmt.Fprintf(os.Stderr, "agent-run: no such session: %s\n", id)
		return 1
	}

	fmt.Printf("agent-run: stopped session '%s'\n", id)
	return 0
}

// RunTermctl runs a termctl terminal in the foreground (blocking on h.Run()).
// This is meant to be spawned as a child process by `session run`.
// Args: <ship-id> <pipe-path> <shell-command>
func RunTermctl(root string, args []string) int {
	if len(args) < 3 {
		fmt.Fprintln(os.Stderr, "termctl-run: need <ship-id> <pipe-path> <shell-cmd>")
		return 2
	}
	shipID := args[0]
	pipePath := args[1]
	shellCmd := args[2]

	// Detached terminals must survive the parent's shell exiting. Ignore
	// SIGHUP so a closing controlling terminal doesn't kill us.
	signal.Ignore(syscall.SIGHUP)

	fmt.Fprintf(os.Stderr, "termctl-run: shipID=%s pipePath=%s shellCmd=%s\n", shipID, pipePath, shellCmd)

	// The shellCmd is a script like "export FOO=bar; exec bash -i"
	// We run it via /bin/sh -c "shellCmd"
	shellBin := "/bin/sh"
	shellArgs := []string{"-c", shellCmd}

	// Ensure pipe directory exists (termctl creates the FIFO but needs the dir)
	if err := os.MkdirAll(filepath.Dir(pipePath), 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "termctl-run: mkdir pipe dir: %v\n", err)
		return 1
	}

	reg := NewRegistry(root)

	// Build termctl handle
	h, err := termctl.New(
		termctl.WithName(shipID),
		termctl.WithTitle("starfleet: "+shipID),
		termctl.WithControlPipe(pipePath),
		termctl.WithShell(shellBin),
		termctl.WithShellArgs(shellArgs),
		termctl.WithExtraEnv([]string{
			"STARFLEET_SHIP_ID=" + shipID,
		}),
		termctl.WithOnExit(func() {
			// Cleanup registry on shell exit
			fmt.Fprintf(os.Stderr, "termctl-run: OnExit callback for %s\n", shipID)
			_ = reg.Delete(shipID)
		}),
	)
	if err != nil {
		fmt.Fprintf(os.Stderr, "termctl.New: %v\n", err)
		return 1
	}

	fmt.Fprintf(os.Stderr, "termctl-run: starting Run()\n")
	// Block until shell exits
	if err := h.Run(); err != nil {
		fmt.Fprintf(os.Stderr, "termctl.Run: %v\n", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "termctl-run: Run() returned\n")
	return 0
}