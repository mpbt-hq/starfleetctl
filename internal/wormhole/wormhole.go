// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Wormhole manager for starfleetctl — handles incoming/outgoing messages
// between fleet ships via the comms bus.
package wormhole

import (
	"fmt"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"
)

// Config holds the wormhole manager configuration.
type Config struct {
	ListenAddr        string        `yaml:"listen_addr"`
	PIDFile           string        `yaml:"pid_file"`
	StateFile         string        `yaml:"state_file"`
	MaxMessageAge     time.Duration `yaml:"max_message_age"`
	AutoStart         bool          `yaml:"auto_start"`
	ForwardMode       string        `yaml:"forward_mode"`
	WorkerPollInterval time.Duration `yaml:"worker_poll_interval"`
	MaxConcurrent     int           `yaml:"max_concurrent"`
	PersistState      bool          `yaml:"persist_state"`
	Repositories      []GitSyncConfig `yaml:"repositories"`
}

// DefaultConfig returns default wormhole configuration.
func DefaultConfig() *Config {
	return &Config{
		ListenAddr:        "127.0.0.1:9999",
		PIDFile:           "pid",
		StateFile:         "state",
		MaxMessageAge:     time.Hour,
		AutoStart:         true,
		ForwardMode:       "targeted",
		WorkerPollInterval: 3 * time.Second,
		MaxConcurrent:     5,
		PersistState:      true,
	}
}

// Message represents a wormhole message between ships.
type Message struct {
	ID        string    `json:"id"`
	From      string    `json:"from"`
	To        string    `json:"to"`
	Content   string    `json:"content"`
	Timestamp time.Time `json:"timestamp"`
	State     string    `json:"state"`
}

// NewMessage creates a new wormhole message.
func NewMessage(from, to, typ, content string) *Message {
	return &Message{
		ID:        fmt.Sprintf("msg_%d", time.Now().Unix()),
		From:      from,
		To:        to,
		Content:   content,
		Timestamp: time.Now(),
		State:     "pending",
	}
}

// WorkDir returns the wormhole data directory under the workspace root.
func WorkDir(root string) string {
	return filepath.Join(root, ".starfleet-ai", "var", "wormholes")
}

// EnsureDir creates the wormhole data directory if it doesn't exist.
func EnsureDir(root string) error {
	return os.MkdirAll(WorkDir(root), 0o755)
}

// PIDFile returns the path to the PID file.
func PIDFile(root string) string {
	return filepath.Join(WorkDir(root), DefaultConfig().PIDFile)
}

// StateFile returns the path to the state file.
func StateFile(root string) string {
	return filepath.Join(WorkDir(root), DefaultConfig().StateFile)
}

// ConfigFile returns the path to the configuration file.
func ConfigFile(root string) string {
	return filepath.Join(root, ".starfleet-ai", "conf", "wormholes.yaml")
}

// LoadConfig loads wormhole configuration from file, falling back to defaults.
func LoadConfig(root string) *Config {
	cfg := DefaultConfig()
	cfgPath := ConfigFile(root)
	data, err := os.ReadFile(cfgPath)
	if err != nil {
		return cfg
	}
	if err := parseYAMLConfig(data, cfg); err != nil {
		return cfg
	}
	return cfg
}

// parseYAMLConfig performs basic YAML parsing for wormhole config.
func parseYAMLConfig(data []byte, cfg *Config) error {
	lines := strings.Split(string(data), "\n")
	for _, line := range lines {
		line = strings.TrimSpace(line)
		if strings.HasPrefix(line, "#") || line == "" {
			continue
		}
		parts := strings.SplitN(line, ":", 2)
		if len(parts) != 2 {
			continue
		}
		key := strings.TrimSpace(parts[0])
		val := strings.TrimSpace(parts[1])
		val = strings.Trim(val, "\"")
		switch key {
		case "listen_addr":
			cfg.ListenAddr = val
		case "pid_file":
			cfg.PIDFile = val
		case "state_file":
			cfg.StateFile = val
		case "max_message_age":
			if d, err := time.ParseDuration(val); err == nil {
				cfg.MaxMessageAge = d
			}
		case "auto_start":
			cfg.AutoStart = val == "true"
		case "forward_mode":
			cfg.ForwardMode = val
		case "worker_poll_interval":
			if d, err := time.ParseDuration(val); err == nil {
				cfg.WorkerPollInterval = d
			}
		case "max_concurrent":
			var n int
			if _, err := fmt.Sscanf(val, "%d", &n); err == nil {
				cfg.MaxConcurrent = n
			}
		case "repositories":
			// Repositories parsing - handled by caller
		}
	}
	return nil
}

// daemonState holds the current state of the wormhole daemon.
type daemonState struct {
	status   string
	started  string
	stopped  string
	pid      string
	pidFile  string
	stateFile string
	root     string
	cfg      *Config
	running  bool
}

// daemonInstance holds the running daemon state.
var daemonInstance *daemonState

// StartDaemon starts the wormhole manager daemon.
func StartDaemon(root string) error {
	if daemonInstance != nil {
		return fmt.Errorf("wormhole daemon already running")
	}

	if err := EnsureDir(root); err != nil {
		return fmt.Errorf("wormhole daemon dir: %w", err)
	}

	cfg := LoadConfig(root)
	d := &daemonState{
		status:   "starting",
		started:  time.Now().Format(time.RFC3339),
		stopped:  "",
		pid:      fmt.Sprintf("%d", os.Getpid()),
		pidFile:  PIDFile(root),
		stateFile: StateFile(root),
		root:     root,
		cfg:      cfg,
		running:  true,
	}
	daemonInstance = d

	// Write PID file
	if err := os.WriteFile(d.pidFile, []byte(d.pid), 0o644); err != nil {
		daemonInstance = nil
		return fmt.Errorf("wormhole pid file: %w", err)
	}

	// Write initial state
	stateData := fmt.Sprintf(`{"status":"starting","started":"%s"}`, d.started)
	if err := os.WriteFile(d.stateFile, []byte(stateData), 0o644); err != nil {
		daemonInstance = nil
		return fmt.Errorf("wormhole state file: %w", err)
	}

	daemonInstance.status = "running"
	return nil
}

// StopDaemon stops the wormhole manager daemon.
func StopDaemon() error {
	if daemonInstance == nil {
		return fmt.Errorf("wormhole daemon not running")
	}

	daemonInstance.running = false

	// Update state to stopped
	stateFile := daemonInstance.stateFile
	stateData := fmt.Sprintf(`{"status":"stopped","stopped":"%s"}`, time.Now().Format(time.RFC3339))
	if err := os.WriteFile(stateFile, []byte(stateData), 0o644); err != nil {
		return fmt.Errorf("wormhole state file: %w", err)
	}

	// Remove PID file
	if err := os.Remove(daemonInstance.pidFile); err != nil {
		return fmt.Errorf("wormhole pid file: %w", err)
	}

	daemonInstance = nil
	return nil
}

// Status returns the current daemon status.
func Status() string {
	if daemonInstance == nil {
		return "Wormhole daemon: not running\n"
	}
	return fmt.Sprintf("Wormhole Daemon Status:\n  PID: %s\n  Status: %s\n  Started: %s\n",
		daemonInstance.pid, daemonInstance.status, daemonInstance.started)
}

// Autostart starts the wormhole daemon if the configuration enables it.
// Returns true if the daemon was started (or is already running), false if
// wormholes are disabled in config.
func Autostart(root string) (bool, error) {
	cfg := LoadConfig(root)
	if !cfg.AutoStart {
		return false, nil
	}
	if err := StartDaemon(root); err != nil {
		return false, err
	}
	return true, nil
}

// IsRunning returns whether the daemon is currently running.
func IsRunning() bool {
	return daemonInstance != nil && daemonInstance.running
}

// RunLoop runs the daemon's main processing loop.
func RunLoop(root string) error {
	if daemonInstance == nil {
		return fmt.Errorf("wormhole daemon not started")
	}

	cfg := daemonInstance.cfg
	ticker := time.NewTicker(cfg.WorkerPollInterval)
	defer ticker.Stop()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)

	// Immediate first sync: sync repos + process comms inbox
	if err := syncAllRepos(root); err != nil {
		// Log but continue
	}
	if err := processPendingMessages(root); err != nil {
		// Log but continue
	}

	for {
		select {
		case <-ticker.C:
			if err := syncAllRepos(root); err != nil {
				// Log but continue
			}
			if err := processPendingMessages(root); err != nil {
				// Log but continue
			}
		case <-sigChan:
			return StopDaemon()
		}
	}
}

// processPendingMessages processes any pending wormhole messages.
func processPendingMessages(root string) error {
	inboxDir := filepath.Join(root, ".starfleet-ai", "var", "comms", "msgs")
	entries, err := os.ReadDir(inboxDir)
	if err != nil {
		return err
	}

	for _, entry := range entries {
		if !entry.IsDir() {
			continue
		}
		shipDir := filepath.Join(inboxDir, entry.Name(), "unseen")
		msgEntries, err := os.ReadDir(shipDir)
		if err != nil {
			continue
		}
		for _, msgEntry := range msgEntries {
			if !msgEntry.IsDir() && strings.HasSuffix(msgEntry.Name(), ".json") {
				msgPath := filepath.Join(shipDir, msgEntry.Name())
				data, err := os.ReadFile(msgPath)
				if err != nil {
					continue
				}
				msg := parseMessageFromFile(data)
				if msg != nil {
					if err := RouteMessage(root, msg); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// syncAllRepos syncs all configured repositories.
func syncAllRepos(root string) error {
	cfg := LoadConfig(root)
	if !cfg.AutoStart {
		return nil
	}
	for _, repo := range cfg.Repositories {
		if !repo.Enabled {
			continue
		}
		gs := NewGitSync(root, repo)
		if _, err := gs.Sync(); err != nil {
			// Log but continue with other repos
		}
	}
	return nil
}

// parseMessageFromFile parses a message from a JSON file.
func parseMessageFromFile(data []byte) *Message {
	msg := &Message{}
	parts := strings.Split(string(data), ",")
	for _, part := range parts {
		kv := strings.SplitN(part, ":", 2)
		if len(kv) != 2 {
			continue
		}
		key := strings.TrimSpace(kv[0])
		val := strings.TrimSpace(strings.Trim(kv[1], "\""))
		switch key {
		case `"id"`:
			msg.ID = val
		case `"from"`:
			msg.From = val
		case `"to"`:
			msg.To = val
		case `"content"`:
			msg.Content = val
		case `"state"`:
			msg.State = val
		}
	}
	if msg.ID == "" {
		return nil
	}
	return msg
}
