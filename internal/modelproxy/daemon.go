// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package modelproxy

import (
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"
)

// IsRunning checks whether something is already listening on the proxy's
// configured address.
func IsRunning(addr string) bool {
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return false
	}
	conn.Close()
	return true
}

// Serve runs the proxy in the foreground (the daemon child path, i.e.
// `model-proxy start`). It blocks until SIGINT/SIGTERM.
func Serve(root string) error {
	cfg, err := Load(root)
	if err != nil {
		return err
	}
	if len(cfg.Providers) == 0 {
		return fmt.Errorf("model-proxy: no providers configured in model-proxy.yaml")
	}
	srv := &http.Server{Addr: cfg.ListenAddr, Handler: New(cfg)}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()
	errCh := make(chan error, 1)
	go func() {
		if err := srv.ListenAndServe(); err != nil && err != http.ErrServerClosed {
			errCh <- err
		}
	}()
	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	}
}

// Daemonize starts the proxy as a background daemon (a detached
// `starfleetctl model-proxy start` child), returning its PID.
func Daemonize(root string) (int, error) {
	cfg, err := Load(root)
	if err != nil {
		return 0, err
	}
	if err := os.MkdirAll(filepath.Dir(cfg.LogFile), 0o755); err != nil {
		return 0, err
	}
	logF, err := os.OpenFile(cfg.LogFile, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return 0, err
	}
	defer logF.Close()

	cmd := exec.Command(os.Args[0], "model-proxy", "start")
	cmd.Dir = root
	cmd.Stdout = logF
	cmd.Stderr = logF
	cmd.Stdin = nil
	// Same PATH story as the web daemon: cron spawns us with a minimal PATH,
	// and the proxy itself may exec things. Env refs that were not set in our
	// environment (e.g. because cron started us) are back-filled from the
	// user's opencode config, which is where the real API keys live.
	cmd.Env = append(daemonEnv(cfg), "PATH="+daemonPath())
	cmd.SysProcAttr = &syscall.SysProcAttr{
		Setpgid: true,
		Pgid:    0,
	}
	if err := cmd.Start(); err != nil {
		return 0, err
	}
	pid := cmd.Process.Pid
	cmd.Process.Release()
	return pid, nil
}

// daemonEnv returns the environment for the daemon child: the caller's env,
// plus any model-proxy env refs (e.g. NIM_API_KEY) that are missing but can
// be resolved from the user's opencode config.
func daemonEnv(cfg *Config) []string {
	env := os.Environ()
	have := map[string]bool{}
	for _, e := range env {
		if eq := strings.IndexByte(e, '='); eq > 0 {
			have[e[:eq]] = true
		}
	}
	for _, ref := range cfg.EnvRefs {
		if have[ref] {
			continue
		}
		if v, ok := lookupUserConfigKey(ref); ok {
			env = append(env, ref+"="+v)
		}
	}
	return env
}

// lookupUserConfigKey resolves an env var reference by scanning the user's
// opencode config for an apiKey of the form {env:<want>} and returning the
// current process value of that variable. Only {env:} references are
// considered: a literal key in the config cannot be attributed to a specific
// env var name. This mirrors the web daemon's key back-fill for cron-spawned
// daemons (a daemon started from a real shell already has the key in its env).
func lookupUserConfigKey(want string) (string, bool) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", false
	}
	data, err := os.ReadFile(filepath.Join(home, ".config", "opencode", "opencode.json"))
	if err != nil {
		return "", false
	}
	var cfg struct {
		Provider map[string]struct {
			Options struct {
				APIKey string `json:"apiKey"`
			} `json:"options"`
		} `json:"provider"`
	}
	if err := json.Unmarshal(data, &cfg); err != nil {
		return "", false
	}
	for _, prov := range cfg.Provider {
		key := prov.Options.APIKey
		if strings.HasPrefix(key, "{env:") && strings.HasSuffix(key, "}") && key[5:len(key)-1] == want {
			if v := os.Getenv(want); v != "" {
				return v, true
			}
		}
	}
	return "", false
}

// Autostart ensures the proxy daemon is running, starting it if not. Designed
// for cron invocation.
func Autostart(root string) (bool, error) {
	cfg, err := Load(root)
	if err != nil {
		return false, err
	}
	if IsRunning(cfg.ListenAddr) {
		return true, nil
	}
	if pid, err := ReadPID(cfg.PIDFile); err == nil {
		if !IsPIDAlive(pid) {
			_ = os.Remove(cfg.PIDFile)
		}
	}
	pid, err := Daemonize(root)
	if err != nil {
		return false, err
	}
	if err := os.WriteFile(cfg.PIDFile, []byte(strconv.Itoa(pid)), 0o644); err != nil {
		return false, err
	}
	time.Sleep(500 * time.Millisecond)
	if IsRunning(cfg.ListenAddr) {
		return true, nil
	}
	return false, fmt.Errorf("model-proxy: daemon started but not listening on %s", cfg.ListenAddr)
}

// Stop stops the proxy daemon (PID file first, then /proc fallback).
func Stop(root string) error {
	cfg, err := Load(root)
	if err != nil {
		return err
	}
	if pid, err := ReadPID(cfg.PIDFile); err == nil && pid > 0 {
		if process, err := os.FindProcess(pid); err == nil {
			_ = process.Kill()
		}
	}
	if err := os.Remove(cfg.PIDFile); err != nil && !os.IsNotExist(err) {
		return err
	}
	_ = killProcOnAddr(cfg.ListenAddr)
	return nil
}

// Restart replaces a running proxy daemon.
func Restart(root string) error {
	if err := Stop(root); err != nil {
		return err
	}
	time.Sleep(500 * time.Millisecond)
	_, err := Autostart(root)
	return err
}

// Status reports whether the proxy is running and where.
func Status(root string) error {
	cfg, err := Load(root)
	if err != nil {
		return err
	}
	pid := 0
	if p, err := ReadPID(cfg.PIDFile); err == nil {
		pid = p
	}
	if IsRunning(cfg.ListenAddr) {
		fmt.Printf("model-proxy: running on %s (pid %d)\n", cfg.ListenAddr, pid)
	} else {
		fmt.Printf("model-proxy: not running (pid file %s, listen %s)\n", cfg.PIDFile, cfg.ListenAddr)
	}
	return nil
}

// ReadPID / IsPIDAlive / daemonPath — small local copies of the web daemon's
// helpers, kept independent so the two daemons don't share lifecycle state.

func ReadPID(pidFile string) (int, error) {
	data, err := os.ReadFile(pidFile)
	if err != nil {
		return 0, err
	}
	return strconv.Atoi(strings.TrimSpace(string(data)))
}

func IsPIDAlive(pid int) bool {
	if pid <= 0 {
		return false
	}
	process, err := os.FindProcess(pid)
	if err != nil {
		return false
	}
	return process.Signal(os.Signal(nil)) == nil
}

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

// proxyStartProcs finds all `starfleetctl model-proxy start` processes via
// /proc (PATH-independent, unlike pgrep of a cron-spawned daemon).
func proxyStartProcs() []int {
	var pids []int
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
		for i := 0; i < len(args)-1; i++ {
			if args[i] == "model-proxy" && args[i+1] == "start" {
				pids = append(pids, pid)
				break
			}
		}
	}
	return pids
}

// killProcOnAddr kills a running proxy daemon by matching its listen address.
// The daemon's cmdline does not carry the address, so we match the PID file
// first and only fall back to /proc when the PID file is stale/missing.
func killProcOnAddr(addr string) error {
	for _, pid := range proxyStartProcs() {
		if process, err := os.FindProcess(pid); err == nil {
			_ = process.Kill()
		}
	}
	return nil
}
