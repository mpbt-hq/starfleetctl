// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package opencode

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
)

// UnixDialer is a custom http.Transport dialer for Unix Domain Sockets.
type UnixDialer struct {
	socketPath string
}

// DialContext implements the http.DialContext interface for Unix Domain Sockets.
func (d *UnixDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	// We ignore the network and addr parameters and always dial our Unix socket
	var dialer net.Dialer
	return dialer.DialContext(ctx, "unix", d.socketPath)
}

// Client is a client for communicating with a headless opencode server
// over a Unix Domain Socket.
type Client struct {
	httpClient *http.Client
	baseURL    *url.URL
	password   string
	shipID     string
}

// NewClient creates a new opencode client for a ship's Unix Domain Socket.
func NewClient(shipID, socketPath, password string) (*Client, error) {
	socketPath = strings.TrimSpace(socketPath)
	if socketPath == "" {
		return nil, fmt.Errorf("socket path is required")
	}

	// Convert socket path to a URL for the HTTP client
	// We use a dummy scheme/host since the UnixDialer will override the connection
	baseURL := &url.URL{
		Scheme: "http",
		Host:   "localhost",
	}

	transport := &http.Transport{
		DialContext: (&UnixDialer{socketPath: socketPath}).DialContext,
	}

	client := &http.Client{
		Transport: transport,
		Timeout:   30 * time.Second,
	}

	return &Client{
		httpClient: client,
		baseURL:    baseURL,
		password:   password,
		shipID:     shipID,
	}, nil
}

// doRequest performs an HTTP request to the opencode server over UDS.
func (c *Client) doRequest(ctx context.Context, method, path string, body io.Reader) (*http.Response, error) {
	fullURL := c.baseURL.ResolveReference(&url.URL{Path: path})
	req, err := http.NewRequestWithContext(ctx, method, fullURL.String(), body)
	if err != nil {
		return nil, err
	}

	req.Header.Set("Content-Type", "application/json")
	req.SetBasicAuth("opencode", c.password)

	return c.httpClient.Do(req)
}

// Session represents an opencode session.
type Session struct {
	ID        string `json:"id"`
	Title     string `json:"title"`
	Model     string `json:"model"`
	ModelID   string `json:"modelID"`
	Provider  string `json:"provider"`
	Status    string `json:"status"`
	CreatedAt string `json:"createdAt"`
	UpdatedAt string `json:"updatedAt"`
}

// ListSessions returns all sessions from the opencode server.
func (c *Client) ListSessions(ctx context.Context) ([]Session, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/api/session", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list sessions: HTTP %d: %s", resp.StatusCode, string(body))
	}

	var data struct {
		Data []Session `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decode sessions: %w", err)
	}
	return data.Data, nil
}

// CreateSession creates a new session on the opencode server.
func (c *Client) CreateSession(ctx context.Context, model string) (*Session, error) {
	body := map[string]string{
		"model": model,
	}
	bodyBytes, _ := json.Marshal(body)

	resp, err := c.doRequest(context.Background(), http.MethodPost, "/api/session", bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK && resp.StatusCode != http.StatusCreated {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("create session: HTTP %d: %s", resp.StatusCode, string(body))
	}

	var session Session
	if err := json.NewDecoder(resp.Body).Decode(&session); err != nil {
		return nil, fmt.Errorf("decode session: %w", err)
	}
	return &session, nil
}

// MessagePart represents a message part in opencode's API.
type MessagePart struct {
	Type string `json:"type"`
	Text string `json:"text,omitempty"`
}

// ChatMessage represents a chat message for the /api/v1/agent/chat endpoint.
type ChatMessage struct {
	Model    string     `json:"model"`
	Messages []ChatPart `json:"messages"`
	Stream   bool       `json:"stream"`
}

type ChatPart struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// ChatCompletion sends a chat completion request to the opencode server.
func (c *Client) ChatCompletion(ctx context.Context, model string, messages []ChatPart, stream bool) (*http.Response, error) {
	req := ChatMessage{
		Model:    model,
		Messages: messages,
		Stream:   stream,
	}
	bodyBytes, _ := json.Marshal(req)

	return c.doRequest(ctx, http.MethodPost, "/v1/chat/completions", bytes.NewReader(bodyBytes))
}

// Model represents an opencode model.
type Model struct {
	ID      string `json:"id"`
	Label   string `json:"label,omitempty"`
	Context int    `json:"context,omitempty"`
	Output  int    `json:"output,omitempty"`
}

// ListModels returns the list of available models.
func (c *Client) ListModels(ctx context.Context) ([]Model, error) {
	resp, err := c.doRequest(ctx, http.MethodGet, "/v1/models", nil)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("list models: HTTP %d: %s", resp.StatusCode, string(body))
	}

	var data struct {
		Data []Model `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&data); err != nil {
		return nil, fmt.Errorf("decode models: %w", err)
	}
	return data.Data, nil
}

// SocketPath returns the standard socket path for a ship.
func SocketPath(workspaceRoot, shipID string) string {
	return filepath.Join(workspaceRoot, ".starfleet-ai", "var", "ships", shipID, "opencode.sock")
}

// ServerConfig holds configuration for the opencode server.
type ServerConfig struct {
	SocketPath string
	Password   string
	Model      string
	// Additional config options can be added here
}

// Server represents a headless opencode server instance.
type Server struct {
	config ServerConfig
	cmd    *exec.Cmd
	socket net.Listener
}

func (s *Server) Start() error {
	// Remove stale socket file if it exists
	if err := os.Remove(s.config.SocketPath); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("remove stale socket: %w", err)
	}

	listener, err := net.Listen("unix", s.config.SocketPath)
	if err != nil {
		return fmt.Errorf("listen on unix socket: %w", err)
	}
	s.socket = listener

	// Build the opencode serve command
	args := []string{"serve", "--socket", s.config.SocketPath}
	if s.config.Password != "" {
		args = append(args, "--password", s.config.Password)
	}
	if s.config.Model != "" {
		args = append(args, "--model", s.config.Model)
	}

	cmd := exec.Command("opencode", args...)
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	s.cmd = cmd

	if err := cmd.Start(); err != nil {
		listener.Close()
		return fmt.Errorf("start opencode: %w", err)
	}

	return nil
}

func (s *Server) Stop() error {
	if s.cmd != nil && s.cmd.Process != nil {
		s.cmd.Process.Kill()
		s.cmd.Wait()
	}
	if s.socket != nil {
		s.socket.Close()
	}
	// Clean up socket file
	os.Remove(s.config.SocketPath)
	return nil
}

// SendMessage sends a message to a session via the opencode server.
// This is used for message injection (A2A communication) over UDS.
func (c *Client) SendMessage(ctx context.Context, sessionID string, parts []MessagePart) (string, error) {
	body := map[string]any{
		"parts": parts,
	}
	bodyBytes, _ := json.Marshal(body)

	resp, err := c.doRequest(ctx, http.MethodPost, fmt.Sprintf("/api/session/%s/message", sessionID), bytes.NewReader(bodyBytes))
	if err != nil {
		return "", err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("send message: HTTP %d: %s", resp.StatusCode, string(body))
	}

	// For non-streaming, read the full response
	result, err := io.ReadAll(resp.Body)
	if err != nil {
		return "", fmt.Errorf("read response: %w", err)
	}
	return string(result), nil
}

// SendMessageStream sends a message and returns an SSE stream reader.
// For streaming responses, the caller should handle the SSE stream.
func (c *Client) SendMessageStream(ctx context.Context, sessionID string, parts []MessagePart) (io.ReadCloser, error) {
	body := map[string]any{
		"parts":  parts,
		"stream": true,
	}
	bodyBytes, _ := json.Marshal(body)

	resp, err := c.doRequest(ctx, http.MethodPost, fmt.Sprintf("/api/session/%s/message", sessionID), bytes.NewReader(bodyBytes))
	if err != nil {
		return nil, err
	}

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		resp.Body.Close()
		return nil, fmt.Errorf("send message stream: HTTP %d: %s", resp.StatusCode, string(body))
	}

	return resp.Body, nil
}
