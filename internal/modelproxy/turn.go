// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package modelproxy

import (
	"context"
	"fmt"
	"io"
	"math/rand"
	"net/http"
	"sync"
	"time"
)

// Turn represents the central data object flowing through the pipeline.
// It's transport-agnostic and contains all metadata needed for processing
// a language model request.
type Turn struct {
	// Request-Info
	RequestID   string
	ShipID      string
	Model       string
	Strategy    string
	Messages    []Message
	Tools       []Tool
	Temperature float32
	MaxTokens   int
	Stream      bool

	// Pipeline-State (modified during pipeline processing)
	Provider        string // Selected provider
	Attempt         int    // Current attempt number
	StartTime       time.Time
	Metrics         *TurnMetrics
	Context         context.Context
	ErrorClass      ErrorClass    // Classified error for retry decisions
	PreparedRequest *http.Request // Prepared upstream request

	// Response-Assembly
	StatusCode   int
	Headers      http.Header
	BodyReader   io.Reader        // Final reader for HTTP response
	StreamChunks chan StreamChunk // For streaming responses
	Error        error
}

// TurnMetrics tracks metrics for a single turn/request.
type TurnMetrics struct {
	RequestCount        int64
	ErrorCount          int64
	TotalLatency        time.Duration
	TotalTokens         int64
	ConsecutiveFailures int
	LastRequest         time.Time
	mu                  sync.Mutex
}

// Message represents a chat message in the conversation.
type Message struct {
	Role    string `json:"role"`    // system, user, assistant, tool
	Content string `json:"content"` // Message content
}

// Tool represents a tool that can be called by the model.
type Tool struct {
	// Tool definition would go here based on actual implementation
	// For now, keeping it simple as the exact structure isn't critical for the pipeline
	Name        string
	Description string
	Parameters  map[string]interface{}
}

// StreamChunk represents a chunk of data in a streaming response.
type StreamChunk struct {
	Type     ChunkType // Content, Delta, Done, Error
	Data     []byte
	Metadata map[string]string
}

// ChunkType defines the type of a stream chunk.
type ChunkType string

const (
	ChunkContent    ChunkType = "content"
	ChunkDelta      ChunkType = "delta"
	ChunkDone       ChunkType = "done"
	ChunkError      ChunkType = "error"
	ChunkToolCall   ChunkType = "tool_call"
	ChunkToolResult ChunkType = "tool_result"
)

// NewTurn creates a new Turn from an HTTP request.
func NewTurn(r *http.Request) *Turn {
	return &Turn{
		RequestID:       generateRequestID(),
		Context:         r.Context(),
		Headers:         make(http.Header),
		StreamChunks:    make(chan StreamChunk, 10), // Buffered channel for streaming
		StartTime:       time.Now(),
		Metrics:         &TurnMetrics{},
		PreparedRequest: nil,
	}
}

// generateRequestID creates a unique request ID.
func generateRequestID() string {
	// Simple implementation - in production would use better UUID generation
	return fmt.Sprintf("req_%d_%d", time.Now().UnixNano(), rand.Intn(1000000))
}

// HandleError processes an error that occurred during pipeline execution.
// It can be used to trigger fallback logic or error response assembly.
func (t *Turn) HandleError(err error) error {
	t.Error = err
	return err
}

// IsStreaming returns true if the turn represents a streaming request.
func (t *Turn) IsStreaming() bool {
	return t.Stream
}

// SessionID returns the session ID from the turn's headers.
func (t *Turn) SessionID() string {
	// Try to get from headers first.
	if sessionID := t.Headers.Get("X-Session-Id"); sessionID != "" {
		return sessionID
	}
	if sessionID := t.Headers.Get("x-opencode-session"); sessionID != "" {
		return sessionID
	}
	// If not in headers, try to get from context? Not stored in turn.
	// We'll return empty string.
	return ""
}
