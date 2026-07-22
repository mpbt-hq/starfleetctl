// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package agentbus

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestMfileRejectsTraversal(t *testing.T) {
	b := newTestBus(t, "TestShip")

	// malicious id must not produce a path outside MsgDir
	_, err := b.mfile("../../../../etc/passwd", "test")
	if err == nil {
		t.Fatal("mfile accepted a traversal id")
	}

	// valid id stays inside MsgDir and ends in .json
	p, err := b.mfile("m0042", "test")
	if err != nil {
		t.Fatalf("mfile rejected a valid id: %v", err)
	}
	if !strings.HasPrefix(p, b.MsgDir+string(filepath.Separator)) {
		t.Errorf("mfile path %q not under MsgDir %q", p, b.MsgDir)
	}
	if !strings.HasSuffix(p, ".json") {
		t.Errorf("mfile path %q does not end in .json", p)
	}
}

func TestMfileSeenRejectsTraversal(t *testing.T) {
	b := newTestBus(t, "TestShip")

	// fsafe sanitizes target traversal characters but fsutil.Safe rejects traversal IDs
	if _, err := b.mfileSeen("test", "../../etc/passwd"); err == nil {
		t.Fatal("mfileSeen accepted a traversal id")
	}

	p2, err := b.mfileSeen("../../x", "m0001")
	if err != nil {
		t.Fatalf("mfileSeen returned error: %v", err)
	}
	if !strings.HasPrefix(p2, b.MsgDir+string(filepath.Separator)) {
		t.Errorf("mfileSeen path %q escapes MsgDir %q", p2, b.MsgDir)
	}

	p3, err := b.mfileSeen("test", "m0001")
	if err != nil {
		t.Fatalf("mfileSeen rejected a valid pair: %v", err)
	}
	if !strings.Contains(p3, filepath.Join("test", "seen")) {
		t.Errorf("mfileSeen path %q missing target/seen", p3)
	}
}
