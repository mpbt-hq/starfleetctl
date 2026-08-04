// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package web provides the autostart/daemon functionality for the web server.
// Designed to be called from cron every minute to ensure the web console is running.
package web

import (
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/metux/starfleetctl/internal/config"
)

const (
	defaultListenAddr = "0.0.0.0:8080"
	defaultPIDFile    = ".starfleet-ai/var/web.pid"
	defaultLogFile    = ".starfleet-ai/var/log/web.log"
)

// autostartConfig holds resolved paths for autostart.
type autostartConfig struct {
	PIDFile string
	LogFile string
}

// DefaultAutostartConfig returns default paths resolved to absolute.
func DefaultAutostartConfig(root string) autostartConfig {
	return autostartConfig{
		PIDFile: filepath.Join(root, defaultPIDFile),
		LogFile: filepath.Join(root, defaultLogFile),
	}
}

// IsWebServerRunning checks if a server is listening on the given address.
func IsWebServerRunning(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// IsPIDAlive checks if a process with the given PID exists.
func IsPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	// On Unix, FindProcess always succeeds, so send signal 0 to check
	err = process.Signal(os.Signal(nil))
	return err == nil
}

// ReadPID reads the PID from the PID file.
func ReadPID(pidFile string) (int, error) {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return 0, err
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(data)))
	if err != nil {
		return 0, err
	}
	return pid, nil
}

// WritePID writes the current process PID to the PID file.
func WritePID(pidFile string) error {
	if err := os.MkdirAll(filepath.Dir(pidFile), 0o755); err != nil {
		return err
	}
	return os.WriteFile(pidFile, []byte(strconv.Itoa(os.Getpid())), 0o644)
}

// RemovePID removes the PID file.
func RemovePID(pidFile string) error {
	return os.Remove(pidFile)
}

// EnsureLogDir creates the log directory.
func EnsureLogDir(logFile string) error {
	return os.MkdirAll(filepath.Dir(logFile), 0o755)
}

// Daemonize starts the web server as a background daemon.
// Returns the child PID.
func Daemonize(root, addr, logFile string) (int, error) {
	logF, err := os.OpenFile(logFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, err
	}

	cmd := exec.Command(os.Args[0], "web", "start", "--addr", addr)
	cmd.Dir = root
	cmd.Stdout = logF
	cmd.Stderr = logF
	cmd.Stdin = nil
	// The daemon is often spawned by cron with a minimal PATH
	// (PATH=/usr/bin:/bin), which its children then inherit. Expand the PATH
	// so exec'd helpers (ss, git, ...) are found regardless of the caller's
	// environment. Pass through the full parent environment.
	cmd.Env = append(os.Environ(), "PATH="+daemonPath())
	// Detach from parent
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
		Pgid:    0,
	}

	if err := cmd.Start(); err != nil {
		logF.Close()
		return 0, err
	}
	pid := cmd.Process.Pid
	logF.Close()
	cmd.Process.Release()
	return pid, nil
}

// Autostart checks if web server is running, starts it as daemon if not.
// Returns true if server is running (either was already running or was started).
// Designed to be called from cron every minute.
func Autostart(root string) (bool, error) {
	cfg, err := config.Load(root)
	if err != nil {
		return false, err
	}
	addr := cfg.Web.ListenAddr
	ac := DefaultAutostartConfig(root)

	// Clean up any starfleetctl web processes on WRONG ports (always run)
	if err := cleanupWrongPortProcesses(addr); err != nil {
		return false, err
	}

	// Check if already running on the configured address
	if IsWebServerRunning(addr) {
		return true, nil
	}

	// Check PID file - if process is alive but not on our port, clean up
	if pid, err := ReadPID(ac.PIDFile); err == nil {
		if IsPIDAlive(pid) {
			// Process exists but not on our port - cleanupWrongPortProcesses should have handled this
		} else {
			// Stale PID file
			RemovePID(ac.PIDFile)
		}
	}

	// Ensure log dir exists
	if err := EnsureLogDir(ac.LogFile); err != nil {
		return false, err
	}

	// Start daemon
	pid, err := Daemonize(root, addr, ac.LogFile)
	if err != nil {
		return false, fmt.Errorf("daemonize: %w", err)
	}

	// Write daemon PID to PID file
	if err := os.WriteFile(ac.PIDFile, []byte(strconv.Itoa(pid)), 0o644); err != nil {
		return false, err
	}

	// Give it a moment to start listening
	time.Sleep(500 * time.Millisecond)

	// Verify it's now running
	if IsWebServerRunning(addr) {
		return true, nil
	}

	return false, fmt.Errorf("server started but not listening on %s", addr)
}

// cleanupWrongPortProcesses finds and kills any starfleetctl web processes
// that are listening on ports other than the configured address.
func cleanupWrongPortProcesses(expectedAddr string) error {
	// Parse expected port
	_, expectedPort, err := net.SplitHostPort(expectedAddr)
	if err != nil {
		return nil // not our problem
	}

	for _, p := range webStartProcs() {
		if p.Port != "" && p.Port != expectedPort {
			// Kill process on wrong port
			process, err := os.FindProcess(p.PID)
			if err == nil {
				process.Kill()
			}
		}
	}
	return nil
}

// webProcInfo holds info about a starfleetctl web process
type webProcInfo struct {
	PID  int
	Port string
}

// webStartProcs finds all starfleetctl "web start" processes (the daemon and
// any foreground instance) via /proc, along with the port from their
// "--addr HOST:PORT" argument. Pure /proc scan — no external tools, so it
// works regardless of the daemon's PATH.
func webStartProcs() []webProcInfo {
	var procs []webProcInfo
	entries, err := os.ReadDir("/proc")
	if err != nil {
		return nil
	}
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		pid, err := strconv.Atoi(e.Name())
		if err != nil {
			continue
		}
		cmdline, err := os.ReadFile(filepath.Join("/proc", e.Name(), "cmdline"))
		if err != nil {
			continue
		}
		args := strings.Split(strings.TrimSuffix(string(cmdline), "\x00"), "\x00")
		if !webStartArgs(args) {
			continue
		}
		procs = append(procs, webProcInfo{PID: pid, Port: webAddrPort(args)})
	}
	return procs
}

// webStartArgs reports whether the /proc cmdline args belong to a
// "starfleetctl web start" invocation.
func webStartArgs(args []string) bool {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "web" && args[i+1] == "start" {
			return true
		}
	}
	return false
}

// webAddrPort extracts the port from a "--addr HOST:PORT" argument pair.
func webAddrPort(args []string) string {
	for i := 0; i < len(args)-1; i++ {
		if args[i] == "--addr" {
			if _, port, err := net.SplitHostPort(args[i+1]); err == nil {
				return port
			}
		}
	}
	return ""
}

// daemonPath returns the PATH the web daemon should run with: the caller's
// PATH first, then the standard bin/sbin dirs (and the user's ~/.local/bin and
// ~/bin). This makes the daemon independent of a minimal environment passed
// down by cron.
func daemonPath() string {
	extra := []string{"/usr/local/sbin", "/usr/sbin", "/usr/local/bin"}
	if home, err := os.UserHomeDir(); err == nil {
		extra = append(extra, filepath.Join(home, ".local", "bin"), filepath.Join(home, "bin"))
	}
	seen := map[string]bool{}
	var parts []string
	for _, d := range append(filepath.SplitList(os.Getenv("PATH")), extra...) {
		if d == "" || seen[d] {
			continue
		}
		seen[d] = true
		parts = append(parts, d)
	}
	return strings.Join(parts, string(os.PathListSeparator))
}

// killWebProcOnPort kills any starfleetctl web process bound to the given
// listen address (e.g. "0.0.0.0:8080").
func killWebProcOnPort(addr string) error {
	_, expectedPort, err := net.SplitHostPort(addr)
	if err != nil {
		return nil
	}
	for _, p := range webStartProcs() {
		if p.Port == expectedPort {
			if process, err := os.FindProcess(p.PID); err == nil {
				process.Kill()
			}
		}
	}
	return nil
}

// Stop stops the web server daemon.
func Stop(root string) error {
	ac := DefaultAutostartConfig(root)
	pid, err := ReadPID(ac.PIDFile)
	if err != nil {
		return err
	}
	if pid > 0 {
		process, err := os.FindProcess(pid)
		if err == nil {
			process.Kill()
		}
	}
	return RemovePID(ac.PIDFile)
}

// Restart stops the web server if running, then starts it again.
func Restart(root string) error {
	cfg, err := config.Load(root)
	if err != nil {
		return err
	}
	addr := cfg.Web.ListenAddr

	Stop(root)
	// Wait for the port to be freed.
	time.Sleep(500 * time.Millisecond)

	// If the port is still busy, the PID file was missing or stale and Stop
	// could not kill the daemon. Kill the starfleetctl web process bound to
	// the configured address so Autostart actually replaces it instead of
	// seeing the port as busy and returning early.
	if IsWebServerRunning(addr) {
		if err := killWebProcOnPort(addr); err != nil {
			return err
		}
		time.Sleep(300 * time.Millisecond)
	}

	_, err = Autostart(root)
	return err
}
