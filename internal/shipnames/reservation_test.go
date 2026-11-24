// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package shipnames

import (
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestReserveRejectsLiveDuplicate(t *testing.T) {
	reg := New(t.TempDir())
	if err := reg.Reserve("Voyager"); err != nil { t.Fatalf("first reserve: %v", err) }
	if err := reg.Reserve("Voyager"); err == nil || !strings.Contains(err.Error(), "already in use") {
		t.Fatalf("second reserve error = %v, want already-in-use conflict", err)
	}
}

func TestReserveReclaimsDeadReservation(t *testing.T) {
	reg := New(t.TempDir())
	if err := os.MkdirAll(reg.ShipsDir, 0o755); err != nil { t.Fatal(err) }
	path := filepath.Join(reg.ShipsDir, "Voyager")
	if err := os.WriteFile(path, []byte("999999999:1:\n"), 0o644); err != nil { t.Fatal(err) }
	if err := reg.Reserve("Voyager"); err != nil { t.Fatalf("reserve dead name: %v", err) }
	data, err := os.ReadFile(path)
	if err != nil { t.Fatal(err) }
	pid, _, _ := parseReservation(string(data))
	if pid != os.Getpid() { t.Fatalf("reservation PID = %d, want %d", pid, os.Getpid()) }
}

func TestConcurrentReserveOnlyOneWinner(t *testing.T) {
	reg := New(t.TempDir())
	const contenders = 8
	var wg sync.WaitGroup
	wg.Add(contenders)
	results := make(chan error, contenders)
	for i := 0; i < contenders; i++ {
		go func() { defer wg.Done(); results <- reg.Reserve("Defiant") }()
	}
	wg.Wait()
	close(results)
	successes, conflicts := 0, 0
	for err := range results {
		if err == nil { successes++ } else if strings.Contains(err.Error(), "already in use") { conflicts++ } else { t.Errorf("unexpected reserve error: %v", err) }
	}
	if successes != 1 || conflicts != contenders-1 { t.Fatalf("successes=%d conflicts=%d, want 1 and %d", successes, conflicts, contenders-1) }
}
