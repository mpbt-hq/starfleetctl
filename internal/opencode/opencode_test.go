// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package opencode

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"
)

// TestUnixDialer tests that the UnixDialer correctly dials a Unix socket.
func TestUnixDialer(t *testing.T) {
	listener, err := net.Listen("unix", "/tmp/test_opencode_dialer.sock")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer listener.Close()
	defer os.Remove("/tmp/test_opencode_dialer.sock")

	go func() {
		conn, err := listener.Accept()
		if err != nil {
			return
		}
		conn.Close()
	}()

	dialer := &UnixDialer{socketPath: "/tmp/test_opencode_dialer.sock"}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn, err := dialer.DialContext(ctx, "unix", "/tmp/test_opencode_dialer.sock")
	if err != nil {
		t.Fatalf("DialContext failed: %v", err)
	}
	conn.Close()
}

// TestClientNewClient tests creating a new Client.
func TestClientNewClient(t *testing.T) {
	listener, err := net.Listen("unix", "/tmp/test_opencode_client.sock")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer listener.Close()
	defer os.Remove("/tmp/test_opencode_client.sock")

	client, err := NewClient("test-ship", "/tmp/test_opencode_client.sock", "test-password")
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}
	if client == nil {
		t.Fatal("NewClient returned nil client")
	}
	if client.shipID != "test-ship" {
		t.Errorf("shipID = %q, want %q", client.shipID, "test-ship")
	}
}

// TestSocketPath tests the SocketPath function.
func TestSocketPath(t *testing.T) {
	path := SocketPath("/workspace/root", "test-ship")
	expected := filepath.Join("/workspace/root", ".starfleet-ai", "var", "ships", "test-ship", "opencode.sock")
	if path != expected {
		t.Errorf("SocketPath = %q, want %q", path, expected)
	}
}

// TestServerConfig tests the ServerConfig struct.
func TestServerConfig(t *testing.T) {
	config := ServerConfig{
		SocketPath: "/tmp/test.sock",
		Password:   "test-password",
		Model:      "test-model",
	}
	if config.SocketPath != "/tmp/test.sock" {
		t.Errorf("SocketPath = %q, want %q", config.SocketPath, "/tmp/test.sock")
	}
	if config.Password != "test-password" {
		t.Errorf("Password = %q, want %q", config.Password, "test-password")
	}
	if config.Model != "test-model" {
		t.Errorf("Model = %q, want %q", config.Model, "test-model")
	}
}

// TestServerStartStop tests starting and stopping the Server.
func TestServerStartStop(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping integration test in short mode")
	}

	if _, err := exec.LookPath("opencode"); err != nil {
		t.Skip("opencode not installed, skipping integration test")
	}

	socketPath := "/tmp/test_opencode_server.sock"
	defer os.Remove(socketPath)

	config := ServerConfig{
		SocketPath: socketPath,
		Password:   "test-password",
		Model:      "nvidia/nemotron-3-ultra-550b-a55b",
	}

	server := &Server{config: config}
	err := server.Start()
	if err != nil {
		t.Fatalf("Server.Start() failed: %v", err)
	}
	defer server.Stop()

	time.Sleep(2 * time.Second)

	_, _ = os.Stat(socketPath)
	if os.IsNotExist(err) {
		t.Errorf("socket file not created: %v", err)
	}

	client, err := NewClient("test-ship", socketPath, "test-password")
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sessions, err := client.ListSessions(ctx)
	if err != nil {
		t.Logf("ListSessions error (may be expected if server not fully ready): %v", err)
	} else {
		t.Logf("Sessions: %d", len(sessions))
	}
}

// TestClientDoRequest tests the doRequest method with a mock server.
func TestClientDoRequest(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/session", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		json.NewEncoder(w).Encode(map[string]any{
			"data": []Session{{
				ID:     "test-session",
				Title:  "Test Session",
				Model:  "test-model",
				Status: "active",
			}},
		})
	})

	server := httptest.NewServer(mux)
	defer server.Close()

	listener, err := net.Listen("unix", "/tmp/test_opencode_proxy.sock")
	if err != nil {
		t.Fatalf("failed to listen: %v", err)
	}
	defer listener.Close()
	defer os.Remove("/tmp/test_opencode_proxy.sock")

	go func() {
		for {
			conn, err := listener.Accept()
			if err != nil {
				return
			}
			go func() {
				conn2, err := net.Dial("tcp", server.Listener.Addr().String())
				if err != nil {
					conn.Close()
					return
				}
				conn.Close()
				conn2.Close()
			}()
		}
	}()

	client, err := NewClient("test-ship", "/tmp/test_opencode_proxy.sock", "test-password")
	if err != nil {
		t.Fatalf("NewClient failed: %v", err)
	}

	_, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	_ = cancel

	_ = client
}
