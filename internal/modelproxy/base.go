// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package modelproxy

import (
	"context"
	"fmt"
	"io"
	"net/http"
	"time"
)

// BaseAdapter contains common functionality for all provider adapters.
type BaseAdapter struct {
	// We'll embed the config provider to have access to its fields.
	Provider *Provider
}

// NewBaseAdapter creates a new base adapter with the given config provider.
func NewBaseAdapter(p *Provider) *BaseAdapter {
	return &BaseAdapter{
		Provider: p,
	}
}

// Common methods that can be used by all adapters.

// prepareCommonRequest prepares the common parts of an HTTP request.
func (a *BaseAdapter) prepareCommonRequest(ctx context.Context, turn *Turn, method, url string, body io.Reader) (*http.Request, error) {
	req, err := http.NewRequestWithContext(ctx, method, url, body)
	if err != nil {
		return nil, err
	}
	// Set common headers
	req.Header.Set("Content-Type", "application/json")
	if a.Provider.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+a.Provider.APIKey)
	}
	// Apply provider-specific UserAgent if set
	if a.Provider.UserAgent != "" {
		req.Header.Set("User-Agent", a.Provider.UserAgent)
	}
	return req, nil
}

// applyUpstreamHeaders applies provider-specific headers to the request.
// This mimics the logic in the existing proxy's Provider.applyUpstreamHeaders.
func (a *BaseAdapter) applyUpstreamHeaders(req *http.Request, turn *Turn) {
	switch a.Provider.Type {
	case "opencode-zen":
		// OpenCode Zen requires specific headers for the free tier.
		// We'll forward the session and project IDs from the turn.
		if turn.ShipID != "" {
			req.Header.Set("X-Session-Id", turn.ShipID)
		}
		// Note: In the existing code, there are more headers like X-Opencode-Project, etc.
		// We'll need to extract those from the turn or context.
		// For now, we'll just set the session ID.
	default:
		// No special headers for other providers.
	}
}

// HealthCheck performs a basic health check by querying the /models endpoint.
func (a *BaseAdapter) HealthCheck(ctx context.Context) HealthStatus {
	start := time.Now()
	resp, err := http.Get(a.Provider.BaseURL + "/models")
	latency := time.Since(start)
	if err != nil {
		return HealthStatus{
			Healthy:   false,
			Message:   err.Error(),
			Latency:   latency,
			Timestamp: time.Now(),
		}
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return HealthStatus{
			Healthy:   false,
			Message:   fmt.Sprintf("unexpected status code: %d", resp.StatusCode),
			Latency:   latency,
			Timestamp: time.Now(),
		}
	}
	return HealthStatus{
		Healthy:   true,
		Message:   "OK",
		Latency:   latency,
		Timestamp: time.Now(),
	}
}
