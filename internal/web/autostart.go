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
	defaultLogFile    = ".starfleet-ai/logs/web.log"
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

	cmd := exec.Command(os.Args[0], "web", "--addr", addr)
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

	// Check if already running on the configured address
	if IsWebServerRunning(addr) {
		return true, nil
	}

	// Check PID file - if process is alive but not on our port, clean up
	if pid, err := ReadPID(ac.PIDFile); err == nil {
		if IsPIDAlive(pid) {
			// Process exists but not on our port - maybe different config
			// Leave it alone for safety
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

// RunAutostart is the CLI entry point for `starfleetctl web autostart`.
func RunAutostart(root string, args []string) int {
	if len(args) > 0 {
		switch args[0] {
		case "stop":
			if err := Stop(root); err != nil {
				fmt.Fprintln(os.Stderr, "web autostart stop:", err)
				return 1
			}
			fmt.Println("web server stopped")
			return 0
		case "restart":
			if err := Restart(root); err != nil {
				fmt.Fprintln(os.Stderr, "web autostart restart:", err)
				return 1
			}
			fmt.Println("web server restarted")
			return 0
		}
	}

	ok, err := Autostart(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "web autostart:", err)
		return 1
	}
	if ok {
		fmt.Println("web server running")
	} else {
		fmt.Println("web server start failed")
	}
	return 0
}