// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package session

import (
	"os"
	"strings"
	"testing"
)

// TestRunClientEnvLaunchType ensures the `run`-launched client environment
// carries STARFLEET_LAUNCH_TYPE=terminal — previously only the heartbeat and
// generated config said "terminal" while the process env lacked the variable,
// letting ships inherit a stale/default "background" label.
func TestRunClientEnvLaunchType(t *testing.T) {
	env := runClientEnv("TestShip", "ship", t.TempDir())
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
	env := runClientEnv("TestShip", "ship", t.TempDir())
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
	for _, kv := range runClientEnv("Flagship", "flagship", t.TempDir()) {
		if strings.HasPrefix(kv, "STARFLEET_TARGET=") {
			t.Errorf("STARFLEET_TARGET should not be set for role=flagship: %s", kv)
		}
	}
	_ = os.Environ() // keep import
}