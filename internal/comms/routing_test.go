// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
package comms

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeCommsConfig(t *testing.T, root, content string) {
	t.Helper()
	dir := filepath.Join(root, ".starfleet-ai", "conf")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "comms.yaml"), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func newRoutingTestBus(t *testing.T, root string) *Bus {
	t.Helper()
	t.Setenv("STARFLEET_BUS_DIR", filepath.Join(root, "bus"))
	t.Setenv("STARFLEET_SHIP_ID", "alpha")
	b, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

func TestRoutingLocalRecipients(t *testing.T) {
	root := t.TempDir()
	writeCommsConfig(t, root, `
comms:
  routing:
    local_domain: local
`)
	b := newRoutingTestBus(t, root)

	for _, target := range []string{"beta", "beta@local", "beta@LOCAL"} {
		id, err := b.Tell(target, "hello", "")
		if err != nil {
			t.Fatalf("%s must be local: %v", target, err)
		}
		if id == "" {
			t.Fatalf("%s: empty local id", target)
		}
	}
	if _, err := os.Stat(filepath.Join(b.MsgDir, "beta", "unseen")); err != nil {
		t.Fatalf("local recipient inbox missing: %v", err)
	}
}

func TestRoutingWithoutTableKeepsLocalIdentity(t *testing.T) {
	root := t.TempDir()
	b := newRoutingTestBus(t, root)

	id, err := b.Tell("user@host", "hello", "")
	if err != nil {
		t.Fatalf("no routing table must keep legacy local identity: %v", err)
	}
	if id == "" {
		t.Fatal("empty id")
	}
	if _, err := os.Stat(filepath.Join(b.MsgDir, fsafe("user@host"), "unseen")); err != nil {
		t.Fatalf("legacy local inbox missing: %v", err)
	}
}

func TestRoutingRemoteThroughTransport(t *testing.T) {
	root := t.TempDir()
	writeCommsConfig(t, root, `
comms:
  routing:
    local_domain: local
    domains:
      - domain: peer.example
        transport: fake-route
        repository: relay
`)
	b := newRoutingTestBus(t, root)

	var got OutboundMessage
	var gotRoute TransportRoute
	RegisterRemoteTransport("fake-route", func(root string, route TransportRoute, msg OutboundMessage) (string, error) {
		gotRoute = route
		got = msg
		return "ext-123", nil
	})

	id, err := b.Tell("bob@peer.example", "remote hello", "corr-1")
	if err != nil {
		t.Fatalf("remote send: %v", err)
	}
	if id != "ext-123" {
		t.Fatalf("returned id = %q, want transport id", id)
	}
	if gotRoute.Transport != "fake-route" || gotRoute.Repository != "relay" || gotRoute.Domain != "peer.example" {
		t.Fatalf("unexpected route: %+v", gotRoute)
	}
	if got.Target != "bob" || got.From != "alpha" || got.Text != "remote hello" || got.ReplyTo != "corr-1" {
		t.Fatalf("unexpected outbound message: %+v", got)
	}
	if _, err := os.Stat(filepath.Join(b.MsgDir, "bob", "unseen")); !os.IsNotExist(err) {
		t.Fatalf("remote message must not be written into the local inbox (err=%v)", err)
	}
}

func TestRoutingRemoteUnknownDomain(t *testing.T) {
	root := t.TempDir()
	writeCommsConfig(t, root, `
comms:
  routing:
    domains:
      - domain: peer.example
        transport: fake-route
        repository: relay
`)
	b := newRoutingTestBus(t, root)

	_, err := b.Tell("bob@other.example", "hello", "")
	if err == nil || !strings.Contains(err.Error(), "no route") {
		t.Fatalf("unknown domain must fail cleanly, got %v", err)
	}
}

func TestRoutingRemoteUnavailableTransport(t *testing.T) {
	root := t.TempDir()
	writeCommsConfig(t, root, `
comms:
  routing:
    domains:
      - domain: peer.example
        transport: not-registered
        repository: relay
`)
	b := newRoutingTestBus(t, root)

	_, err := b.Tell("bob@peer.example", "hello", "")
	if err == nil || !strings.Contains(err.Error(), "not available") {
		t.Fatalf("unavailable transport must fail cleanly, got %v", err)
	}
}

func TestRoutingRemoteRejectsCommand(t *testing.T) {
	root := t.TempDir()
	writeCommsConfig(t, root, `
comms:
  routing:
    domains:
      - domain: peer.example
        transport: fake-route
        repository: relay
`)
	b := newRoutingTestBus(t, root)
	RegisterRemoteTransport("fake-route", func(root string, route TransportRoute, msg OutboundMessage) (string, error) {
		return "ext", nil
	})

	if _, err := b.Command("bob@peer.example", "model", "x"); err == nil {
		t.Fatal("remote command must be rejected")
	}
}

func TestRoutingTableValidation(t *testing.T) {
	cases := []string{
		"comms:\n  routing:\n    domains:\n      - domain: ''\n        transport: wormhole\n        repository: r\n",
		"comms:\n  routing:\n    domains:\n      - domain: peer\n        transport: ''\n        repository: r\n",
		"comms:\n  routing:\n    domains:\n      - domain: peer\n        transport: wormhole\n",
		"comms:\n  routing:\n    local_domain: peer\n    domains:\n      - domain: peer\n        transport: wormhole\n        repository: r\n",
	}
	for _, cfg := range cases {
		root := t.TempDir()
		writeCommsConfig(t, root, cfg)
		if _, err := loadRoutingTable(root); err == nil {
			t.Fatalf("expected routing table validation error for:\n%s", cfg)
		}
	}
}
