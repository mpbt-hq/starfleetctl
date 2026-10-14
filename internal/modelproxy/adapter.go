// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package modelproxy

import (
	"context"
	"io"
	"net/http"
	"time"
)

// ProviderCapabilities defines what a provider can do.
type ProviderCapabilities struct {
	SupportsStreaming  bool
	SupportsTools      bool
	SupportsSystemMsgs bool
	MaxTokens          int
}

// ProviderAdapter defines the interface for provider-specific adapters.
// These sit at the bottom layer of the pipeline and handle provider-specific
// quirks and communication patterns.
type ProviderAdapter interface {
	// Name returns the provider name (e.g., "nim", "zen", "nvidia-direct").
	Name() string

	// Capabilities returns what this provider can do.
	Capabilities() ProviderCapabilities

	// PrepareRequest prepares the upstream request from the turn.
	// Returns the prepared HTTP request and any error.
	PrepareRequest(turn *Turn) (*http.Request, error)

	// HandleResponse processes the upstream response and returns a reader
	// for the response body and any error.
	HandleResponse(resp *http.Response, turn *Turn) (io.Reader, error)

	// HandleStreamChunk processes a chunk of streaming data.
	// Returns the processed stream chunk and any error.
	HandleStreamChunk(chunk []byte, turn *Turn) (StreamChunk, error)

	// ClassifyError classifies an error for retry/fallback decisions.
	ClassifyError(err error, resp *http.Response) ErrorClass

	// HealthCheck performs a health check against the provider.
	HealthCheck(ctx context.Context) HealthStatus
}

// ErrorClass categorizes errors for retry/fallback decisions.
type ErrorClass int

const (
	// ErrorClassRetryable indicates transient errors worth retrying
	// (network issues, timeouts, 5xx errors).
	ErrorClassRetryable ErrorClass = iota

	// ErrorClassRateLimit indicates rate limiting or quota exhaustion.
	ErrorClassRateLimit

	// ErrorClassAuthFailed indicates authentication or authorization failure.
	ErrorClassAuthFailed

	// ErrorClassInvalidRequest indicates invalid request (400 errors).
	ErrorClassInvalidRequest

	// ErrorClassUpstreamUnavailable indicates upstream service unavailable.
	ErrorClassUpstreamUnavailable

	// ErrorClassFatal indicates fatal errors not worth retrying.
	ErrorClassFatal
)

// HealthStatus represents the health status of a provider.
type HealthStatus struct {
	Healthy   bool
	Message   string
	Latency   time.Duration
	Timestamp time.Time
}
