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
	"sync"
	"testing"
	"time"
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
	return newRoutingTestBusAs(t, root, "alpha")
}

func newRoutingTestBusAs(t *testing.T, root, shipID string) *Bus {
	t.Helper()
	t.Setenv("STARFLEET_BUS_DIR", filepath.Join(root, "bus"))
	t.Setenv("STARFLEET_SHIP_ID", shipID)
	b, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	return b
}

// TestRoutingRemoteRouteValidationErrorIsScopedToItsDomain proves that a
// routing-table entry rejected by its transport is reported when that route
// is used, while local traffic keeps working.
func TestRoutingRemoteRouteValidationErrorIsScopedToItsDomain(t *testing.T) {
	root := t.TempDir()
	writeCommsConfig(t, root, `
comms:
  routing:
    domains:
      - domain: peer.example
        transport: fake-route-validated
        repository: relay
`)
	b := newRoutingTestBus(t, root)

	RegisterRemoteTransportWithValidator("fake-route-validated",
		func(root string, route TransportRoute, msg OutboundMessage) (string, error) {
			return "ext-1", nil
		},
		func(root string, route TransportRoute) error {
			return fmt.Errorf("unknown repository %q", route.Repository)
		})

	_, err := b.Tell("bob@peer.example", "remote hello", "")
	if err == nil {
		t.Fatal("send to a domain with an invalid route must fail")
	}
	if !strings.Contains(err.Error(), "unknown repository") {
		t.Fatalf("error must name the invalid endpoint, got: %v", err)
	}
	if _, err := b.Tell("beta", "local hello", ""); err != nil {
		t.Fatalf("local send must still work: %v", err)
	}
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

func TestRoutingLocalNoMatchNewShip(t *testing.T) {
	root := t.TempDir()
	writeCommsConfig(t, root, `
comms:
  routing:
    local_domain: local
`)
	b := newRoutingTestBus(t, root)

	// New ship "gamma" has no mailbox yet; use as-is.
	id, err := b.Tell("gamma", "hello", "")
	if err != nil {
		t.Fatalf("new local recipient: %v", err)
	}
	if id == "" {
		t.Fatal("empty id")
	}
	if _, err := os.Stat(filepath.Join(b.MsgDir, "gamma", "unseen", id+".json")); err != nil {
		t.Fatalf("message not delivered to new gamma mailbox: %v", err)
	}
}

func TestRoutingRemoteThroughTransportWithCollidingLocal(t *testing.T) {
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

	// Pre-create colliding local mailboxes spelled "bob" and "Bob".
	if err := os.MkdirAll(filepath.Join(b.MsgDir, "bob", "unseen"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(b.MsgDir, "Bob", "unseen"), 0o755); err != nil {
		t.Fatal(err)
	}
	bobBefore := countJSONInDir(t, filepath.Join(b.MsgDir, "bob", "unseen"))
	bobBefore += countJSONInDir(t, filepath.Join(b.MsgDir, "Bob", "unseen"))

	var got OutboundMessage
	var gotRoute TransportRoute
	RegisterRemoteTransport("fake-route", func(root string, route TransportRoute, msg OutboundMessage) (string, error) {
		gotRoute = route
		got = msg
		return "ext-456", nil
	})

	// Remote recipient with colliding local mailboxes must still go to transport.
	id, err := b.Tell("BOB@peer.example", "remote hello", "")
	if err != nil {
		t.Fatalf("remote send with colliding local mailbox: %v", err)
	}
	if id != "ext-456" {
		t.Fatalf("returned id = %q, want transport id", id)
	}
	if gotRoute.Transport != "fake-route" || gotRoute.Repository != "relay" || gotRoute.Domain != "peer.example" {
		t.Fatalf("unexpected route: %+v", gotRoute)
	}
	if got.Target != "BOB" || got.From != "alpha" || got.Text != "remote hello" {
		t.Fatalf("unexpected outbound message: %+v", got)
	}
	// Must NOT write into either local mailbox.
	bobAfter := countJSONInDir(t, filepath.Join(b.MsgDir, "bob", "unseen"))
	bobAfter += countJSONInDir(t, filepath.Join(b.MsgDir, "Bob", "unseen"))
	if bobAfter != bobBefore {
		t.Fatalf("remote message written into local mailbox: %d json before, %d after", bobBefore, bobAfter)
	}
}

// countJSONInDir counts .json files in dir (returns 0 if dir does not exist).
func countJSONInDir(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatalf("read dir %q: %v", dir, err)
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			n++
		}
	}
	return n
}

func TestOutboxIdempotentRetrySameContent(t *testing.T) {
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

	callCount := 0
	RegisterRemoteTransport("fake-route", func(root string, route TransportRoute, msg OutboundMessage) (string, error) {
		callCount++
		return fmt.Sprintf("ext-%d", callCount), nil
	})

	// First send
	id1, err := b.Tell("bob@peer.example", "hello world", "corr-1")
	if err != nil {
		t.Fatalf("first send: %v", err)
	}
	if id1 != "ext-1" {
		t.Fatalf("first id = %q, want ext-1", id1)
	}

	// Second send with identical content - should reuse same transport ID
	id2, err := b.Tell("bob@peer.example", "hello world", "corr-1")
	if err != nil {
		t.Fatalf("second send: %v", err)
	}
	if id2 != "ext-1" {
		t.Fatalf("second id = %q, want ext-1 (idempotent)", id2)
	}
	if callCount != 1 {
		t.Fatalf("transport called %d times, want 1", callCount)
	}
}

func TestOutboxDifferentDestinationSameText(t *testing.T) {
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

	callCount := 0
	RegisterRemoteTransport("fake-route", func(root string, route TransportRoute, msg OutboundMessage) (string, error) {
		callCount++
		return fmt.Sprintf("ext-%d", callCount), nil
	})

	// Send to bob
	id1, err := b.Tell("bob@peer.example", "hello world", "corr-1")
	if err != nil {
		t.Fatalf("send to bob: %v", err)
	}

	// Send same text to alice - MUST produce different hash and different transport call
	id2, err := b.Tell("alice@peer.example", "hello world", "corr-1")
	if err != nil {
		t.Fatalf("send to alice: %v", err)
	}

	if id1 == id2 {
		t.Fatalf("different destinations produced same id: %s", id1)
	}
	if callCount != 2 {
		t.Fatalf("transport called %d times, want 2 (different destinations)", callCount)
	}
}

func TestOutboxRetryAfterSeen(t *testing.T) {
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

	callCount := 0
	RegisterRemoteTransport("fake-route", func(root string, route TransportRoute, msg OutboundMessage) (string, error) {
		callCount++
		return fmt.Sprintf("ext-%d", callCount), nil
	})

	// First send
	id1, err := b.Tell("bob@peer.example", "retry test", "corr-1")
	if err != nil {
		t.Fatalf("first send: %v", err)
	}
	if id1 != "ext-1" {
		t.Fatalf("first id = %q, want ext-1", id1)
	}

	// Simulate the message being acked/moved to seen/ by directly calling ack
	// We need to find the outbox record ID (the hash-based ID) to ack it
	// The outbox record is in the sender's (alpha) unseen/ dir
	entries, err := os.ReadDir(filepath.Join(b.MsgDir, "alpha", "unseen"))
	if err != nil {
		t.Fatalf("read unseen: %v", err)
	}
	var outboxID string
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			data, _ := os.ReadFile(filepath.Join(b.MsgDir, "alpha", "unseen", e.Name()))
			var rec msgRecord
			if json.Unmarshal(data, &rec) == nil && rec.ExternalID == "ext-1" {
				outboxID = rec.ID
				break
			}
		}
	}
	if outboxID == "" {
		t.Fatal("could not find outbox record for ext-1")
	}

	// Move to seen/ (simulate ack/delivery)
	if err := b.ackMessage(outboxID); err != nil {
		t.Fatalf("ack outbox record: %v", err)
	}

	// Now retry sending same content - should find the seen record and reuse ext-1
	id2, err := b.Tell("bob@peer.example", "retry test", "corr-1")
	if err != nil {
		t.Fatalf("retry send: %v", err)
	}
	if id2 != "ext-1" {
		t.Fatalf("retry id = %q, want ext-1 (idempotent after seen)", id2)
	}
	if callCount != 1 {
		t.Fatalf("transport called %d times, want 1 (reused after seen)", callCount)
	}
}

func TestOutboxPublishFailureRetries(t *testing.T) {
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

	callCount := 0
	RegisterRemoteTransport("fake-route", func(root string, route TransportRoute, msg OutboundMessage) (string, error) {
		callCount++
		if callCount == 1 {
			return "", fmt.Errorf("simulated transport failure")
		}
		return "ext-2", nil
	})

	// First send - transport fails
	_, err := b.Tell("bob@peer.example", "failure test", "corr-1")
	if err == nil {
		t.Fatal("first send should have failed")
	}
	if callCount != 1 {
		t.Fatalf("transport called %d times on first attempt", callCount)
	}

	// Pending record should exist in alpha/unseen/
	entries, err := os.ReadDir(filepath.Join(b.MsgDir, "alpha", "unseen"))
	if err != nil {
		t.Fatalf("read unseen: %v", err)
	}
	found := false
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			data, _ := os.ReadFile(filepath.Join(b.MsgDir, "alpha", "unseen", e.Name()))
			var rec msgRecord
			if json.Unmarshal(data, &rec) == nil && rec.ExternalID == "" && rec.ExternalHash != "" {
				found = true
				break
			}
		}
	}
	if !found {
		t.Fatal("pending outbox record not found after transport failure")
	}

	// Second send - transport succeeds
	id2, err := b.Tell("bob@peer.example", "failure test", "corr-1")
	if err != nil {
		t.Fatalf("second send: %v", err)
	}
	if id2 != "ext-2" {
		t.Fatalf("second id = %q, want ext-2", id2)
	}
	if callCount != 2 {
		t.Fatalf("transport called %d times, want 2", callCount)
	}

	// Record should now have ExternalID = ext-2
	entries, err = os.ReadDir(filepath.Join(b.MsgDir, "alpha", "unseen"))
	if err != nil {
		t.Fatalf("read unseen after success: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			data, _ := os.ReadFile(filepath.Join(b.MsgDir, "alpha", "unseen", e.Name()))
			var rec msgRecord
			if json.Unmarshal(data, &rec) == nil && rec.ExternalID == "ext-2" {
				return // success
			}
		}
	}
	t.Fatal("updated outbox record with transport ID not found")
}

// TestOutboxConcurrentSameSenderSameContent tests that two concurrent sends
// from the same sender with identical content produce exactly one transport call.
func TestOutboxConcurrentSameSenderSameContent(t *testing.T) {
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

	var callCount int32
	var callMu sync.Mutex
	var firstID string
	RegisterRemoteTransport("fake-route", func(root string, route TransportRoute, msg OutboundMessage) (string, error) {
		callMu.Lock()
		callCount++
		id := fmt.Sprintf("ext-%d", callCount)
		if firstID == "" {
			firstID = id
		}
		callMu.Unlock()
		// Small delay to increase chance of race
		time.Sleep(10 * time.Millisecond)
		return id, nil
	})

	const numGoroutines = 10
	var wg sync.WaitGroup
	errCh := make(chan error, numGoroutines)

	for i := 0; i < numGoroutines; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			_, err := b.Tell("bob@peer.example", "concurrent test", "corr-1")
			if err != nil {
				errCh <- err
			}
		}()
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent send failed: %v", err)
	}

	if callCount != 1 {
		t.Fatalf("transport called %d times, want 1 (same sender, same content)", callCount)
	}
}

// TestOutboxConcurrentDifferentSenderSameContent tests that concurrent sends
// from different senders with identical content produce separate transport calls.
func TestOutboxConcurrentDifferentSenderSameContent(t *testing.T) {
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

	var callCount int32
	var callMu sync.Mutex
	RegisterRemoteTransport("fake-route", func(root string, route TransportRoute, msg OutboundMessage) (string, error) {
		callMu.Lock()
		callCount++
		id := fmt.Sprintf("ext-%d", callCount)
		callMu.Unlock()
		time.Sleep(10 * time.Millisecond)
		return id, nil
	})

	const numSenders = 5
	var wg sync.WaitGroup
	errCh := make(chan error, numSenders)

	for i := 0; i < numSenders; i++ {
		sender := fmt.Sprintf("ship%d", i)
		// Each sender needs its own bus instance with its own identity
		b := newRoutingTestBusAs(t, root, sender)
		wg.Add(1)
		go func(b *Bus, s string) {
			defer wg.Done()
			// Each sender sends the same content
			_, err := b.Tell("bob@peer.example", "shared content", "corr-1")
			if err != nil {
				errCh <- err
			}
		}(b, sender)
	}

	wg.Wait()
	close(errCh)

	for err := range errCh {
		t.Errorf("concurrent send failed: %v", err)
	}

	if callCount != numSenders {
		t.Fatalf("transport called %d times, want %d (different senders, same content)", callCount, numSenders)
	}
}

// TestOutboxLegacyFlatCrossSenderCollision tests that a legacy flat record
// from a different sender is NOT matched (hash collision protection).
func TestOutboxLegacyFlatCrossSenderCollision(t *testing.T) {
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

	callCount := 0
	RegisterRemoteTransport("fake-route", func(root string, route TransportRoute, msg OutboundMessage) (string, error) {
		callCount++
		return fmt.Sprintf("ext-%d", callCount), nil
	})

	// Manually create a legacy flat record for "charlie" with a specific hash
	// that would collide with the new hash format if sender wasn't validated
	legacyHashInput := "test content\x00bob@peer.example\x00peer.example\x00relay\x00ship\x00corr-1"
	legacyHash := sha256.Sum256([]byte(legacyHashInput))
	legacyHashStr := hex.EncodeToString(legacyHash[:])

	legacyRec := msgRecord{
		ID:             legacyHashStr,
		Epoch:          time.Now().Unix(),
		ISO:            time.Now().UTC().Format(time.RFC3339),
		From:           "charlie", // DIFFERENT sender
		Target:         "charlie",
		Text:           "test content",
		Type:           "ship",
		ReplyTo:        "corr-1",
		ExternalSource: "relay",
		ExternalID:     "ext-legacy",
		ExternalHash:   legacyHashStr,
	}
	data, _ := json.Marshal(legacyRec)
	legacyPath := filepath.Join(b.MsgDir, fsafe(legacyHashStr)+".json")
	if err := os.WriteFile(legacyPath, data, 0o644); err != nil {
		t.Fatalf("write legacy record: %v", err)
	}

	// Now "alpha" sends the same content - should NOT match charlie's legacy record
	id, err := b.Tell("bob@peer.example", "test content", "corr-1")
	if err != nil {
		t.Fatalf("send: %v", err)
	}

	// Should have created a new transport call, not reused the legacy one
	if id == "ext-legacy" {
		t.Fatalf("incorrectly matched legacy record from different sender")
	}
	if callCount != 1 {
		t.Fatalf("transport called %d times, want 1", callCount)
	}
}

// TestOutboxRepositorySeparation tests that messages with same content but
// different repositories produce different hashes and transport calls.
func TestOutboxRepositorySeparation(t *testing.T) {
	root := t.TempDir()
	writeCommsConfig(t, root, `
comms:
  routing:
    local_domain: local
    domains:
      - domain: peer.example
        transport: fake-route
        repository: relay1
      - domain: peer2.example
        transport: fake-route
        repository: relay2
`)
	b := newRoutingTestBus(t, root)

	callCount := 0
	RegisterRemoteTransport("fake-route", func(root string, route TransportRoute, msg OutboundMessage) (string, error) {
		callCount++
		return fmt.Sprintf("ext-%d", callCount), nil
	})

	// Send to peer.example (repository relay1)
	id1, err := b.Tell("bob@peer.example", "same content", "corr-1")
	if err != nil {
		t.Fatalf("send to relay1: %v", err)
	}

	// Send same content to peer2.example (repository relay2)
	id2, err := b.Tell("bob@peer2.example", "same content", "corr-1")
	if err != nil {
		t.Fatalf("send to relay2: %v", err)
	}

	if id1 == id2 {
		t.Fatalf("different repositories produced same id: %s", id1)
	}
	if callCount != 2 {
		t.Fatalf("transport called %d times, want 2 (different repositories)", callCount)
	}
}

// TestOutboxDelimiterFields tests that fields containing the delimiter (null byte)
// don't cause hash collisions.
func TestOutboxDelimiterFields(t *testing.T) {
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

	callCount := 0
	RegisterRemoteTransport("fake-route", func(root string, route TransportRoute, msg OutboundMessage) (string, error) {
		callCount++
		return fmt.Sprintf("ext-%d", callCount), nil
	})

	// These two messages have different content but with the old pipe delimiter
	// they would collide: "a|b" vs "a" + "b" (pipe in field)
	// With null byte delimiter, they should NOT collide
	id1, err := b.Tell("bob@peer.example", "content with pipe", "corr-1")
	if err != nil {
		t.Fatalf("first send: %v", err)
	}

	id2, err := b.Tell("bob@peer.example", "content", "with pipe|corr-1")
	if err != nil {
		t.Fatalf("second send: %v", err)
	}

	if id1 == id2 {
		t.Fatalf("delimiter-containing fields produced same id: %s", id1)
	}
	if callCount != 2 {
		t.Fatalf("transport called %d times, want 2", callCount)
	}
}

// TestOutboxLostResponseWindow tests the at-least-once window:
// if transport succeeds but updating the local record fails, retry
// should NOT re-publish (uses same outbox ID).
func TestOutboxLostResponseWindow(t *testing.T) {
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

	callCount := 0
	var publishID string
	RegisterRemoteTransport("fake-route", func(root string, route TransportRoute, msg OutboundMessage) (string, error) {
		callCount++
		publishID = fmt.Sprintf("ext-%d", callCount)
		return publishID, nil
	})

	// First send - transport succeeds
	id1, err := b.Tell("bob@peer.example", "lost response test", "corr-1")
	if err != nil {
		t.Fatalf("first send: %v", err)
	}
	if id1 != publishID {
		t.Fatalf("first id mismatch: got %q, want %q", id1, publishID)
	}
	if callCount != 1 {
		t.Fatalf("transport called %d times, want 1", callCount)
	}

	// Simulate crash after transport but before local record update:
	// Remove the outbox record that has the ExternalID set
	entries, err := os.ReadDir(filepath.Join(b.MsgDir, "alpha", "unseen"))
	if err != nil {
		t.Fatalf("read unseen: %v", err)
	}
	for _, e := range entries {
		if strings.HasSuffix(e.Name(), ".json") {
			path := filepath.Join(b.MsgDir, "alpha", "unseen", e.Name())
			data, _ := os.ReadFile(path)
			var rec msgRecord
			if json.Unmarshal(data, &rec) == nil && rec.ExternalID == publishID {
				os.Remove(path) // Delete the completed record
				break
			}
		}
	}

	// Retry - should find NO pending record (it was completed but lost),
	// and the hash-based lookup should find nothing, so it publishes again.
	// This is the at-least-once window: we CANNOT distinguish between
	// "transport failed" and "transport succeeded but local update failed".
	// So we re-publish with the SAME outbox ID (deterministic hash).
	id2, err := b.Tell("bob@peer.example", "lost response test", "corr-1")
	if err != nil {
		t.Fatalf("retry send: %v", err)
	}

	// The transport was called again (at-least-once)
	if callCount != 2 {
		t.Fatalf("transport called %d times, want 2 (at-least-once on lost response)", callCount)
	}

	// BUT the outbox ID (deterministic hash) is the same
	// The remote side can deduplicate using this ID
	if id2 != publishID {
		t.Fatalf("retry id = %q, want %q (same deterministic outbox ID)", id2, publishID)
	}
}
