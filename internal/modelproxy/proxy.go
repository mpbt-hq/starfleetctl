// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package modelproxy

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"
)

// ModelInfo is one model entry served by GET /v1/models. The upstream fields
// (id, object, created, owned_by) are taken verbatim from the backend's
// /models response; Label/Context/Caps are enriched from the opencode model
// catalog (the upstreams themselves only expose the bare OpenAI fields).
type ModelInfo struct {
	ID      string   `json:"id"`
	Object  string   `json:"object,omitempty"`
	Created int64    `json:"created,omitempty"`
	OwnedBy string   `json:"owned_by,omitempty"`
	Label   string   `json:"label,omitempty"`
	Context int      `json:"context,omitempty"`
	Output  int      `json:"output,omitempty"`
	Caps    []string `json:"caps,omitempty"`
}

// Proxy is the local OpenAI-compatible model API server that fronts the
// configured upstream providers.
type Proxy struct {
	cfg    *Config
	logger *log.Logger
	// modelCache maps provider ID → set of model IDs, refreshed on demand.
	cacheMu   sync.RWMutex
	modelSets map[string]map[string]bool
	modelInfo map[string][]ModelInfo
	cacheAt   map[string]time.Time
	tracker   *shipTracker
	mux       *http.ServeMux

	// Saturation gate per provider: when a provider returns 429/saturation,
	// subsequent requests wait behind a single cooldown timer that respects
	// Retry-After headers, instead of each request retrying individually.
	satMu sync.Mutex
	sat   map[string]*saturationState
}

// saturationState tracks the saturation cooldown for a single provider.
type saturationState struct {
	providerID    string
	cooldownUntil time.Time
	retryAfter    time.Duration
}

// New builds a Proxy from a resolved config.
func New(cfg *Config) *Proxy {
	p := &Proxy{
		cfg:       cfg,
		logger:    log.Default(),
		modelSets: map[string]map[string]bool{},
		modelInfo: map[string][]ModelInfo{},
		cacheAt:   map[string]time.Time{},
		tracker:   newShipTracker(),
		sat:       map[string]*saturationState{},
	}
	p.mux = http.NewServeMux()
	p.mux.HandleFunc("/v1/models", p.handleModels)
	p.mux.HandleFunc("/v1/chat/completions", p.handleChat)
	p.mux.HandleFunc("/v1/ships", p.handleShips)
	p.mux.HandleFunc("/healthz", p.handleHealth)
	return p
}

// Handler returns the HTTP handler (for testing).
func (p *Proxy) Handler() http.Handler { return p.mux }

// ServeHTTP implements http.Handler.
func (p *Proxy) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	p.mux.ServeHTTP(w, r)
}

func (p *Proxy) logf(format string, args ...any) {
	p.logger.Printf("[model-proxy] "+format, args...)
}

func (p *Proxy) handleHealth(w http.ResponseWriter, _ *http.Request) {
	writeJSON(w, map[string]any{"ok": true})
}

// providerModelSet returns the set of model IDs a provider serves, using a
// short-lived cache and falling back to the upstream /models query. Empty on
// any error (the caller treats unknown models accordingly).
func (p *Proxy) providerModelSet(prov Provider) map[string]bool {
	p.cacheMu.RLock()
	set, ok := p.modelSets[prov.ID]
	at := p.cacheAt[prov.ID]
	p.cacheMu.RUnlock()
	if ok && time.Since(at) < 60*time.Second {
		return set
	}

	models, err := fetchModels(prov)
	if err != nil {
		p.logf("model query %s: %v", prov.ID, err)
		set = map[string]bool{}
	} else {
		set = make(map[string]bool, len(models))
		for _, id := range models {
			set[id] = true
		}
	}
	p.cacheMu.Lock()
	p.modelSets[prov.ID] = set
	p.cacheAt[prov.ID] = time.Now()
	p.cacheMu.Unlock()
	return set
}

// providerModelInfo returns the full model metadata a provider serves, using
// a short-lived cache and the upstream /models query as source of truth.
func (p *Proxy) providerModelInfo(prov Provider) []ModelInfo {
	p.cacheMu.RLock()
	info, ok := p.modelInfo[prov.ID]
	at := p.cacheAt[prov.ID]
	p.cacheMu.RUnlock()
	if ok && time.Since(at) < 60*time.Second {
		return info
	}

	raw, err := fetchModelInfo(prov)
	if err != nil {
		p.logf("model query %s: %v", prov.ID, err)
		info = nil
	} else {
		info = make([]ModelInfo, len(raw))
		for i, m := range raw {
			info[i] = m
			meta := catalog.lookup(m.ID)
			if info[i].Label == "" {
				info[i].Label = meta.Label
			}
			if info[i].Context == 0 {
				info[i].Context = meta.Context
			}
			if info[i].Output == 0 {
				info[i].Output = meta.Output
			}
			if len(info[i].Caps) == 0 {
				info[i].Caps = meta.Caps
			}
		}
	}
	p.cacheMu.Lock()
	p.modelInfo[prov.ID] = info
	p.cacheAt[prov.ID] = time.Now()
	p.cacheMu.Unlock()
	return info
}

// fetchModels queries an upstream OpenAI-compatible /models endpoint and
// returns the model ids.
func fetchModels(prov Provider) ([]string, error) {
	infos, err := fetchModelInfo(prov)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(infos))
	for _, m := range infos {
		if m.ID != "" {
			ids = append(ids, m.ID)
		}
	}
	return ids, nil
}

// fetchModelInfo queries an upstream OpenAI-compatible /models endpoint and
// returns the raw model entries (id, object, created, owned_by).
func fetchModelInfo(prov Provider) ([]ModelInfo, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	req, err := http.NewRequest(http.MethodGet, prov.BaseURL+"/models", nil)
	if err != nil {
		return nil, err
	}
	if prov.APIKey != "" {
		req.Header.Set("Authorization", "Bearer "+prov.APIKey)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("models: upstream HTTP %d", resp.StatusCode)
	}
	var out struct {
		Data []ModelInfo `json:"data"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return nil, fmt.Errorf("models: decode: %w", err)
	}
	var infos []ModelInfo
	for _, m := range out.Data {
		if m.ID != "" {
			infos = append(infos, m)
		}
	}
	// Apply the provider's model filter (model_filter in model-proxy.yaml):
	// "" / "all" pass everything through; "free-only" keeps only free-tier
	// models; anything else is a comma-separated explicit allowlist. Applied
	// here so proxy serving, config generation and `model-proxy models` all
	// see the same filtered listing.
	return applyModelFilter(prov, infos), nil
}

// handleModels serves GET /v1/models — the consolidated list of all upstream
// models, enriched with display metadata (label, context, caps) from the
// opencode catalog. The listing is queried straight from the upstream /models
// endpoints (never from models.yaml). With ?provider=<id> only that provider's
// models are returned (used by opencode-config generation to enumerate one
// backend's catalog).
func (p *Proxy) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}
	providerID := r.URL.Query().Get("provider")
	data := []ModelInfo{}
	for _, prov := range p.cfg.Providers {
		if providerID != "" && prov.ID != providerID {
			continue
		}
		// Apply the provider's model filter to the models this proxy serves.
		filtered := applyModelFilter(prov, p.providerModelInfo(prov))
		for _, m := range filtered {
			m.OwnedBy = prov.ID
			data = append(data, m)
		}
	}
	writeJSON(w, map[string]any{"object": "list", "data": data})
}

// transientStatus reports whether an HTTP status from the upstream is a
// transient failure worth retrying.
func transientStatus(code int) bool {
	switch code {
	case http.StatusRequestTimeout, http.StatusTooManyRequests,
		http.StatusInternalServerError, http.StatusBadGateway,
		http.StatusServiceUnavailable, http.StatusGatewayTimeout:
		return true
	}
	return false
}

// retryableError reports whether a transport-level error is transient.
func retryableError(err error) bool {
	if err == nil {
		return false
	}
	s := strings.ToLower(err.Error())
	for _, frag := range []string{
		"connection reset", "connection refused", "connection closed",
		"unexpected eof", "broken pipe", "econnreset", "econnrefused",
		"timeout", "temporary", "transient", "upstream request timeout",
		"server gave http response to https client",
	} {
		if strings.Contains(s, frag) {
			return true
		}
	}
	return false
}

// retryableErrorText reports whether an upstream error message describes a
// transient saturation/overload condition worth retrying even when the HTTP
// status alone wouldn't classify it as transient. This catches gRPC-style
// errors (e.g. NIM's "ResourceExhausted: Worker local total request limit
// reached") that some backends deliver with a generic status or — in streaming
// mode — as a 200 SSE error event instead of a 4xx/5xx response. Such
// saturation errors must be absorbed here in the proxy so they never reach the
// client; the worker stays saturated only for a short while and a plain retry
// succeeds.
func retryableErrorText(s string) bool {
	l := strings.ToLower(s)
	for _, frag := range []string{
		"resourceexhausted", "resource exhausted",
		"rate limit", "too many requests", "overloaded", "overload",
		"worker busy", "worker saturated", "capacity exceeded",
	} {
		if strings.Contains(l, frag) {
			return true
		}
	}
	return false
}

// isStreamingRequest reports whether the chat request asked for a stream.
func isStreamingRequest(body []byte) bool {
	var req struct {
		Stream bool `json:"stream"`
	}
	_ = json.Unmarshal(body, &req)
	return req.Stream
}

// routeModel picks the provider serving the requested model ID. Exact model
// catalog match wins; a "<provider>/<model>" prefixed id is also honored.
func (p *Proxy) routeModel(model string) (*Provider, string) {
	for i := range p.cfg.Providers {
		prov := &p.cfg.Providers[i]
		if p.providerModelSet(*prov)[model] {
			return prov, model
		}
	}
	// Fallback: "<providerID>/<model>" prefix routing.
	if idx := strings.IndexByte(model, '/'); idx > 0 {
		prefix := model[:idx]
		for i := range p.cfg.Providers {
			prov := &p.cfg.Providers[i]
			if prov.ID == prefix {
				return prov, model[idx+1:]
			}
		}
	}
	return nil, model
}

// handleShips serves GET /v1/ships — the per-ship usage/status statistics.
func (p *Proxy) handleShips(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}
	writeJSON(w, map[string]any{"ships": p.tracker.snapshot()})
}

// handleChat serves POST /v1/chat/completions. It routes to the provider that
// serves the requested model, retries transient upstream failures (incl.
// gRPC-style saturation errors such as ResourceExhausted, which are absorbed
// here and never reach the client), and pipes the SSE stream through (catching
// mid-stream breaks with a clean error event).
func (p *Proxy) handleChat(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		writeErr(w, 400, "read body: "+err.Error())
		return
	}
	var req struct {
		Model string `json:"model"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		writeErr(w, 400, "invalid json: "+err.Error())
		return
	}
	prov, upstreamModel := p.routeModel(req.Model)
	if prov == nil {
		writeErr(w, 400, fmt.Sprintf("model %q not served by any configured model-proxy provider", req.Model))
		return
	}
	ship := shipFromRequest(r.Header.Get("Authorization"))
	p.forwardChat(w, r, prov, upstreamModel, body, isStreamingRequest(body), ship, req.Model)
}

// forwardChat performs the (possibly retried) upstream chat request and
// records the outcome in the per-ship tracker.
func (p *Proxy) forwardChat(w http.ResponseWriter, r *http.Request, prov *Provider, model string, body []byte, streaming bool, ship, requestedModel string) {
	client := &http.Client{Timeout: 0} // streaming needs no client-side deadline; server read deadline governs
	attempts := prov.MaxRetries + 1

	buildReq := func() (*http.Request, io.Reader, error) {
		payload := body
		if model != "" {
			// Re-write the model field to the upstream model id (identity for
			// catalog matches; strips any "<provider>/" prefix in fallback mode).
			var m map[string]any
			if err := json.Unmarshal(body, &m); err != nil {
				return nil, nil, err
			}
			m["model"] = model
			raw, err := json.Marshal(m)
			if err != nil {
				return nil, nil, err
			}
			payload = raw
		}
		upstreamReq, err := http.NewRequestWithContext(r.Context(), http.MethodPost, prov.BaseURL+"/chat/completions", bytes.NewReader(payload))
		if err != nil {
			return nil, nil, err
		}
		upstreamReq.Header.Set("Content-Type", "application/json")
		if prov.APIKey != "" {
			upstreamReq.Header.Set("Authorization", "Bearer "+prov.APIKey)
		}
		// Stamp any provider-configured headers (e.g. a User-Agent override for
		// upstreams like OpenCode Zen that validate the client to grant
		// anonymous/free capacity). Passes the original client request so
		// client-supplied identity (session id) can be forwarded to Zen.
		prov.applyUpstreamHeaders(upstreamReq, r)
		return upstreamReq, bytes.NewReader(payload), nil
	}

	var lastErr error
	retries := 0
	writeHeaders := true
	skipSaturationWait := false // skip waitForSaturationGate after recording saturation for retry
	for attempt := 1; attempt <= attempts; attempt++ {
		// Wait for saturation gate (global cooldown per provider), unless we just
		// recorded saturation and are about to retry (we already slept RetryDelayMS)
		if !skipSaturationWait {
			if !p.waitForSaturationGate(r.Context(), prov.ID) {
				p.tracker.record(ship, prov.ID, requestedModel, nil, retries, true)
				writeErr(w, 503, fmt.Sprintf("provider %s saturated, request cancelled", prov.ID))
				return
			}
		}
		skipSaturationWait = false // reset for next iteration

		upstreamReq, _, err := buildReq()
		if err != nil {
			writeErr(w, 400, "build upstream request: "+err.Error())
			return
		}
		resp, err := client.Do(upstreamReq)
		if err != nil {
			lastErr = err
			if attempt < attempts && retryableError(err) {
				p.logf("%s/%s: transport error (attempt %d/%d): %v — retrying", ship, prov.ID, attempt, attempts, err)
				retries++
				time.Sleep(time.Duration(prov.RetryDelayMS) * time.Millisecond)
				continue
			}
			p.tracker.record(ship, prov.ID, requestedModel, nil, retries, true)
			writeErr(w, 502, fmt.Sprintf("upstream %s: %v", prov.ID, err))
			return
		}

		if resp.StatusCode >= 400 {
			// Read the error body once for logging; retry transient statuses
			// and gRPC-style saturation errors (e.g. ResourceExhausted) that
			// some backends return with a generic status.
			errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			isSaturation := resp.StatusCode == http.StatusTooManyRequests || retryableErrorText(string(errBody))
			if attempt < attempts && (transientStatus(resp.StatusCode) || isSaturation) {
				if isSaturation {
					p.recordSaturation(prov, resp)
					skipSaturationWait = true // we're about to sleep RetryDelayMS, skip next waitForSaturationGate
				}
				p.logf("%s/%s: transient HTTP %d (attempt %d/%d): %.300s — retrying", ship, prov.ID, resp.StatusCode, attempt, attempts, string(errBody))
				retries++
				time.Sleep(time.Duration(prov.RetryDelayMS) * time.Millisecond)
				continue
			}
			if isSaturation {
				p.recordSaturation(prov, resp)
			}
			p.tracker.record(ship, prov.ID, requestedModel, nil, retries, true)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(resp.StatusCode)
			if len(errBody) > 0 {
				_, _ = w.Write(errBody)
			} else {
				writeJSON(w, map[string]any{"error": map[string]any{"message": fmt.Sprintf("upstream %s returned HTTP %d", prov.ID, resp.StatusCode)}})
			}
			return
		}

		// Success — clear saturation state
		p.clearSaturation(prov.ID)

		if streaming {
			usage, failed, retryEarly := p.pipeSSE(w, resp, writeHeaders, attempt < attempts, prov, ship)
			if retryEarly {
				p.logf("%s/%s: retryable streamed error before content (attempt %d/%d) — retrying", ship, prov.ID, attempt, attempts)
				retries++
				writeHeaders = false
				time.Sleep(time.Duration(prov.RetryDelayMS) * time.Millisecond)
				continue
			}
			p.tracker.record(ship, prov.ID, requestedModel, usage, retries, failed)
		} else {
			bodyBytes, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				p.tracker.record(ship, prov.ID, requestedModel, nil, retries, true)
				writeErr(w, 502, fmt.Sprintf("upstream %s: read response: %v", prov.ID, err))
				return
			}
			usage := extractUsage(bodyBytes)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(bodyBytes)
			p.tracker.record(ship, prov.ID, requestedModel, usage, retries, false)
		}
		return
	}
	p.tracker.record(ship, prov.ID, requestedModel, nil, retries, true)
	writeErr(w, 502, fmt.Sprintf("upstream %s: %v", prov.ID, lastErr))
}

// extractUsage parses the usage object from a non-stream chat response body.
func extractUsage(body []byte) *Usage {
	var parsed struct {
		Usage Usage `json:"usage"`
	}
	if err := json.Unmarshal(body, &parsed); err != nil {
		return nil
	}
	if parsed.Usage.PromptTokens == 0 && parsed.Usage.CompletionTokens == 0 {
		return nil
	}
	return &parsed.Usage
}

// extractStreamUsage parses the usage object from a single SSE data payload.
func extractStreamUsage(payload []byte) *Usage {
	return extractUsage(payload)
}

// waitForSaturationGate blocks until the provider's saturation cooldown
// expires. Returns true if the caller should proceed, false if the context
// was cancelled.
func (p *Proxy) waitForSaturationGate(ctx context.Context, provID string) bool {
	p.satMu.Lock()
	st := p.sat[provID]
	p.satMu.Unlock()
	if st == nil {
		return true
	}
	wait := time.Until(st.cooldownUntil)
	if wait <= 0 {
		return true
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

// recordSaturation updates the provider's saturation state based on the
// upstream response (Retry-After header, or default backoff).
func (p *Proxy) recordSaturation(prov *Provider, resp *http.Response) {
	retryAfter := parseRetryAfter(resp)
	if retryAfter <= 0 {
		retryAfter = time.Duration(prov.RetryDelayMS) * time.Millisecond
	}
	cooldownUntil := time.Now().Add(retryAfter)

	p.satMu.Lock()
	p.sat[prov.ID] = &saturationState{
		providerID:    prov.ID,
		cooldownUntil: cooldownUntil,
		retryAfter:    retryAfter,
	}
	p.satMu.Unlock()
	p.logf("provider %s saturated, cooldown until %v (retry-after=%v)", prov.ID, cooldownUntil, retryAfter)
}

// parseRetryAfter extracts the Retry-After header value in seconds or HTTP-date.
func parseRetryAfter(resp *http.Response) time.Duration {
	h := resp.Header.Get("Retry-After")
	if h == "" {
		return 0
	}
	// Try parsing as seconds (integer)
	if secs, err := strconv.Atoi(h); err == nil {
		return time.Duration(secs) * time.Second
	}
	// Try parsing as HTTP-date
	if t, err := http.ParseTime(h); err == nil {
		return time.Until(t)
	}
	return 0
}

// clearSaturation clears the saturation state for a provider on success.
func (p *Proxy) clearSaturation(provID string) {
	p.satMu.Lock()
	delete(p.sat, provID)
	p.satMu.Unlock()
}

// pipeSSE copies an SSE stream from the upstream response to the client,
// forwarding headers and catching a premature close (EOF without a [DONE]
// sentinel) with a clean error event so the client sees a structured failure
// instead of a truncated stream. It returns the accumulated token usage (from
// trailing usage chunks), whether the stream failed, and whether it aborted
// early with a retryable error.
//
// When writeHeaders is false the caller already committed the response
// headers (a previous attempt's 200) and the stream is relayed without
// re-writing them.
//
// A saturation/overload error (e.g. NIM "ResourceExhausted: Worker local total
// request limit reached") that arrives as a streamed error event BEFORE any
// content chunk is NOT relayed: with canRetry set the attempt is aborted
// (retryEarly=true, nothing written to the client besides the already-committed
// headers) so the caller can re-dial the request — the worker stays saturated
// only briefly and the retry succeeds. Such errors must never reach the client,
// which would otherwise treat them as hard failures. Mid-stream errors (after
// content) and errors once the retry budget is exhausted are passed through as
// structured error events.
//
// When the retry budget is exhausted (canRetry=false) and a saturation error
// arrives in the stream, the proxy enters a keepalive hold: it sends SSE
// comment lines (": keepalive\n\n") every 3s to keep the connection alive
// while waiting for the provider to recover, up to HoldTimeoutMS (default 15s).
// After the timeout, the final error is relayed to the client.
func (p *Proxy) pipeSSE(w http.ResponseWriter, resp *http.Response, writeHeaders, canRetry bool, prov *Provider, ship string) (*Usage, bool, bool) {
	defer resp.Body.Close()
	if writeHeaders {
		for k, vv := range resp.Header {
			for _, v := range vv {
				w.Header().Add(k, v)
			}
		}
		w.WriteHeader(resp.StatusCode)
	}

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	sawDone := false
	sawError := false
	sawContent := false
	flusher, _ := w.(http.Flusher)
	var usage *Usage
	var saturationErrorPayload string
	inKeepalive := false
	keepaliveStart := time.Time{}

	// Channel for scanner results
	type scanResult struct {
		line string
		err  error
	}
	scanCh := make(chan scanResult, 1)
	go func() {
		for sc.Scan() {
			scanCh <- scanResult{line: sc.Text(), err: nil}
		}
		scanCh <- scanResult{line: "", err: sc.Err()}
		close(scanCh)
	}()

	// Keepalive ticker
	var keepaliveTicker *time.Ticker
	stopKeepalive := make(chan struct{})

	for {
		select {
		case result, ok := <-scanCh:
			if !ok {
				// Scanner finished
				if inKeepalive {
					p.logf("%s/%s: stream ended during keepalive hold, relaying saturation error", ship, prov.ID)
					evt := map[string]any{"error": map[string]any{
						"message": saturationErrorPayload,
						"code":    "rate_limit_exceeded",
					}}
					raw, _ := json.Marshal(evt)
					_, _ = io.WriteString(w, "\ndata: "+string(raw)+"\n\n")
					_, _ = io.WriteString(w, "data: [DONE]\n\n")
					if flusher != nil {
						flusher.Flush()
					}
					if keepaliveTicker != nil {
						keepaliveTicker.Stop()
					}
					return usage, true, false
				}
				if !sawDone && !sawError {
					// The upstream ended without the [DONE] sentinel and without a
					// structured error event — i.e. a truncated/aborted stream (conn
					// reset, overload kill, ...) or an empty 200. Emit a structured
					// error event so opencode's error handling can act on it rather than
					// silently continuing with half a response.
					p.logf("stream ended without [DONE] (err=%v) — emitting error event", result.err)
					evt := map[string]any{"error": map[string]any{
						"message": "model-proxy: upstream stream interrupted before [DONE]",
						"code":    "stream_interrupted",
					}}
					raw, _ := json.Marshal(evt)
					_, _ = io.WriteString(w, "\ndata: "+string(raw)+"\n\n")
					_, _ = io.WriteString(w, "data: [DONE]\n\n")
					if flusher != nil {
						flusher.Flush()
					}
					if keepaliveTicker != nil {
						keepaliveTicker.Stop()
					}
					return usage, true, false
				}
				if keepaliveTicker != nil {
					keepaliveTicker.Stop()
				}
				return usage, false, false
			}

			line := result.line
			trimmed := strings.TrimSpace(line)
			if strings.HasPrefix(trimmed, "data:") {
				payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
				if payload == "[DONE]" {
					sawDone = true
					// Stop keepalive if running
					if keepaliveTicker != nil {
						keepaliveTicker.Stop()
					}
					close(stopKeepalive)
				} else if strings.HasPrefix(payload, "{") && strings.Contains(payload, "\"error\"") {
					// A streamed error event. Before any content chunk a retryable
					// overload/rate-limit error is aborted for a clean retry;
					// everything else is passed through and remembered so we don't
					// append our own error on EOF.
					if !sawContent && canRetry && retryableErrorText(payload) {
						if keepaliveTicker != nil {
							keepaliveTicker.Stop()
						}
						return nil, true, true
					}
					// Saturation error after retry budget exhausted: enter keepalive hold
					if !sawContent && !canRetry && retryableErrorText(payload) {
						saturationErrorPayload = payload
						inKeepalive = true
						keepaliveStart = time.Now()
						// Start keepalive ticker
						keepaliveTicker = time.NewTicker(3 * time.Second)
						go func() {
							for {
								select {
								case <-keepaliveTicker.C:
									if !inKeepalive {
										return
									}
									elapsed := time.Since(keepaliveStart)
									if elapsed >= time.Duration(prov.HoldTimeoutMS)*time.Millisecond {
										// Keepalive timeout reached — emit the buffered saturation error
										p.logf("%s/%s: keepalive hold timeout (%dms) reached, relaying saturation error", ship, prov.ID, prov.HoldTimeoutMS)
										evt := map[string]any{"error": map[string]any{
											"message": saturationErrorPayload,
											"code":    "rate_limit_exceeded",
										}}
										raw, _ := json.Marshal(evt)
										_, _ = io.WriteString(w, "\ndata: "+string(raw)+"\n\n")
										_, _ = io.WriteString(w, "data: [DONE]\n\n")
										if flusher != nil {
											flusher.Flush()
										}
										inKeepalive = false
										sawDone = true
										sawError = true
										keepaliveTicker.Stop()
										close(stopKeepalive)
										return
									}
									// Send keepalive comment
									_, _ = io.WriteString(w, ": keepalive\n\n")
									if flusher != nil {
										flusher.Flush()
									}
								case <-stopKeepalive:
									keepaliveTicker.Stop()
									return
								}
							}
						}()
						// Don't write the error yet; start keepalive
						continue
					}
					sawError = true
					_, _ = io.WriteString(w, line+"\n\n")
					if flusher != nil {
						flusher.Flush()
					}
					continue
				} else if strings.HasPrefix(payload, "{") {
					if strings.Contains(payload, "\"choices\"") {
						sawContent = true
					}
					// Upstreams (notably NIM) repeat the usage block on several
					// trailing chunks with a growing counter — the last one holds
					// the final values, so last-wins instead of accumulating.
					if u := extractStreamUsage([]byte(payload)); u != nil {
						usage = u
					}
				}
			}
			_, _ = io.WriteString(w, line+"\n")
			if strings.TrimSpace(line) == "" {
				if flusher != nil {
					flusher.Flush()
				}
			}
		case <-stopKeepalive:
			if keepaliveTicker != nil {
				keepaliveTicker.Stop()
			}
			// Continue to drain scanner if needed
			continue
		}
	}
}

func writeJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(v)
}

func writeErr(w http.ResponseWriter, code int, msg string) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]any{"message": msg}})
}
