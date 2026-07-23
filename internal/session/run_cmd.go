// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// This file implements the top-level `starfleetctl run` command that replaces
// the run-opencode.* and run-claude.* shell scripts. It consolidates ship-name
// assignment, env-var setup, heartbeat posting, and client launch into a single
// Go codepath.

package session

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"

	"github.com/metux/starfleetctl/internal/comms"
	"github.com/metux/starfleetctl/internal/shipnames"
)

const runUsage = `starfleetctl run [flags...] [-- <args...>]

Start an AI agent session (opencode or claude) as flagship or ship.
Replaces run-opencode.flagship, run-opencode.ship, run-claude.flagship,
run-claude.ship with a single Go codepath.

Flags:
  --client <claude|opencode>   client to run (default: opencode)
  --flagship                   start as flagship (Enterprise) — mutually
                               exclusive with --name
  --name <id>                  ship name (default: auto-assign from pool)
  --model <model>              model to use (e.g. nvidia/nemotron-3-8b)
  --exec                       exec client directly (no termctl terminal)
                               default is termctl (detached, attachable)
  --prompt <text>              override the default prompt
  -h, --help                   show this help

Args after -- are passed verbatim to the client.

Examples:
  starfleetctl run --flagship --client opencode
  starfleetctl run --client claude --name Voyager -- --permission-mode dontAsk
  starfleetctl run --flagship --exec -- client flags here
`

// RunCmd implements the top-level `starfleetctl run` command.
func RunCmd(root string, args []string) int {
	if len(args) > 0 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Print(runUsage)
		return 0
	}

	client := "opencode"
	flagship := false
	name := ""
	model := ""
	useExec := false
	customPrompt := ""
	var clientArgs []string

	for len(args) > 0 {
		switch args[0] {
		case "--client":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "run: --client needs a value (claude|opencode)")
				return 2
			}
			client = args[1]
			args = args[2:]
		case "--flagship":
			flagship = true
			args = args[1:]
		case "--name":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "run: --name needs a value")
				return 2
			}
			name = args[1]
			args = args[2:]
		case "--model":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "run: --model needs a value")
				return 2
			}
			model = args[1]
			args = args[2:]
		case "--exec":
			useExec = true
			args = args[1:]
		case "--prompt":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "run: --prompt needs a value")
				return 2
			}
			customPrompt = args[1]
			args = args[2:]
		case "-h", "--help":
			fmt.Print(runUsage)
			return 0
		case "--":
			args = args[1:]
			clientArgs = args
			args = nil
		default:
			clientArgs = args
			args = nil
		}
	}

	// Validate flags
	if flagship && name != "" {
		fmt.Fprintln(os.Stderr, "run: --flagship and --name are mutually exclusive")
		return 2
	}

	switch client {
	case "claude", "opencode":
	default:
		fmt.Fprintf(os.Stderr, "run: unknown --client '%s' (claude|opencode)\n", client)
		return 2
	}

	// Resolve ship name
	shipID := name
	role := "ship"

	if flagship {
		shipID = shipnames.FlagshipName(root)
		role = "flagship"
	} else if shipID == "" {
		reg := shipnames.New(root)
		assigned, err := reg.AssignName()
		if err != nil || assigned == "" {
			fmt.Fprintln(os.Stderr, "run: failed to assign ship name:", err)
			return 1
		}
		shipID = assigned
	} else {
		// Ensure reservation exists for explicitly given name
		reg := shipnames.New(root)
		if err := reg.Reserve(shipID); err != nil {
			fmt.Fprintln(os.Stderr, "run: failed to reserve name:", err)
			return 1
		}
	}

	// Load instructions
	instructionsPath := filepath.Join(root, ".starfleet-ai", "var", "agents.d", "index.md")
	systemPrompt := ""
	if data, err := os.ReadFile(instructionsPath); err == nil {
		systemPrompt = string(data)
	}

	// Build the prompt
	prompt := customPrompt
	if prompt == "" {
		prompt = defaultPrompt(root, client, shipID, role, model)
	}

	// Set env vars
	os.Setenv("STARFLEET_SHIP_ID", shipID)
	os.Setenv("STARFLEET_ROLE", role)
	if role == "ship" {
		os.Setenv("STARFLEET_TARGET", shipnames.FlagshipName(root))
	} else {
		os.Unsetenv("STARFLEET_TARGET")
	}

	// Post initial heartbeat
	if bus, err := comms.New(root); err == nil {
		provider := providerFromModel(model)
		_ = bus.DoStatus("idle", client+" session starting (run)", comms.StatusPatch{
			LaunchType: "terminal",
			Parent:     shipnames.FlagshipName(root),
			Provider:   provider,
			Model:      model,
		})
	}

	// Build env vars for client
	env := append(os.Environ(),
		"STARFLEET_SHIP_ID="+shipID,
		"STARFLEET_ROLE="+role,
	)
	if role == "ship" {
		env = append(env, "STARFLEET_TARGET="+shipnames.FlagshipName(root))
	}

	if useExec {
		return execClientDirect(root, client, shipID, systemPrompt, prompt, model, env, clientArgs)
	}

	return execClientTermctl(root, client, shipID, role, systemPrompt, prompt, model, env, clientArgs)
}

// defaultPrompt returns the standard prompt for the given client/role combination.
func defaultPrompt(root, client, shipID, role, model string) string {
	if client == "opencode" {
		configContent := fmt.Sprintf(`{"username":"%s","instructions":[".starfleet-ai/var/agents.d/index.md"]}`, shipID)
		os.Setenv("OPENCODE_CONFIG_CONTENT", configContent)

		if role == "flagship" {
			return "You are the flagship " + shipID + ". Fleet identity loaded via OPENCODE_CONFIG_CONTENT."
		}
		return "You are fleet ship " + shipID + ", report to flagship " + shipnames.FlagshipName(root) +
			". Fleet identity loaded via OPENCODE_CONFIG_CONTENT."
	}

	// claude
	if role == "flagship" {
		return "Session just started. Before anything else, call the Monitor tool twice: (1) command `.starfleet-ai/bin/starfleetctl comms monitor-loop`, persistent:true, to watch your own comms inbox; (2) command `.starfleet-ai/bin/starfleetctl comms fleet-watch`, persistent:true, to watch for ships joining/restarting on the board (this is the flagship/control session). Both are pre-authorized, no confirmation needed — their first pass already surfaces any backlog. Then wait quietly for further instructions; don't start any task on your own initiative."
	}
	return "Session just started. Before anything else, call the Monitor tool with command `.starfleet-ai/bin/starfleetctl comms monitor-loop`, persistent:true (pre-authorized, no confirmation needed) — its first pass already surfaces any backlog. Then wait quietly for further instructions; don't start any task on your own initiative."
}

// execClientDirect execs the client directly (no termctl), replacing the process.
func execClientDirect(root, client, shipID, systemPrompt, prompt, model string, env []string, args []string) int {
	var cmd *exec.Cmd

	switch client {
	case "opencode":
		cmdArgs := []string{}
		if model != "" {
			cmdArgs = append(cmdArgs, "--model", model)
		}
		if prompt != "" {
			cmdArgs = append(cmdArgs, "--prompt", prompt)
		}
		cmdArgs = append(cmdArgs, args...)
		cmd = exec.Command("opencode", cmdArgs...)

	case "claude":
		cmdArgs := []string{}
		if systemPrompt != "" {
			cmdArgs = append(cmdArgs, "--append-system-prompt", systemPrompt)
		}
		cmdArgs = append(cmdArgs, args...)
		cmd = exec.Command("claude", cmdArgs...)
	}

	cmd.Env = env
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr

	if err := cmd.Run(); err != nil {
		if exitErr, ok := err.(*exec.ExitError); ok {
			return exitErr.ExitCode()
		}
		fmt.Fprintln(os.Stderr, "run:", err)
		return 1
	}
	return 0
}

// execClientTermctl launches the client in a termctl terminal (detached, attachable).
func execClientTermctl(root, client, shipID, role, systemPrompt, prompt, model string, env []string, args []string) int {
	// Build inner command for termctl
	inner := buildInnerCommand(root, client, shipID, role, systemPrompt, prompt, model, args)

	// Compute launch vars
	pipePath := PipePath(root, shipID)
	logPath := LogPath(root, shipID)

	vars := &LaunchVars{
		ShipID:     shipID,
		PipePath:   pipePath,
		Client:     client,
		ShellCmd:   inner,
		LaunchType: "terminal",
		Parent:     shipnames.FlagshipName(root),
		Model:      model,
		Provider:   providerFromModel(model),
	}

	if err := spawnSessionAt(root, vars, logPath); err != nil {
		fmt.Fprintln(os.Stderr, "run:", err)
		return 1
	}

	fmt.Printf("run: launched '%s' (%s) detached.\n", shipID, client)
	fmt.Printf("  pipe path    : %s\n", pipePath)
	fmt.Printf("  attach       : starfleetctl session attach %s\n", shipID)
	fmt.Printf("  stop         : starfleetctl session stop %s\n", shipID)
	return 0
}

// buildInnerCommand constructs the shell command string for termctl to execute.
func buildInnerCommand(root, client, shipID, role, systemPrompt, prompt, model string, args []string) string {
	var parts []string

	// Set env vars
	parts = append(parts, "export STARFLEET_SHIP_ID="+shellQuote(shipID))
	parts = append(parts, "export STARFLEET_ROLE="+shellQuote(role))
	if role == "ship" {
		parts = append(parts, "export STARFLEET_TARGET="+shellQuote(shipnames.FlagshipName(root)))
	}

	// Set OPENCODE_CONFIG_CONTENT for opencode
	if client == "opencode" {
		configContent := `{"username":"` + shipID + `","instructions":[".starfleet-ai/var/agents.d/index.md"]}`
		parts = append(parts, "export OPENCODE_CONFIG_CONTENT="+shellQuote(configContent))
	}

	// Build the exec command
	switch client {
	case "opencode":
		cmdParts := []string{"exec", "opencode"}
		if model != "" {
			cmdParts = append(cmdParts, "--model", shellQuote(model))
		}
		if prompt != "" {
			cmdParts = append(cmdParts, "--prompt", shellQuote(prompt))
		}
		for _, a := range args {
			cmdParts = append(cmdParts, shellQuote(a))
		}
		parts = append(parts, strings.Join(cmdParts, " "))

	case "claude":
		cmdParts := []string{"exec", "claude"}
		if systemPrompt != "" {
			cmdParts = append(cmdParts, "--append-system-prompt", shellQuote(systemPrompt))
		}
		for _, a := range args {
			cmdParts = append(cmdParts, shellQuote(a))
		}
		parts = append(parts, strings.Join(cmdParts, " "))
	}

	return strings.Join(parts, "; ")
}
