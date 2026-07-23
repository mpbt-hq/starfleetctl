// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package web provides the autostart/daemon functionality for the web server.
// Designed to be called from cron every minute to ensure the web console is running.
package web

import (
	"bufio"
	"bytes"
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

	// Find all starfleetctl web processes and their listening ports
	procs, err := findStarlfleetWebProcs()
	if err != nil {
		return err
	}

	for _, p := range procs {
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

// findStarlfleetWebProcs finds all starfleetctl web processes and their listening ports.
func findStarlfleetWebProcs() ([]webProcInfo, error) {
	var procs []webProcInfo

	// Use ss to find listening processes
	cmd := exec.Command("ss", "-ltnp")
	out, err := cmd.Output()
	if err != nil {
		return nil, err
	}

	scanner := bufio.NewScanner(bytes.NewReader(out))
	for scanner.Scan() {
		line := scanner.Text()
		// Look for starfleetctl processes
		if strings.Contains(line, "starfleetctl") && strings.Contains(line, "web") {
			// Parse: LISTEN 0 4096 *:8090 *:* users:(("starfleetctl",pid=1234,fd=3))
			port := extractPort(line)
			pid := extractPID(line)
			if port != "" && pid > 0 {
				procs = append(procs, webProcInfo{PID: pid, Port: port})
			}
		}
	}
	return procs, scanner.Err()
}

func extractPort(line string) string {
	// Find *:PORT or IP:PORT pattern
	fields := strings.Fields(line)
	for _, f := range fields {
		if strings.Contains(f, ":") && !strings.HasPrefix(f, "pid=") && !strings.HasPrefix(f, "fd=") {
			parts := strings.Split(f, ":")
			if len(parts) == 2 {
				return parts[1]
			}
		}
	}
	return ""
}

func extractPID(line string) int {
	// Find pid=NUMBER
	idx := strings.Index(line, "pid=")
	if idx < 0 {
		return 0
	}
	idx += 4
	end := idx
	for end < len(line) && line[end] >= '0' && line[end] <= '9' {
		end++
	}
	pid, _ := strconv.Atoi(line[idx:end])
	return pid
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
	Stop(root)
	// Wait for the port to be freed.
	time.Sleep(500 * time.Millisecond)
	_, err := Autostart(root)
	return err
}
