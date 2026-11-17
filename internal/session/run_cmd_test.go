// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package session

import (
	"os"
	"strings"
	"testing"

	"github.com/metux/starfleetctl/internal/shipnames"
)

// TestRunClientEnvLaunchType ensures the `run`-launched client environment
// carries STARFLEET_LAUNCH_TYPE=terminal — previously only the heartbeat and
// generated config said "terminal" while the process env lacked the variable,
// letting ships inherit a stale/default "background" label.
func TestRunClientEnvLaunchType(t *testing.T) {
	env := runClientEnv("TestShip", "ship", t.TempDir(), "opencode")
	launchSeen := ""
	for _, kv := range env {
		if strings.HasPrefix(kv, "STARFLEET_LAUNCH_TYPE=") {
			launchSeen = strings.TrimPrefix(kv, "STARFLEET_LAUNCH_TYPE=")
		}
	}
	if launchSeen != "terminal" {
		t.Errorf("STARFLEET_LAUNCH_TYPE = %q, want %q", launchSeen, "terminal")
	}
}

// TestRunClientEnvShipTarget ensures ship-role runs get the flagship target var.
func TestRunClientEnvShipTarget(t *testing.T) {
	env := runClientEnv("TestShip", "ship", t.TempDir(), "opencode")
	targetSeen := ""
	for _, kv := range env {
		if strings.HasPrefix(kv, "STARFLEET_TARGET=") {
			targetSeen = strings.TrimPrefix(kv, "STARFLEET_TARGET=")
		}
	}
	if targetSeen == "" {
		t.Error("STARFLEET_TARGET missing for ship role")
	}
	// Non-ship roles must not leak a target.
	for _, kv := range runClientEnv("Flagship", "flagship", t.TempDir(), "opencode") {
		if strings.HasPrefix(kv, "STARFLEET_TARGET=") {
			t.Errorf("STARFLEET_TARGET should not be set for role=flagship: %s", kv)
		}
	}
	_ = os.Environ() // keep import
}

// TestRunClientEnvDoesNotInheritAmbientIdentity ensures the ambient fleet
// identity of the parent process (a ship shell exports STARFLEET_TARGET and
// STARFLEET_SHIP_ID) is not inherited by the launched client. Without the
// filter in runClientEnv, a flagship run launched from a ship shell received
// the parent ship's STARFLEET_TARGET — which also made this package's tests
// fail in every ship shell while passing on a bare CI runner.
func TestRunClientEnvDoesNotInheritAmbientIdentity(t *testing.T) {
	t.Setenv("STARFLEET_TARGET", "Enterprise")
	t.Setenv("STARFLEET_SHIP_ID", "ParentShip")
	t.Setenv("STARFLEET_ROLE", "ship")
	t.Setenv("STARFLEET_LAUNCH_TYPE", "background")

	root := t.TempDir()

	for _, kv := range runClientEnv("Flagship", "flagship", root, "opencode") {
		if strings.HasPrefix(kv, "STARFLEET_TARGET=") {
			t.Errorf("ambient STARFLEET_TARGET leaked into flagship env: %s", kv)
		}
	}

	env := runClientEnv("TestShip", "ship", root, "opencode")
	seen := map[string]string{}
	for _, kv := range env {
		k, v, ok := strings.Cut(kv, "=")
		if !ok || !strings.HasPrefix(k, "STARFLEET_") {
			continue
		}
		if _, dup := seen[k]; dup {
			t.Errorf("duplicate env key %q in client env", k)
		}
		seen[k] = v
	}
	want := map[string]string{
		"STARFLEET_TARGET":      shipnames.FlagshipName(root),
		"STARFLEET_SHIP_ID":     "TestShip",
		"STARFLEET_ROLE":        "ship",
		"STARFLEET_LAUNCH_TYPE": "terminal",
	}
	for k, w := range want {
		if seen[k] != w {
			t.Errorf("%s = %q, want %q", k, seen[k], w)
		}
	}
}
