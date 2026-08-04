// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package modelproxy

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
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
	return infos, nil
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
		for _, m := range p.providerModelInfo(prov) {
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
// serves the requested model, retries transient upstream failures, and pipes
// the SSE stream through (catching mid-stream breaks with a clean error event).
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
		return upstreamReq, bytes.NewReader(payload), nil
	}

	var lastErr error
	retries := 0
	for attempt := 1; attempt <= attempts; attempt++ {
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
			// Read the error body once for logging; retry transient statuses.
			errBody, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
			resp.Body.Close()
			if transientStatus(resp.StatusCode) && attempt < attempts {
				p.logf("%s/%s: transient HTTP %d (attempt %d/%d): %.300s — retrying", ship, prov.ID, resp.StatusCode, attempt, attempts, string(errBody))
				retries++
				time.Sleep(time.Duration(prov.RetryDelayMS) * time.Millisecond)
				continue
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

		if streaming {
			usage, failed := p.pipeSSE(w, resp)
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

// pipeSSE copies an SSE stream from the upstream response to the client,
// forwarding headers and catching a premature close (EOF without a [DONE]
// sentinel) with a clean error event so the client sees a structured failure
// instead of a truncated stream. It returns the accumulated token usage (from
// trailing usage chunks) and whether the stream failed.
func (p *Proxy) pipeSSE(w http.ResponseWriter, resp *http.Response) (*Usage, bool) {
	defer resp.Body.Close()
	for k, vv := range resp.Header {
		for _, v := range vv {
			w.Header().Add(k, v)
		}
	}
	w.WriteHeader(resp.StatusCode)

	sc := bufio.NewScanner(resp.Body)
	sc.Buffer(make([]byte, 64*1024), 16*1024*1024)
	sawDone := false
	sawError := false
	flusher, _ := w.(http.Flusher)
	var usage *Usage

	for sc.Scan() {
		line := sc.Text()
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "data:") {
			payload := strings.TrimSpace(strings.TrimPrefix(trimmed, "data:"))
			if payload == "[DONE]" {
				sawDone = true
			} else if strings.HasPrefix(payload, "{") && strings.Contains(payload, "\"error\"") {
				// Upstream streamed a structured error event — pass it through
				// and remember we saw one (so we don't append our own).
				sawError = true
				_, _ = io.WriteString(w, line+"\n\n")
				if flusher != nil {
					flusher.Flush()
				}
				continue
			} else if strings.HasPrefix(payload, "{") {
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
	}

	if !sawDone && !sawError {
		// The upstream ended without the [DONE] sentinel and without a
		// structured error event — i.e. a truncated/aborted stream (conn
		// reset, overload kill, ...) or an empty 200. Emit a structured
		// error event so opencode's error handling can act on it rather than
		// silently continuing with half a response.
		p.logf("stream ended without [DONE] (err=%v) — emitting error event", sc.Err())
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
		return usage, true
	}
	return usage, false
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
