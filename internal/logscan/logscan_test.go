// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
package logscan

import (
	"os"
	"path/filepath"
	"testing"
)

func TestScanExtractsFindings(t *testing.T) {
	dir := t.TempDir()
	log := filepath.Join(dir, "TestShip.log")
	content := `2026/07/17 18:26:38 X11 Error: BadMatch (code=8), seq=25268, opcode=42.0, id=41943042
2026/07/17 18:26:39 X11 Error: BadMatch (code=8), seq=25269, opcode=42.0, id=41943042
2026/07/17 18:27:39 term: recovered from event handler panic: assignment to entry in nil map
2026/07/17 18:28:39 X11 connection closed: X11ConnError: readLoop() EOF
`
	if err := os.WriteFile(log, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}

	findings := Scan([]string{log})
	if len(findings) == 0 {
		t.Fatal("expected at least one finding")
	}

	byKey := map[string]Finding{}
	for _, f := range findings {
		byKey[f.Key()] = f
	}

	x11, ok := byKey["x11-error|x11:BadMatch:code8:op42.0"]
	if !ok {
		t.Fatalf("X11 BadMatch finding missing; got %v", keys(byKey))
	}
	if x11.Count != 2 {
		t.Errorf("expected X11 count 2, got %d", x11.Count)
	}
	if x11.Severity != 3 {
		t.Errorf("expected X11 severity 3, got %d", x11.Severity)
	}
	if x11.Component != "go-x11proto" {
		t.Errorf("expected component go-x11proto, got %s", x11.Component)
	}

	panicF, ok := byKey["panic|panic:assignment to entry in nil map"]
	if !ok {
		t.Fatalf("nil-map panic finding missing; got %v", keys(byKey))
	}
	if panicF.Count != 1 {
		t.Errorf("expected panic count 1, got %d", panicF.Count)
	}

	conn, ok := byKey["conn-closed|x11:connection-closed"]
	if !ok {
		t.Fatalf("conn-closed finding missing; got %v", keys(byKey))
	}
	if conn.Count != 1 {
		t.Errorf("expected conn-closed count 1, got %d", conn.Count)
	}
}

func keys(m map[string]Finding) []string {
	out := make([]string, 0, len(m))
	for k := range m {
		out = append(out, k)
	}
	return out
}
