// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package modelproxy

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
)

// ZenAdapter adapts the OpenCode Zen provider.
type ZenAdapter struct {
	*BaseAdapter
}

// NewZenAdapter creates a new Zen adapter.
func NewZenAdapter(p *Provider) ProviderAdapter {
	return &ZenAdapter{
		BaseAdapter: NewBaseAdapter(p),
	}
}

// Name returns the adapter name.
func (a *ZenAdapter) Name() string {
	return "zen"
}

// Capabilities returns the capabilities of the Zen provider.
func (a *ZenAdapter) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		SupportsStreaming:  true,
		SupportsTools:      true,
		SupportsSystemMsgs: true,
		MaxTokens:          32768,
	}
}

// PrepareRequest prepares the upstream request for Zen.
func (a *ZenAdapter) PrepareRequest(turn *Turn) (*http.Request, error) {
	req, err := a.prepareCommonRequest(turn.Context, turn, http.MethodPost, a.Provider.BaseURL+"/chat/completions", nil)
	if err != nil {
		return nil, err
	}

	// Build the request body from the turn.
	reqBody := map[string]interface{}{
		"model":    turn.Model,
		"messages": turn.Messages,
		"stream":   turn.Stream,
	}
	if turn.Temperature != 0 {
		reqBody["temperature"] = turn.Temperature
	}
	if turn.MaxTokens != 0 {
		reqBody["max_tokens"] = turn.MaxTokens
	}
	if len(turn.Tools) > 0 {
		reqBody["tools"] = turn.Tools
	}

	var buf bytes.Buffer
	enc := json.NewEncoder(&buf)
	enc.SetEscapeHTML(false)
	if err := enc.Encode(reqBody); err != nil {
		return nil, err
	}
	req.Body = io.NopCloser(&buf)
	req.ContentLength = int64(buf.Len())

	// Apply provider-specific headers.
	a.applyUpstreamHeaders(req, turn)

	return req, nil
}

// HandleResponse processes the Zen response.
func (a *ZenAdapter) HandleResponse(resp *http.Response, turn *Turn) (io.Reader, error) {
	if turn.Stream {
		return resp.Body, nil
	}
	return resp.Body, nil
}

// HandleStreamChunk processes a chunk of streaming data from Zen.
// Zen uses the same OpenAI-compatible streaming format as NIM.
func (a *ZenAdapter) HandleStreamChunk(chunk []byte, turn *Turn) (StreamChunk, error) {
	// Parse the chunk as an OpenAI streaming response.
	// Expected format: "data: {json}\n\n"
	// We'll strip the "data: " prefix and parse the JSON.
	if len(chunk) < 6 || !bytes.HasPrefix(chunk, []byte("data: ")) {
		// If it's not a data chunk, it might be a comment or empty line.
		if bytes.HasPrefix(chunk, []byte(": ")) {
			// Comment line, ignore.
			return StreamChunk{}, nil
		}
		if len(chunk) == 0 {
			// Empty line, ignore.
			return StreamChunk{}, nil
		}
		// If it's "[DONE]", we return a done chunk.
		if bytes.Equal(chunk, []byte("data: [DONE]")) {
			return StreamChunk{Type: ChunkDone, Data: nil}, nil
		}
		// Otherwise, we don't know what it is; return as content for now.
		return StreamChunk{Type: ChunkContent, Data: chunk}, nil
	}

	// Remove the "data: " prefix.
	data := chunk[6:]
	if len(data) == 0 {
		return StreamChunk{}, nil
	}

	// Parse the JSON.
	var streamResp struct {
		ID      string `json:"id"`
		Object  string `json:"object"`
		Created int64  `json:"created"`
		Model   string `json:"model"`
		Choices []struct {
			Index int `json:"index"`
			Delta struct {
				Role      string            `json:"role,omitempty"`
				Content   string            `json:"content,omitempty"`
				ToolCalls []json.RawMessage `json:"tool_calls,omitempty"`
			} `json:"delta"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
			TotalTokens      int `json:"total_tokens"`
		} `json:"usage"`
	}

	if err := json.Unmarshal(data, &streamResp); err != nil {
		return StreamChunk{}, err
	}

	// Convert to our StreamChunk type.
	if len(streamResp.Choices) > 0 {
		choice := &streamResp.Choices[0]
		delta := choice.Delta
		if delta.Content != "" {
			return StreamChunk{Type: ChunkDelta, Data: []byte(delta.Content)}, nil
		}
		if delta.Role != "" {
			// Role delta is not typically used in our chunk types; we might ignore or treat as content.
			// For now, we'll ignore role-only deltas.
			return StreamChunk{}, nil
		}
		if len(delta.ToolCalls) > 0 {
			// We'll need to marshal the tool calls back to JSON.
			toolCallsData, err := json.Marshal(delta.ToolCalls)
			if err != nil {
				return StreamChunk{}, err
			}
			return StreamChunk{Type: ChunkToolCall, Data: toolCallsData}, nil
		}
	}

	// If we have usage, we might want to update metrics, but we'll do that elsewhere.
	// For now, treat as content if there's any data.
	return StreamChunk{Type: ChunkContent, Data: data}, nil
}

// ClassifyError classifies an error for Zen.
func (a *ZenAdapter) ClassifyError(err error, resp *http.Response) ErrorClass {
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return ErrorClassFatal
		}
		if strings.Contains(err.Error(), "connection refused") ||
			strings.Contains(err.Error(), "connection reset") ||
			strings.Contains(err.Error(), "timeout") {
			return ErrorClassRetryable
		}
		return ErrorClassFatal
	}

	if resp != nil {
		switch resp.StatusCode {
		case http.StatusTooManyRequests:
			return ErrorClassRateLimit
		case http.StatusUnauthorized, http.StatusForbidden:
			return ErrorClassAuthFailed
		case http.StatusBadRequest:
			return ErrorClassInvalidRequest
		case http.StatusInternalServerError, http.StatusBadGateway, http.StatusServiceUnavailable, http.StatusGatewayTimeout:
			return ErrorClassRetryable
		default:
			if resp.StatusCode >= 400 && resp.StatusCode < 500 {
				return ErrorClassInvalidRequest
			}
			if resp.StatusCode >= 500 {
				return ErrorClassRetryable
			}
		}
	}

	return ErrorClassFatal
}

// applyUpstreamHeaders applies Zen-specific headers.
func (a *ZenAdapter) applyUpstreamHeaders(req *http.Request, turn *Turn) {
	// Call the base method to set common headers like User-Agent and Authorization.
	a.BaseAdapter.applyUpstreamHeaders(req, turn)

	// Zen-specific headers: we need to forward the session and project IDs.
	// In the existing proxy, these are set in the Provider.applyUpstreamHeaders method for type "opencode-zen".
	// We'll set them here.
	if turn.ShipID != "" {
		req.Header.Set("X-Session-Id", turn.ShipID)
	}
	// Note: We don't have the project ID in the turn. We might need to get it from the context or config.
	// For now, we'll leave it out.
	// The existing code also sets X-Opencode-Client and X-Opencode-Project.
	// We'll set a fixed client value if needed.
	req.Header.Set("X-Opencode-Client", "opencode")
	// We don't have the project ID, so we'll skip it for now.
}

// Register the Zen adapter in init.
func init() {
	reg := GetGlobalProviderRegistry()
	reg.Register("zen-proxy", NewZenAdapter)
}
