// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Wormhole manager for starfleetctl — a Comms transport backend.
//
// Wormhole is NOT a parallel messaging system: received messages are imported
// only through Comms Core's external-ingestion API (Bus.PostExternal), and the
// agent/session plugin only ever reads the local Comms inbox. See
// decisions/2026-11-08--wormhole-is-comms-transport.md (wormhole-0 repo).
package wormhole

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/metux/starfleetctl/internal/comms"
	"github.com/metux/starfleetctl/internal/config"
	"github.com/metux/starfleetctl/internal/flock"
	"github.com/metux/starfleetctl/internal/shipnames"
	"gopkg.in/yaml.v3"
)

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
	return filepath.Join(WorkDir(root), "pid")
}

// StateFile returns the path to the state file.
func StateFile(root string) string {
	return filepath.Join(WorkDir(root), "state")
}

// ConfigFile returns the path to the wormhole configuration file.
func ConfigFile(root string) string {
	return filepath.Join(root, ".starfleet-ai", "conf", "wormholes.yaml")
}

// DefaultConfig returns default wormhole configuration.
func DefaultConfig() *config.WormholesConfig {
	return &config.WormholesConfig{
		Enabled: false,
		Sync: config.SyncConfig{
			Interval:       "5s",
			StartupSync:    true,
			CloneTimeout:   "2m",
			CommandTimeout: "30s",
			PushRetries:    3,
		},
	}
}

// validateAccessMode checks that access is "read-only" or "read-write".
func validateAccessMode(access string) error {
	switch access {
	case "", "read-only", "read-write":
		return nil
	default:
		return fmt.Errorf("invalid access mode %q: must be read-only or read-write", access)
	}
}

// validateInitialScan checks that initial_scan is "baseline" or "replay".
func validateInitialScan(scan string) error {
	switch scan {
	case "", "baseline", "replay":
		return nil
	default:
		return fmt.Errorf("invalid initial_scan %q: must be baseline or replay", scan)
	}
}

// ValidateConfig checks the wormhole configuration for required values,
// ranges, durations, and duplicate IDs.
// Schema/syntax/range validation is unconditional (runs even when disabled);
// active-repository requirements (remote/branch) are conditional on Enabled.
func ValidateConfig(cfg *config.WormholesConfig) error {
	if cfg == nil {
		return fmt.Errorf("wormhole config is nil")
	}

	interval, err := time.ParseDuration(cfg.Sync.Interval)
	if err != nil {
		return fmt.Errorf("invalid sync.interval %q: %w", cfg.Sync.Interval, err)
	}
	if interval <= 0 {
		return fmt.Errorf("sync.interval must be positive, got %q", cfg.Sync.Interval)
	}

	cloneTimeout, err := time.ParseDuration(cfg.Sync.CloneTimeout)
	if err != nil {
		return fmt.Errorf("invalid sync.clone_timeout %q: %w", cfg.Sync.CloneTimeout, err)
	}
	if cloneTimeout <= 0 {
		return fmt.Errorf("sync.clone_timeout must be positive, got %q", cfg.Sync.CloneTimeout)
	}

	commandTimeout, err := time.ParseDuration(cfg.Sync.CommandTimeout)
	if err != nil {
		return fmt.Errorf("invalid sync.command_timeout %q: %w", cfg.Sync.CommandTimeout, err)
	}
	if commandTimeout <= 0 {
		return fmt.Errorf("sync.command_timeout must be positive, got %q", cfg.Sync.CommandTimeout)
	}

	if cfg.Sync.PushRetries < 0 || cfg.Sync.PushRetries > 10 {
		return fmt.Errorf("invalid sync.push_retries %d: must be 0 to 10", cfg.Sync.PushRetries)
	}

	seenIDs := make(map[string]bool)
	for i, repo := range cfg.Repositories {
		if repo.ID == "" {
			return fmt.Errorf("repositories[%d]: id must not be empty", i)
		}
		if !validRepoID(repo.ID) {
			return fmt.Errorf("repositories[%d]: id %q must consist solely of ASCII letters, digits, '-', '_'", i, repo.ID)
		}
		if seenIDs[repo.ID] {
			return fmt.Errorf("repositories[%d]: duplicate id %q", i, repo.ID)
		}
		seenIDs[repo.ID] = true

		if err := validateAccessMode(repo.Access); err != nil {
			return fmt.Errorf("repositories[%d] %q: %w", i, repo.ID, err)
		}
		if err := validateInitialScan(repo.Incoming.InitialScan); err != nil {
			return fmt.Errorf("repositories[%d] %q: incoming.initial_scan: %w", i, repo.ID, err)
		}
	}

	if !cfg.Enabled {
		return nil
	}

	for i, repo := range cfg.Repositories {
		if !repoEnabled(repo) {
			continue
		}
		if repo.Remote == "" {
			return fmt.Errorf("repositories[%d] %q: remote is required for enabled repository", i, repo.ID)
		}
		if repo.Branch == "" {
			return fmt.Errorf("repositories[%d] %q: branch is required for enabled repository", i, repo.ID)
		}
	}
	return nil
}

// repoEnabled reports whether a repository is enabled (default: true).
func repoEnabled(repo config.RepositoryConfig) bool {
	return repo.Enabled == nil || *repo.Enabled
}

// incomingEnabled reports whether message import is enabled (default: true).
func incomingEnabled(repo config.RepositoryConfig) bool {
	return repo.Incoming.Enabled == nil || *repo.Incoming.Enabled
}

// validRepoID checks that the repository ID consists solely of safe
// ASCII characters: letters, digits, '-', '_'.
var validRepoIDRe = regexp.MustCompile(`^[A-Za-z0-9_-]+$`)

func validRepoID(id string) bool {
	return validRepoIDRe.MatchString(id)
}

// LoadConfig loads wormhole configuration from wormholes.yaml.
// Missing files return defaults. I/O errors and parse errors are returned
// as errors. The canonical schema is internal/config.WormholesConfig.
// Uses strict YAML decoding (KnownFields) to reject unknown keys at all levels.
func LoadConfig(root string) (*config.WormholesConfig, error) {
	cfgPath := ConfigFile(root)

	data, err := os.ReadFile(cfgPath)
	if err != nil {
		if os.IsNotExist(err) {
			return DefaultConfig(), nil
		}
		return nil, fmt.Errorf("read wormhole config %s: %w", cfgPath, err)
	}

	var raw map[string]yaml.Node
	if err := yaml.Unmarshal(data, &raw); err != nil {
		return nil, fmt.Errorf("parse wormhole config %s: %w", cfgPath, err)
	}
	wormNode, ok := raw["wormholes"]
	if !ok {
		return nil, fmt.Errorf("wormhole config %s: missing required top-level key 'wormholes'", cfgPath)
	}
	if err := validateYAMLKeys(&wormNode, "wormholes"); err != nil {
		return nil, fmt.Errorf("wormhole config %s: %w", cfgPath, err)
	}

	var cfg config.WormholesConfig
	if err := wormNode.Decode(&cfg); err != nil {
		return nil, fmt.Errorf("decode wormhole config %s: %w", cfgPath, err)
	}

	if err := ValidateConfig(&cfg); err != nil {
		return nil, fmt.Errorf("validate wormhole config %s: %w", cfgPath, err)
	}

	return &cfg, nil
}

// allKnownKeys maps each YAML mapping path to the set of keys allowed at that
// path. No global-known-key fallback is used: any mapping not present in this
// map fails closed with an "unknown key" error.
//
// The schema is fixed (W0-129/W0-134): only repository identity/remote/branch
// and genuinely operational settings remain. Legacy incoming/outgoing folder
// configuration (paths, messages, decisions, target_aliases) is rejected.
var allKnownKeys = map[string]map[string]bool{
	"wormholes": {
		"enabled": true, "sync": true, "repositories": true,
	},
	"wormholes.sync": {
		"interval": true, "startup_sync": true, "clone_timeout": true, "command_timeout": true, "push_retries": true,
	},
	"wormholes.repositories": {
		"id": true, "remote": true, "branch": true, "enabled": true, "access": true, "incoming": true,
	},
	"wormholes.repositories[]": {
		"id": true, "remote": true, "branch": true, "enabled": true, "access": true, "incoming": true,
	},
	"wormholes.repositories[].incoming": {
		"enabled": true, "initial_scan": true,
	},
}

// validateYAMLKeys recursively checks that all mapping keys in a YAML node are
// known schema keys, rejecting unexpected keys at all nesting levels.
// Fails closed: any mapping path not present in allKnownKeys returns an error.
func validateYAMLKeys(node *yaml.Node, path string) error {
	if node.Kind != yaml.MappingNode {
		return nil
	}
	allowed, ok := allKnownKeys[path]
	if !ok {
		return fmt.Errorf("unknown mapping path %q", path)
	}
	for i := 0; i < len(node.Content); i += 2 {
		keyNode := node.Content[i]
		valNode := node.Content[i+1]
		key := keyNode.Value
		if !allowed[key] {
			return fmt.Errorf("unknown key %q at %s", key, path)
		}
		if valNode.Kind == yaml.MappingNode {
			if err := validateYAMLKeys(valNode, path+"."+key); err != nil {
				return err
			}
		}
		if valNode.Kind == yaml.SequenceNode {
			for _, item := range valNode.Content {
				if item.Kind == yaml.MappingNode {
					if err := validateYAMLKeys(item, path+"."+key+"[]"); err != nil {
						return err
					}
				}
			}
		}
	}
	return nil
}

// daemonState holds the persistent state of the wormhole daemon.
// It is serialized to the state file so that status can be queried
// from a different process than the one that started the daemon.
type daemonState struct {
	Status  string `json:"status"`
	Started string `json:"started"`
	Stopped string `json:"stopped,omitempty"`
	PID     int    `json:"pid"`
	Root    string `json:"root"`
	Running bool   `json:"running"`
	Error   string `json:"error,omitempty"`
}

// StartLockPath returns the path to the start serialization lock file.
func StartLockPath(root string) string {
	return filepath.Join(WorkDir(root), "start.lock")
}

// isProcessAlive reports whether the process with the given PID is still alive.
func isProcessAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	p, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// Signal 0 checks for process existence without sending a signal.
	return p.Signal(syscall.Signal(0)) == nil
}

// pidFileRefusal formats the error for a present-but-unusable pid file. A
// present pid file that cannot be verified must never be interpreted as "no
// daemon": callers fail closed and leave the file for manual inspection.
func pidFileRefusal(path, reason string) error {
	return fmt.Errorf("wormhole: pid file %s %s — refusing to act on an unverified PID (remove it manually if you are sure no wormhole daemon is running)", path, reason)
}

// readPIDFile reads the daemon PID from the PID file, distinguishing:
//   - no pid file: (0, false, nil) — no process is registered
//   - present and valid positive PID: (pid, true, nil)
//   - present but empty, unparseable, non-positive or unreadable:
//     (0, true, error) — a present but unusable pid file is a hard failure,
//     never "no daemon"
func readPIDFile(path string) (pid int, present bool, err error) {
	data, rerr := os.ReadFile(path)
	if rerr != nil {
		if errors.Is(rerr, fs.ErrNotExist) {
			return 0, false, nil
		}
		return 0, true, pidFileRefusal(path, fmt.Sprintf("is unreadable: %v", rerr))
	}
	text := strings.TrimSpace(string(data))
	if text == "" {
		return 0, true, pidFileRefusal(path, "is empty")
	}
	pid, perr := strconv.Atoi(text)
	if perr != nil {
		return 0, true, pidFileRefusal(path, fmt.Sprintf("does not contain a valid PID (%q)", text))
	}
	if pid <= 0 {
		return 0, true, pidFileRefusal(path, fmt.Sprintf("contains non-positive PID %d", pid))
	}
	return pid, true, nil
}

// writePIDFile atomically writes the daemon PID to the PID file.
func writePIDFile(path string, pid int) error {
	tmp := path + ".tmp." + strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := os.WriteFile(tmp, []byte(fmt.Sprintf("%d", pid)), 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	// fsync the directory to ensure the rename is durable
	dir := filepath.Dir(path)
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

// readStateFile reads the daemon state from the state file.
// Returns (state, true) if the file exists and contains valid JSON.
func readStateFile(path string) (daemonState, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		return daemonState{}, false
	}
	var st daemonState
	if err := json.Unmarshal(data, &st); err != nil {
		return daemonState{}, false
	}
	return st, true
}

// writeStateFile atomically writes the daemon state to the state file.
func writeStateFile(path string, st daemonState) error {
	data, err := json.Marshal(st)
	if err != nil {
		return err
	}
	tmp := path + ".tmp." + strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	// fsync the directory to ensure the rename is durable
	dir := filepath.Dir(path)
	if d, err := os.Open(dir); err == nil {
		d.Sync()
		d.Close()
	}
	return nil
}

// isWormholeWorkerFor checks whether the given PID belongs to a wormhole
// worker process started for THIS workspace root. Identity is established via
// /proc (best-effort, with an inherent TOCTOU window — callers must therefore
// never rely on this check alone before signaling; use resolveDaemon instead).
// It matches both the command line ("wormhole run-internal") and the worker's
// MPBT_WORKSPACE_ROOT environment, so a reused PID that happens to be another
// workspace's wormhole worker is not mistaken for ours.
func isWormholeWorkerFor(pid int, root string) bool {
	if pid <= 0 {
		return false
	}
	procDir := filepath.Join("/proc", strconv.Itoa(pid))
	cmdline, err := os.ReadFile(filepath.Join(procDir, "cmdline"))
	if err != nil {
		return false
	}
	if !strings.Contains(string(cmdline), "wormhole") || !strings.Contains(string(cmdline), "run-internal") {
		return false
	}
	// Cross-check the workspace root the worker was started with. A NULL
	// byte separates entries in /proc/<pid>/environ.
	environ, err := os.ReadFile(filepath.Join(procDir, "environ"))
	if err != nil {
		return false
	}
	want := "MPBT_WORKSPACE_ROOT=" + root
	for _, kv := range bytes.Split(environ, []byte{0}) {
		if string(kv) == want {
			return true
		}
	}
	return false
}

// daemonInstance is the daemon identity recorded in the runtime files,
// resolved by resolveDaemon.
type daemonInstance struct {
	PID   int
	State daemonState
}

// resolveDaemon applies the single shared daemon-identity logic used by
// start, stop, status and autostart:
//   - no PID file: no instance is recorded (nil, nil)
//   - PID file present but empty, unparseable, non-positive or unreadable:
//     hard error — a present but unusable PID file must never be read as
//     "no daemon", or start could spawn a second worker over a recorded one
//   - PID file present but state file missing or corrupt: hard error — the
//     recorded PID cannot be verified against its recorded instance, and an
//     unverifiable PID must never be signaled or overwritten silently
//   - PID file and state file disagree on the PID: hard error (possible PID
//     reuse — refuse to act on it)
//   - state records a different workspace root: hard error (files do not
//     belong to this workspace)
//
// A resolved instance is NOT necessarily alive; use instanceAlive to check.
func resolveDaemon(root string) (*daemonInstance, error) {
	pid, present, err := readPIDFile(PIDFile(root))
	if err != nil {
		return nil, err
	}
	if !present {
		return nil, nil
	}
	st, ok := readStateFile(StateFile(root))
	if !ok {
		return nil, fmt.Errorf("wormhole: state file missing or corrupt while pid file records PID %d — refusing to act on an unverified PID (remove %s and %s manually if you are sure)", pid, PIDFile(root), StateFile(root))
	}
	if st.PID != pid {
		return nil, fmt.Errorf("wormhole: pid file (%d) and state file (%d) disagree — possible PID reuse, refusing to act (remove %s and %s manually if you are sure)", pid, st.PID, PIDFile(root), StateFile(root))
	}
	if st.Root != "" && st.Root != root {
		return nil, fmt.Errorf("wormhole: state file records root %q, not this workspace %q — refusing to act", st.Root, root)
	}
	return &daemonInstance{PID: pid, State: st}, nil
}

// instanceAlive reports whether a resolved instance is still running as the
// very process it was recorded for. When in doubt (cannot verify identity),
// it reports false — callers must never signal a process they cannot verify.
func (inst *daemonInstance) instanceAlive(root string) bool {
	if inst == nil {
		return false
	}
	return isProcessAlive(inst.PID) && isWormholeWorkerFor(inst.PID, root)
}

// StartDaemon starts the wormhole manager daemon.
// It forks a child process that runs "wormhole run-internal".
// The child writes its own PID to the PID file and a "ready" file;
// the parent polls for the ready file with a timeout to confirm successful
// start, and detects an early child exit immediately (instead of waiting
// for the full timeout).
// Uses a start lock to serialize concurrent start attempts.
func StartDaemon(root string) error {
	if err := EnsureDir(root); err != nil {
		return fmt.Errorf("wormhole daemon dir: %w", err)
	}
	_, err := LoadConfig(root)
	if err != nil {
		return fmt.Errorf("wormhole config: %w", err)
	}

	// Acquire start lock to serialize concurrent start/autostart attempts.
	lockPath := StartLockPath(root)
	lock, err := flock.Lock(lockPath, flock.Options{
		Timeout:     10 * time.Second,
		HolderLabel: "wormhole:start",
	})
	if err != nil {
		return fmt.Errorf("wormhole: could not acquire start lock: %w", err)
	}
	defer lock.Close()

	// Apply the shared identity rules: an instance recorded with
	// unverifiable state (missing/corrupt state file, PID disagreement) is a
	// hard error — we must not silently overwrite or signal it.
	inst, rerr := resolveDaemon(root)
	if rerr != nil {
		return rerr
	}
	if inst != nil {
		if inst.instanceAlive(root) {
			return fmt.Errorf("wormhole daemon already running (pid %d)", inst.PID)
		}
		// Stale PID file (process dead or not our worker). Clean up and start.
		_ = os.Remove(PIDFile(root))
		_ = os.Remove(StateFile(root))
		_ = os.Remove(filepath.Join(WorkDir(root), "ready"))
	}

	// Fork a child process to run the daemon.
	// Use STARFLEETCTL_BINARY env var if set (for tests), otherwise os.Args[0]
	binary := os.Getenv("STARFLEETCTL_BINARY")
	if binary == "" {
		binary = os.Args[0]
	}
	cmd := exec.Command(binary, "wormhole", "run-internal")
	cmd.Dir = root
	cmd.Env = append(os.Environ(),
		"MPBT_WORKSPACE_ROOT="+root,
		"STARFLEET_WORMHOLE_INTERNAL=1",
	)
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setsid: true, // Create new session, become process group leader
	}
	// The detached worker must never inherit the parent's stdout/stderr:
	// a caller that captures output via a pipe (e.g. CombinedOutput in tests)
	// would block on EOF forever because the worker keeps the write end open.
	// All worker output goes to a log file inside the daemon work directory.
	logFile, err := os.OpenFile(filepath.Join(WorkDir(root), "daemon.log"),
		os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("wormhole: open daemon log: %w", err)
	}
	defer logFile.Close() // the parent's fd only; the child keeps its own dup
	cmd.Stdout = logFile
	cmd.Stderr = logFile

	if err := cmd.Start(); err != nil {
		return fmt.Errorf("wormhole: failed to start daemon: %w", err)
	}

	childPID := cmd.Process.Pid

	// Reap the child when it exits, so an early crash (bad config, failed
	// startup sync) is detected immediately instead of keeping a zombie that
	// still answers signal 0. On the success path this goroutine is simply
	// abandoned when the parent returns.
	waitCh := make(chan error, 1)
	go func() { waitCh <- cmd.Wait() }()

	// Poll for the child to write its PID file and "ready" file, with timeout.
	// We wait up to 30 seconds for the worker to fully start (includes config load, git clone).
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		select {
		case werr := <-waitCh:
			// Child exited before becoming ready. Remove the PID and ready
			// files, but KEEP the state file: the worker records the failure
			// reason there (e.g. a failed startup sync) and `wormhole status`
			// surfaces it. The next successful start overwrites it.
			_ = os.Remove(PIDFile(root))
			_ = os.Remove(filepath.Join(WorkDir(root), "ready"))
			return fmt.Errorf("wormhole daemon exited during startup: %w", werr)
		default:
		}
		filePID, present, perr := readPIDFile(PIDFile(root))
		if perr == nil && present && filePID == childPID && isProcessAlive(childPID) {
			// Verify the ready file exists (worker completed startup)
			readyPath := filepath.Join(WorkDir(root), "ready")
			if _, err := os.Stat(readyPath); err == nil {
				return nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}

	// Timeout: child failed to start or crashed.
	// Kill the process group we started.
	if cmd.Process != nil {
		_ = syscall.Kill(-cmd.Process.Pid, syscall.SIGKILL)
	}
	// Clean up any partial files left by failed worker
	_ = os.Remove(PIDFile(root))
	_ = os.Remove(StateFile(root))
	_ = os.Remove(filepath.Join(WorkDir(root), "ready"))
	return fmt.Errorf("wormhole daemon failed to start within timeout (child PID %d)", childPID)
}

// StopDaemon stops the wormhole manager daemon.
// Identity is resolved with the same shared rules as start/status/autostart
// (resolveDaemon): a missing or corrupt state file while a PID file exists is
// a hard error — an unverifiable PID is never signaled. A recorded instance
// whose process is already dead is cleaned up and reported as not running.
// Uses a start lock to serialize with concurrent start attempts.
func StopDaemon(root string) error {
	// Acquire start lock to serialize with concurrent start attempts.
	lockPath := StartLockPath(root)
	lock, err := flock.Lock(lockPath, flock.Options{
		Timeout:     10 * time.Second,
		HolderLabel: "wormhole:stop",
	})
	if err != nil {
		return fmt.Errorf("wormhole: could not acquire start lock: %w", err)
	}
	defer lock.Close()

	inst, rerr := resolveDaemon(root)
	if rerr != nil {
		return rerr
	}
	if inst == nil {
		return fmt.Errorf("wormhole daemon not running (no pid file)")
	}

	// A recorded instance whose process is already dead (or cannot be
	// verified as OUR worker) must never be signaled: PID reuse could have
	// handed the PID to an unrelated process.
	if !isProcessAlive(inst.PID) {
		cleanupDaemonFiles(root)
		return fmt.Errorf("wormhole daemon not running (recorded pid %d is dead; cleaned up stale files)", inst.PID)
	}
	if !isWormholeWorkerFor(inst.PID, root) {
		// Live process, but not verifiable as our worker for this workspace.
		// When in doubt: never signal.
		return fmt.Errorf("wormhole: pid %d is alive but not verifiable as this workspace's wormhole worker — refusing to signal (verify manually, then remove %s and %s)", inst.PID, PIDFile(root), StateFile(root))
	}

	p, err := os.FindProcess(inst.PID)
	if err != nil {
		return fmt.Errorf("wormhole daemon pid %d not found: %w", inst.PID, err)
	}

	// Send SIGTERM to request graceful shutdown.
	if err := p.Signal(syscall.SIGTERM); err != nil {
		return fmt.Errorf("wormhole daemon pid %d signal failed: %w", inst.PID, err)
	}

	// Wait up to 10 seconds for the process to exit.
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if !isProcessAlive(inst.PID) {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if isProcessAlive(inst.PID) {
		// Force kill if still alive. The identity was verified above; between
		// then and now the process could in theory have exited and the PID
		// been reused, so re-verify before the SIGKILL.
		if !isWormholeWorkerFor(inst.PID, root) {
			return fmt.Errorf("wormhole: pid %d identity changed during shutdown — refusing SIGKILL", inst.PID)
		}
		if err := syscall.Kill(inst.PID, syscall.SIGKILL); err != nil {
			return fmt.Errorf("wormhole daemon pid %d kill failed: %w", inst.PID, err)
		}
		// Wait a bit more for SIGKILL to take effect
		time.Sleep(500 * time.Millisecond)
		if isProcessAlive(inst.PID) {
			return fmt.Errorf("wormhole daemon pid %d still alive after SIGKILL", inst.PID)
		}
	}

	// Clean up files only after process is confirmed dead.
	cleanupDaemonFiles(root)
	return nil
}

// cleanupDaemonFiles removes the daemon runtime files (pid, state, ready).
func cleanupDaemonFiles(root string) {
	_ = os.Remove(PIDFile(root))
	_ = os.Remove(StateFile(root))
	_ = os.Remove(filepath.Join(WorkDir(root), "ready"))
}

// Status returns the current daemon status. Identity is resolved with the
// same shared rules as start/stop/autostart (resolveDaemon); a recorded
// startup error is surfaced from the state file.
func Status(root string) string {
	inst, rerr := resolveDaemon(root)
	if rerr != nil {
		return fmt.Sprintf("Wormhole daemon: %v\n", rerr)
	}
	if inst == nil {
		// No PID file. A state file left from a failed startup (worker
		// exited with an error) still carries useful diagnostics.
		if st, ok := readStateFile(StateFile(root)); ok && st.Status == "error" {
			return fmt.Sprintf("Wormhole daemon: not running\n  Last status: error\n  Error: %s\n  Started: %s\n", st.Error, st.Started)
		}
		return "Wormhole daemon: not running\n"
	}

	alive := inst.instanceAlive(root)
	status := inst.State.Status
	if !alive && inst.State.Running {
		status = "dead"
	}
	return fmt.Sprintf("Wormhole Daemon Status:\n  PID: %d\n  Status: %s\n  Started: %s\n  Process alive: %v\n",
		inst.PID, status, inst.State.Started, alive)
}

// Autostart starts the wormhole daemon if the configuration enables it.
// It is idempotent: if a daemon is already running, it returns false,nil.
// Concurrent autostarts also converge: when StartDaemon loses the race
// ("already running"), the winner's daemon satisfies this call.
// Uses a start lock to serialize with concurrent start/stop attempts.
func Autostart(root string) (bool, error) {
	cfg, err := LoadConfig(root)
	if err != nil {
		return false, fmt.Errorf("wormhole config: %w", err)
	}
	if !cfg.Enabled {
		return false, nil
	}
	// Check if already running before attempting to start.
	if IsRunning(root) {
		return false, nil
	}
	if err := StartDaemon(root); err != nil {
		// Lost a race against a concurrent autostart/start: if a daemon is
		// now running, this call has achieved its goal.
		if IsRunning(root) {
			return false, nil
		}
		return false, err
	}
	return true, nil
}

// IsRunning returns whether the daemon is currently running, determined by
// the shared identity rules (resolveDaemon) plus a full liveness/identity
// check of the recorded instance.
func IsRunning(root string) bool {
	inst, err := resolveDaemon(root)
	if err != nil {
		return false
	}
	return inst.instanceAlive(root)
}

// RunLoop runs the daemon's main processing loop.
//
// Startup semantics:
//   - with sync.startup_sync enabled, the initial syncAllRepos() must succeed
//     before the worker becomes ready; a failed initial sync is fatal (the
//     worker exits with an error state) so misconfiguration is reported
//     immediately instead of silently running a dead transport
//   - with startup_sync disabled, the worker becomes ready without any sync
//   - per-cycle failures after startup are logged and retried on the next
//     tick — a transient network/Git error must never kill the transport
//     worker
//
// The daemon updates the state file on start and stop so that status can be
// queried from other processes, and creates the "ready" file only after
// successful startup to signal the parent process.
func RunLoop(root string, cfg *config.WormholesConfig) error {
	interval, err := time.ParseDuration(cfg.Sync.Interval)
	if err != nil {
		return fmt.Errorf("wormhole sync interval: %w", err)
	}
	ticker := time.NewTicker(interval)
	defer ticker.Stop()

	sigChan := make(chan os.Signal, 1)
	signal.Notify(sigChan, syscall.SIGINT, syscall.SIGTERM)
	defer signal.Stop(sigChan)

	fmt.Fprintf(os.Stderr, "[wormhole] RunLoop started, pid=%d\n", os.Getpid())

	runCycle := func() {
		if err := processPendingMessages(root); err != nil {
			fmt.Fprintf(os.Stderr, "[wormhole] cycle: %v\n", err)
		}
	}

	if cfg.Sync.StartupSync {
		if err := syncAllRepos(root); err != nil {
			// A failed initial sync is fatal: record the error in the state
			// file (so `wormhole status` shows it) and exit. runInternal()
			// then cleans up the pid/ready files and exits non-zero, which
			// the parent detects as an early child exit.
			st, _ := readStateFile(StateFile(root))
			st.Status = "error"
			st.Error = fmt.Sprintf("startup sync failed: %v", err)
			st.Running = false
			st.Stopped = time.Now().UTC().Format(time.RFC3339)
			_ = writeStateFile(StateFile(root), st)
			return fmt.Errorf("startup sync: %w", err)
		}
		runCycle()
	}

	// Mark as running in the state file.
	st, _ := readStateFile(StateFile(root))
	st.Status = "running"
	st.Running = true
	st.Error = ""
	if err := writeStateFile(StateFile(root), st); err != nil {
		fmt.Fprintf(os.Stderr, "[wormhole] state write failed: %v\n", err)
	}

	// Create ready file to signal successful startup to parent process.
	// At this point either startup_sync succeeded or was disabled.
	readyPath := filepath.Join(WorkDir(root), "ready")
	if err := os.WriteFile(readyPath, []byte("ready\n"), 0o644); err != nil {
		fmt.Fprintf(os.Stderr, "[wormhole] ready file write failed: %v\n", err)
	}

	for {
		select {
		case <-ticker.C:
			runCycle()
		case sig := <-sigChan:
			fmt.Fprintf(os.Stderr, "[wormhole] Received signal %v, stopping\n", sig)
			st, _ := readStateFile(StateFile(root))
			st.Status = "stopped"
			st.Stopped = time.Now().Format(time.RFC3339)
			st.Running = false
			if err := writeStateFile(StateFile(root), st); err != nil {
				fmt.Fprintf(os.Stderr, "[wormhole] state write failed: %v\n", err)
			}
			return nil
		}
	}
}

// processPendingMessages syncs every enabled repository and processes the
// mailboxes of all locally responsible ships for repositories with incoming
// enabled. The local ship set is determined centrally from existing Starfleet
// configuration and registry state (see localShips) — never from the daemon's
// user/host fallback identity. Each repository is handled under its own writer
// lock; failures of one mailbox or repository do not prevent processing others
// and are aggregated.
func processPendingMessages(root string) error {
	cfg, err := LoadConfig(root)
	if err != nil {
		return err
	}
	if !cfg.Enabled {
		return nil
	}
	ships, err := localShips(root)
	if err != nil {
		return fmt.Errorf("local ships: %w", err)
	}
	bus, err := comms.New(root)
	if err != nil {
		return fmt.Errorf("comms: %w", err)
	}
	cloneTimeout, _ := time.ParseDuration(cfg.Sync.CloneTimeout)
	commandTimeout, _ := time.ParseDuration(cfg.Sync.CommandTimeout)
	var errs []error
	for _, repo := range cfg.Repositories {
		if !repoEnabled(repo) {
			continue
		}
		syncer := newSpool(root, repo, cfg.Sync.PushRetries, "", cloneTimeout, commandTimeout)
		if !incomingEnabled(repo) || len(ships) == 0 {
			if err := syncer.runOnce(nil, false); err != nil {
				errs = append(errs, fmt.Errorf("repo %q: %w", repo.ID, err))
			}
			continue
		}
		shipSet := uniqueShips(ships)
		if err := syncer.RunCycleForShips(bus, shipSet); err != nil {
			errs = append(errs, fmt.Errorf("repo %q: %w", repo.ID, err))
		}
	}
	if len(errs) > 0 {
		return combineErrors(errs)
	}
	return nil
}

// syncAllRepos syncs all enabled repositories without processing mailboxes.
// The push retry count comes from the loaded configuration (sync.push_retries).
func syncAllRepos(root string) error {
	cfg, err := LoadConfig(root)
	if err != nil {
		return err
	}
	if !cfg.Enabled {
		return nil
	}
	cloneTimeout, _ := time.ParseDuration(cfg.Sync.CloneTimeout)
	commandTimeout, _ := time.ParseDuration(cfg.Sync.CommandTimeout)
	var errs []error
	for _, repo := range cfg.Repositories {
		if !repoEnabled(repo) {
			continue
		}
		sp := newSpool(root, repo, cfg.Sync.PushRetries, "", cloneTimeout, commandTimeout)
		if err := sp.runOnce(nil, false); err != nil {
			errs = append(errs, fmt.Errorf("repo %q: %w", repo.ID, err))
		}
	}
	if len(errs) > 0 {
		return combineErrors(errs)
	}
	return nil
}

// localShips returns the canonical set of locally responsible ship identities
// for this workspace. No single existing source covers all local ships, so the
// set is the union of the canonical representations, each read from its own
// existing store (no parallel identity management is introduced):
//
//   - fleet configuration: the flagship (fleet.flagship) and the declarative
//     standing ships (fleet.ships). The flagship is not necessarily present in
//     the ship-name registry, and standing ships may not have been spawned yet.
//   - ship-name registry (.starfleet-ai/var/comms/ships/): reserved names,
//     including ships that are reserved but have not heartbeated yet (their
//     mailbox may already contain messages).
//   - comms board status records: ships that are actually running now. This
//     covers ad-hoc ships whose reservation was garbage-collected.
//   - STARFLEET_SHIP_ID, but only when it is explicitly set and is a valid
//     ship token. This keeps the variable relevant for ship processes.
//
// The daemon's user/host fallback identity (e.g. "nekrad@x1", used when
// STARFLEET_SHIP_ID is unset) is never a valid ship token and is filtered out
// by isLocalShipID, so it can never become a mailbox.
//
// The returned slice is deduplicated, sorted and contains only valid ship
// mailbox tokens.
func localShips(root string) ([]string, error) {
	cfg, err := config.Load(root)
	if err != nil {
		return nil, err
	}
	seen := make(map[string]bool)
	var out []string
	add := func(name string) {
		if !isLocalShipID(name) || seen[name] {
			return
		}
		seen[name] = true
		out = append(out, name)
	}

	// Flagship and declarative standing ships from fleet configuration.
	add(shipnames.FlagshipName(root))
	for _, s := range cfg.Fleet.Ships {
		add(s.Name)
	}

	// Active/reserved ship identities from the existing ship registry.
	reg := shipnames.New(root)
	if entries, err := os.ReadDir(reg.ShipsDir); err == nil {
		for _, e := range entries {
			if e.IsDir() || strings.HasPrefix(e.Name(), ".") {
				continue
			}
			add(e.Name())
		}
	}

	// Ships known to the comms board (status records).
	bus, err := comms.New(root)
	if err == nil {
		for _, r := range bus.AllStatusRecords() {
			add(r.Agent)
		}
		// The daemon's own explicitly configured ship identity. When
		// STARFLEET_SHIP_ID is unset, bus.ShipID is the user/host fallback
		// (e.g. "nekrad@x1"), which is not a valid mailbox token and is
		// filtered out by isLocalShipID.
		if bus.ShipIDSet {
			add(bus.ShipID)
		}
	}

	sort.Strings(out)
	return out, nil
}

// uniqueShips returns a deduplicated copy of ships preserving order of first
// occurrence, with invalid/non-mailbox identities filtered out.
func uniqueShips(ships []string) []string {
	seen := make(map[string]bool)
	var out []string
	for _, s := range ships {
		if !isLocalShipID(s) || seen[s] {
			continue
		}
		seen[s] = true
		out = append(out, s)
	}
	return out
}

// isLocalShipID reports whether s is a valid local ship mailbox identity.
// User/host identities (e.g. "nekrad@x1", which contain '@' and are not
// valid wormhole tokens) are rejected.
func isLocalShipID(s string) bool {
	return validToken(s)
}
