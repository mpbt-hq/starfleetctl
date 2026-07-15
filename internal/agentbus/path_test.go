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
	_, err := b.mfile("../../../../etc/passwd")
	if err == nil {
		t.Fatal("mfile accepted a traversal id")
	}

	// valid id stays inside MsgDir and ends in .tsv
	p, err := b.mfile("m0042")
	if err != nil {
		t.Fatalf("mfile rejected a valid id: %v", err)
	}
	if !strings.HasPrefix(p, b.MsgDir+string(filepath.Separator)) {
		t.Errorf("mfile path %q not under MsgDir %q", p, b.MsgDir)
	}
	if !strings.HasSuffix(p, ".tsv") {
		t.Errorf("mfile path %q does not end in .tsv", p)
	}
}

func TestAckmarkRejectsBadAgent(t *testing.T) {
	b := newTestBus(t, "TestShip")

	if _, err := b.ackmark("m0001", "../../x"); err == nil {
		t.Fatal("ackmark accepted a traversal agent id")
	}
	if _, err := b.ackmark("../../x", "ship"); err == nil {
		t.Fatal("ackmark accepted a traversal message id")
	}
	if _, err := b.ackmark("m0001", "ship1"); err != nil {
		t.Fatalf("ackmark rejected a valid pair: %v", err)
	}
}
