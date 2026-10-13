// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package main

import (
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"syscall"

	"github.com/metux/starfleetctl/internal/comms"
	"github.com/metux/starfleetctl/internal/config"
	"github.com/metux/starfleetctl/internal/modelproxy"
	"github.com/metux/starfleetctl/internal/ocsessions"
	"github.com/metux/starfleetctl/internal/shipnames"
)

const supervisorUsage = `usage: starfleetctl ship-supervisor [--root <path>]

Persistent supervisor for ship session restart management.
Runs as a background daemon, holds control sockets, and manages child processes.
`

type ShipConfig struct {
	Root         string
	Name         string
	Model        string
	Parent       string
	LaunchType   string
	Class        string
	Unrestricted bool
	ForceNew     bool
	Provider     string
}

type ShipState struct {
	Name         string
	Config       ShipConfig
	ChildCmd     *exec.Cmd
	ChildPid     int
	CurrentModel string
	Mu           sync.Mutex
	Listener     net.Listener
	SocketPath   string
}

var (
	ships   = make(map[string]*ShipState)
	shipsMu sync.Mutex
	root    string
)

func main() {
	if len(os.Args) > 1 && (os.Args[1] == "-h" || os.Args[1] == "--help") {
		fmt.Print(supervisorUsage)
		os.Exit(0)
	}

	root = os.Getenv("MPBT_WORKSPACE_ROOT")
	if root == "" {
		root = "/home/nekrad/src/xorg/mpbt-workspace"
	}
	for i := 1; i < len(os.Args); i++ {
		if os.Args[i] == "--root" && i+1 < len(os.Args) {
			root = os.Args[i+1]
			i++
		}
	}

	fmt.Fprintf(os.Stderr, "ship-supervisor: starting on root=%s\n", root)

	_, err := config.Load(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "ship-supervisor: config load: %v\n", err)
	}

	shipsDir := filepath.Join(root, ".starfleet-ai", "var", "ships")
	if err := os.MkdirAll(shipsDir, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "ship-supervisor: mkdir ships dir: %v\n", err)
		os.Exit(1)
	}

	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGTERM, syscall.SIGINT)
	go func() {
		<-sigCh
		fmt.Fprintf(os.Stderr, "ship-supervisor: shutdown signal received\n")
		stopAllShips()
		os.Exit(0)
	}()

	select {}
}

func stopAllShips() {
	shipsMu.Lock()
	defer shipsMu.Unlock()
	for name, ship := range ships {
		fmt.Fprintf(os.Stderr, "ship-supervisor: stopping %s\n", name)
		ship.Mu.Lock()
		if ship.ChildCmd != nil && ship.ChildCmd.Process != nil {
			_ = ship.ChildCmd.Process.Kill()
			ship.ChildCmd.Wait()
		}
		if ship.Listener != nil {
			ship.Listener.Close()
		}
		os.Remove(ship.SocketPath)
		ship.Mu.Unlock()
	}
}

func getOrCreateShip(name string) *ShipState {
	shipsMu.Lock()
	defer shipsMu.Unlock()
	if ship, ok := ships[name]; ok {
		return ship
	}
	socketPath := socketPath(root, name)
	ship := &ShipState{
		Name:       name,
		SocketPath: socketPath,
	}
	ships[name] = ship
	return ship
}

func handleShipLaunch(name string, config ShipConfig) error {
	ship := getOrCreateShip(name)
	ship.Config = config

	if err := os.MkdirAll(filepath.Dir(ship.SocketPath), 0o755); err != nil {
		return fmt.Errorf("mkdir socket dir: %w", err)
	}

	os.Remove(ship.SocketPath)

	listener, err := net.Listen("unix", ship.SocketPath)
	if err != nil {
		return fmt.Errorf("listen on %s: %w", ship.SocketPath, err)
	}
	ship.Listener = listener

	fmt.Fprintf(os.Stderr, "ship-supervisor: Ship %s listener on %s\n", name, ship.SocketPath)

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				select {
				case <-ship.Done():
					return
				default:
					fmt.Fprintf(os.Stderr, "ship-supervisor: accept failed for %s: %v\n", name, err)
					continue
				}
			}
			go handleControlConn(ship, conn)
		}
	}()

	if err := startChild(ship, config.Model); err != nil {
		return err
	}

	return nil
}

func (s *ShipState) Done() <-chan struct{} {
	return nil
}

func startChild(ship *ShipState, model string) error {
	ship.Mu.Lock()
	defer ship.Mu.Unlock()

	if ship.ChildCmd != nil && ship.ChildCmd.Process != nil {
		_ = ship.ChildCmd.Process.Kill()
		ship.ChildCmd.Wait()
	}

	effectiveModel := model
	if effectiveModel == "" {
		effectiveModel = ship.Config.Model
	}
	if effectiveModel == "" {
		effectiveModel = "nvidia/nemotron-3-ultra-550b-a55b"
	}
	ship.CurrentModel = effectiveModel

	opencodeConfigPath, err := generateOpencodeConfig(
		root, ship.Name, ship.Config.LaunchType, ship.Config.Unrestricted, effectiveModel, ship.Config.Class)
	if err != nil {
		return fmt.Errorf("generate opencode config: %w", err)
	}

	sessionID := ""
	if !ship.Config.ForceNew {
		sessionID, err = findLastSessionForShip(root, ship.Name)
		if err != nil {
			fmt.Fprintf(os.Stderr, "ship-supervisor: session lookup failed: %v\n", err)
		}
	}

	inner := "export OPENCODE_CONFIG=" + shellQuote(opencodeConfigPath) + "; "
	inner += "exec " + shellQuote(resolveClientPath("opencode"))
	inner += " --model " + shellQuote(effectiveModel)
	if sessionID != "" {
		inner += " --session " + shellQuote(sessionID)
	}

	flagship := shipnames.FlagshipName(root)
	shipPrompt := "You are fleet ship " + ship.Name + ", report to flagship " + flagship + "."
	if ship.Config.LaunchType == "background" || ship.Config.LaunchType == "auto" {
		shipPrompt += " You were launched in " + ship.Config.LaunchType + " mode (detached, no human at your " +
			"console): never prompt for input on your console and never block waiting for stdin — " +
			"communicate only via the agent bus (starfleetctl comms tell/ask). Act autonomously " +
			"and report results back over the bus."
	}
	inner += " --prompt " + shellQuote(shipPrompt) + " "

	pipePath := pipePath(root, ship.Name)
	if err := os.MkdirAll(filepath.Dir(pipePath), 0o755); err != nil {
		return fmt.Errorf("mkdir pipe dir: %w", err)
	}

	logPath := logPath(root, ship.Name)
	_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open log file: %w", err)
	}

	self, err := os.Executable()
	if err != nil {
		return fmt.Errorf("os.Executable: %w", err)
	}

	childEnv := append(os.Environ(),
		"MPBT_WORKSPACE_ROOT="+root,
		"STARFLEET_SHIP_ID="+ship.Name,
		"STARFLEET_CLASS="+ship.Config.Class,
	)

	ship.ChildCmd = exec.Command(self, "termctl-run", ship.Name, pipePath, inner)
	ship.ChildCmd.Env = childEnv
	ship.ChildCmd.Stdin = nil
	ship.ChildCmd.Stdout = logFile
	ship.ChildCmd.Stderr = logFile

	if err := ship.ChildCmd.Start(); err != nil {
		logFile.Close()
		return fmt.Errorf("spawn termctl-run: %w", err)
	}

	ship.ChildPid = ship.ChildCmd.Process.Pid

	pidPath := pidPath(root, ship.Name)
	_ = os.WriteFile(pidPath, []byte(fmt.Sprintf("%d", ship.ChildPid)), 0o644)

	fmt.Fprintf(os.Stderr, "ship-supervisor: Started child for %s (pid=%d, model=%s)\n", ship.Name, ship.ChildPid, effectiveModel)

	os.Setenv("STARFLEET_SHIP_ID", ship.Name)
	if bus, err := comms.New(root); err == nil {
		provider := ship.Config.Provider
		if provider == "" {
			provider = providerFromModel(ship.Config.Model)
		}
		_ = bus.DoStatus("starting", "launched via supervisor (opencode-ship)", comms.StatusPatch{
			LaunchType: ship.Config.LaunchType,
			Parent:     ship.Config.Parent,
			Provider:   provider,
			Model:      ship.Config.Model,
		})
	}
	return nil
}

func handleControlConn(ship *ShipState, conn net.Conn) {
	defer conn.Close()
	buf := make([]byte, 1024)
	n, err := conn.Read(buf)
	if err != nil {
		return
	}
	command := strings.TrimSpace(string(buf[:n]))
	fmt.Fprintf(os.Stderr, "ship-supervisor: Control command for %s: %s\n", ship.Name, command)

	parts := strings.SplitN(command, " ", 2)
	cmd := strings.ToUpper(strings.TrimSpace(strings.Split(parts[0], "\n")[0]))
	args := ""
	if len(parts) > 1 {
		args = strings.TrimSpace(parts[1])
	}

	switch cmd {
	case "RESTART":
		newModel := ""
		if strings.HasPrefix(args, "model=") {
			newModel = strings.TrimPrefix(args, "model=")
		}
		fmt.Fprintf(os.Stderr, "ship-supervisor: RESTART %s (model=%q)\n", ship.Name, newModel)
		if err := startChild(ship, newModel); err != nil {
			fmt.Fprintf(os.Stderr, "ship-supervisor: Restart failed for %s: %v\n", ship.Name, err)
			conn.Write([]byte("ERROR\n"))
		} else {
			conn.Write([]byte("OK\n"))
		}
	case "STOP":
		ship.Mu.Lock()
		if ship.ChildCmd != nil && ship.ChildCmd.Process != nil {
			_ = ship.ChildCmd.Process.Kill()
			ship.ChildCmd.Wait()
		}
		ship.Mu.Unlock()
		conn.Write([]byte("OK\n"))
	case "STATUS":
		ship.Mu.Lock()
		running := ship.ChildCmd != nil && ship.ChildCmd.Process != nil
		pid := ship.ChildPid
		ship.Mu.Unlock()
		if running {
			conn.Write([]byte(fmt.Sprintf("RUNNING pid=%d\n", pid)))
		} else {
			conn.Write([]byte("STOPPED\n"))
		}
	default:
		conn.Write([]byte("UNKNOWN COMMAND\n"))
	}
}

func generateOpencodeConfig(root, shipID, launchType string, unrestricted bool, model, class string) (string, error) {
	cfg, err := config.Load(root)
	if err != nil {
		cfg = config.DefaultConfig()
	}

	var userConfig map[string]any
	if cfg.Fleet.ProviderMode != "model-proxy-only" {
		userConfigPath := filepath.Join(os.Getenv("HOME"), ".config", "opencode", "opencode.json")
		if data, err := os.ReadFile(userConfigPath); err == nil {
			_ = json.Unmarshal(data, &userConfig)
		}
	}

	proxyProviders := modelproxy.ProviderConfigs(root, shipID)

	effectiveModel := model
	if proxyProviders != nil && model != "" {
		mpCfg, err := modelproxy.Load(root)
		if err == nil {
			provider := mpCfg.FindProviderForModel(model)
			if provider != "" {
				if i := strings.IndexByte(model, '/'); i < 0 || model[:i] != provider {
					effectiveModel = provider + "/" + model
				}
			}
		}
	}

	shipConfig := map[string]any{
		"$schema":  "https://opencode.ai/config.json",
		"username": shipID,
		"model":    effectiveModel,
	}

	if proxyProviders != nil {
		provs, _ := shipConfig["provider"].(map[string]any)
		if provs == nil {
			provs = map[string]any{}
		}
		for id, entry := range proxyProviders {
			provs[id] = entry
		}
		shipConfig["provider"] = provs

		if cfg.Fleet.ProviderMode == "model-proxy-only" {
			ids := make([]string, 0, len(proxyProviders))
			for id := range proxyProviders {
				ids = append(ids, id)
			}
			sort.Strings(ids)
			shipConfig["enabled_providers"] = ids
		}
	}

	bashRules := map[string]string{
		"**":                                   "allow",
		"**/.starfleet-ai/bin/starfleetctl **": "allow",
		"starfleetctl **":                      "allow",
	}

	permission := map[string]any{
		"bash":  bashRules,
		"read":  map[string]string{"**": "allow"},
		"write": map[string]string{"**": "allow"},
		"edit":  map[string]string{"**": "allow"},
		"glob":  map[string]string{"**": "allow"},
		"grep":  map[string]string{"**": "allow"},
		"task":  map[string]string{"**": "allow"},
	}

	absWorkspacePattern := root + "/**"
	absLocalBinPattern := filepath.Join(os.Getenv("HOME"), ".local", "bin", "**")
	externalDirRules := map[string]string{
		absWorkspacePattern: "allow",
		absLocalBinPattern:  "allow",
	}
	if unrestricted {
		externalDirRules["**"] = "allow"
	} else if launchType == "terminal" {
		externalDirRules["**"] = "ask"
	} else {
		externalDirRules["**"] = "deny"
	}
	permission["external_directory"] = externalDirRules

	if launchType != "terminal" && !unrestricted {
		permission["question"] = "deny"
	}

	shipConfig["permission"] = permission
	shipConfig["instructions"] = []string{".starfleet-ai/var/sop.d/index.md"}
	shipConfig["plugin"] = []string{"./.opencode/plugins/starfleet-dispatch.ts"}

	configPath := opencodeConfigPath(root, shipID)
	if err := os.MkdirAll(filepath.Dir(configPath), 0o755); err != nil {
		return "", err
	}
	data, err := json.MarshalIndent(shipConfig, "", "  ")
	if err != nil {
		return "", err
	}
	data = append(data, '\n')
	if err := os.WriteFile(configPath, data, 0o644); err != nil {
		return "", err
	}
	return configPath, nil
}

func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", "'\"'\"'") + "'"
}

func resolveClientPath(name string) string {
	switch name {
	case "opencode":
		return "/home/nekrad/.local/bin/opencode"
	case "claude":
		return "claude"
	default:
		return name
	}
}

func providerFromModel(model string) string {
	if i := strings.IndexByte(model, '/'); i >= 0 {
		return model[:i]
	}
	return ""
}

// Helper functions (copied from session package)
func socketPath(root, shipID string) string {
	return filepath.Join(root, ".starfleet-ai", "var", "ships", shipID+".soc")
}

func pipePath(root, shipID string) string {
	return filepath.Join(root, ".starfleet-ai", "var", "ships", shipID+".pipe")
}

func logPath(root, shipID string) string {
	return filepath.Join(root, ".starfleet-ai", "var", "ships", shipID+".log")
}

func pidPath(root, shipID string) string {
	return filepath.Join(root, ".starfleet-ai", "var", "ships", shipID+".pid")
}

func opencodeConfigPath(root, shipID string) string {
	return filepath.Join(root, ".starfleet-ai", "var", "ships", shipID+".opencode.json")
}

func findLastSessionForShip(root, shipName string) (string, error) {
	sessions, err := ocsessions.List(ocsessions.ListOpts{
		Title: shipName,
		Limit: 1,
	})
	if err != nil {
		return "", nil
	}
	if len(sessions) > 0 && sessions[0].ID != "" {
		return sessions[0].ID, nil
	}
	return "", nil
}
