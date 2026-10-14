// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package modelproxy

import (
	"os"
	"testing"
)

// TestIsPIDAliveSelf guards the signal-0 liveness probe. Previously it used
// process.Signal(os.Signal(nil)), which Process.Signal rejects with
// "os: unsupported signal type" -> IsPIDAlive always returned false, so a
// living proxy PID was reported dead.
func TestIsPIDAliveSelf(t *testing.T) {
	if !IsPIDAlive(os.Getpid()) {
		t.Fatal("IsPIDAlive(self) = false — signal-0 probe broken (must be true for a living PID)")
	}
	if IsPIDAlive(-1) {
		t.Fatal("IsPIDAlive(-1) = true — invalid PID must be false")
	}
}
