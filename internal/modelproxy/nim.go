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

// NIMAdapter adapts the NVIDIA NIM provider.
type NIMAdapter struct {
	*BaseAdapter
}

// NewNIMAdapter creates a new NIM adapter.
func NewNIMAdapter(p *Provider) ProviderAdapter {
	return &NIMAdapter{
		BaseAdapter: NewBaseAdapter(p),
	}
}

// Name returns the adapter name.
func (a *NIMAdapter) Name() string {
	return "nim"
}

// Capabilities returns the capabilities of the NIM provider.
func (a *NIMAdapter) Capabilities() ProviderCapabilities {
	return ProviderCapabilities{
		SupportsStreaming:  true,
		SupportsTools:      true,
		SupportsSystemMsgs: true,
		MaxTokens:          32768,
	}
}

// PrepareRequest prepares the upstream request for NIM.
func (a *NIMAdapter) PrepareRequest(turn *Turn) (*http.Request, error) {
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

// HandleResponse processes the NIM response.
func (a *NIMAdapter) HandleResponse(resp *http.Response, turn *Turn) (io.Reader, error) {
	if turn.Stream {
		return resp.Body, nil
	}
	return resp.Body, nil
}

// HandleStreamChunk processes a chunk of streaming data from NIM.
// NIM uses standard OpenAI-compatible streaming format.
func (a *NIMAdapter) HandleStreamChunk(chunk []byte, turn *Turn) (StreamChunk, error) {
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

// ClassifyError classifies an error for NIM.
func (a *NIMAdapter) ClassifyError(err error, resp *http.Response) ErrorClass {
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

// Register the NIM adapter in init.
func init() {
	reg := GetGlobalProviderRegistry()
	reg.Register("nim-proxy", NewNIMAdapter)
}
