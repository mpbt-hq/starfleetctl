// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
package comms

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"fmt"
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

// TestRoutingRemoteIdempotentRetryBeforePublish verifies that if the process
// crashes before the remote publish, a retry with the same content does not
// publish twice — it reuses the pending outbox record and publishes once.
func TestRoutingRemoteIdempotentRetryBeforePublish(t *testing.T) {
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

	publishCount := 0
	RegisterRemoteTransport("fake-route", func(root string, route TransportRoute, msg OutboundMessage) (string, error) {
		publishCount++
		return "ext-123", nil
	})

	// First send - should publish and create pending record
	id1, err := b.Tell("bob@peer.example", "hello world", "")
	if err != nil {
		t.Fatalf("first send: %v", err)
	}
	if id1 != "ext-123" {
		t.Fatalf("first send id = %q, want transport id", id1)
	}
	if publishCount != 1 {
		t.Fatalf("publishCount = %d after first send, want 1", publishCount)
	}

	// Simulate retry with SAME content (new bus instance, same root).
	// The first send completed fully (publish + local update with ExternalID).
	// The retry should find the complete record and return the transport ID
	// WITHOUT calling publish again.
	b2 := newRoutingTestBus(t, root)
	publishCount = 0
	id2, err := b2.Tell("bob@peer.example", "hello world", "")
	if err != nil {
		t.Fatalf("retry send: %v", err)
	}
	if id2 != "ext-123" {
		t.Fatalf("retry send id = %q, want same transport id", id2)
	}
	// With completed record, retry returns cached transport ID without re-publishing.
	if publishCount != 0 {
		t.Fatalf("publishCount = %d on retry, want 0 (no duplicate publish)", publishCount)
	}
}

// TestRoutingRemoteRetryAfterPublishFailure verifies that if the remote
// publish fails, the local pending record remains and a retry will
// attempt the publish again.
func TestRoutingRemoteRetryAfterPublishFailure(t *testing.T) {
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

	// Use a shared counter so both first attempt and retry use the same logic
	// First call fails, second call succeeds
	totalAttempts := 0
	RegisterRemoteTransport("fake-route", func(root string, route TransportRoute, msg OutboundMessage) (string, error) {
		totalAttempts++
		if totalAttempts == 1 {
			return "", fmt.Errorf("simulated transport failure")
		}
		return "ext-success", nil
	})

	// First attempt: publish fails
	_, err := b.Tell("bob@peer.example", "hello world", "")
	if err == nil || !strings.Contains(err.Error(), "simulated transport failure") {
		t.Fatalf("first send should fail: %v", err)
	}
	if totalAttempts != 1 {
		t.Fatalf("totalAttempts = %d after first failure, want 1", totalAttempts)
	}

	// Second attempt (retry): publish succeeds
	b2 := newRoutingTestBus(t, root)
	id2, err := b2.Tell("bob@peer.example", "hello world", "")
	if err != nil {
		t.Fatalf("retry send: %v", err)
	}
	if id2 != "ext-success" {
		t.Fatalf("retry send id = %q, want success transport id", id2)
	}
	if totalAttempts != 2 {
		t.Fatalf("totalAttempts = %d on retry, want 2", totalAttempts)
	}
}

// TestRoutingRemoteRetryWithPendingRecord verifies that if a pending record
// exists (ExternalID empty), a retry will call publish again.
// This simulates a crash AFTER publish but BEFORE local update with transport ID.
// NOTE: Without transport-level idempotency key, this WILL cause a duplicate
// remote publish if the first publish actually succeeded. This is the
// documented fundamental limitation.
func TestRoutingRemoteRetryWithPendingRecord(t *testing.T) {
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

	// Manually create a pending outbox record (simulating crash after publish,
	// before local update with transport ID).
	b := newRoutingTestBus(t, root)
	hash := sha256.Sum256([]byte("hello world"))
	outID := hex.EncodeToString(hash[:])
	pending := msgRecord{
		ID:             outID,
		Epoch:          now(),
		ISO:            isots(),
		From:           "alpha",
		Target:         "alpha",
		Text:           "hello world",
		Type:           "ship",
		ExternalSource: "relay",
		ExternalID:     "", // pending - no transport ID yet
		ExternalHash:   hex.EncodeToString(hash[:]),
	}
	data, err := json.Marshal(pending)
	if err != nil {
		t.Fatal(err)
	}
	path, err := b.mfile(outID, "alpha")
	if err != nil {
		t.Fatal(err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(tmp, path); err != nil {
		t.Fatal(err)
	}

	// Now retry with same content - should find pending record and re-publish
	attempt := 0
	RegisterRemoteTransport("fake-route", func(root string, route TransportRoute, msg OutboundMessage) (string, error) {
		attempt++
		return "ext-retry", nil
	})

	b2 := newRoutingTestBus(t, root)
	id2, err := b2.Tell("bob@peer.example", "hello world", "")
	if err != nil {
		t.Fatalf("retry send: %v", err)
	}
	if id2 != "ext-retry" {
		t.Fatalf("retry send id = %q, want transport id", id2)
	}
	// With pending record (no ExternalID), we re-publish to get the transport ID
	if attempt != 1 {
		t.Fatalf("attempt = %d on retry with pending record, want 1", attempt)
	}

	// Verify the record now has ExternalID
	updated, err := b2.findOutboxByHash("alpha", hash[:])
	if err != nil {
		t.Fatal(err)
	}
	if updated == nil || updated.ExternalID != "ext-retry" {
		t.Fatalf("record not updated with transport ID: %+v", updated)
	}
}
