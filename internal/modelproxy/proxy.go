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
	"math/rand"
	"net/http"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/metux/starfleetctl/internal/config"
)

// ModelInfo is one model entry served by GET /v1/models. The upstream fields
// (id, object, created, owned_by) are taken verbatim from the backend's
// /models response; Label/Context/Caps are enriched from the opencode model
// catalog (the upstreams themselves only expose the bare OpenAI fields).
type ModelInfo struct {
	ID            string   `json:"id"`
	Object        string   `json:"object,omitempty"`
	Created       int64    `json:"created,omitempty"`
	OwnedBy       string   `json:"owned_by,omitempty"`
	Label         string   `json:"label,omitempty"`
	Context       int      `json:"context,omitempty"`
	Output        int      `json:"output,omitempty"`
	Caps          []string `json:"caps,omitempty"`
	ContextWindow int      `json:"context_window,omitempty"` // effective context window for strategy
}

// StrategyState tracks the runtime state of a strategy.
type StrategyState struct {
	Name            string
	Type            config.ModelProxyStrategyType
	EffectiveLimit  int
	Models          []config.ModelProxyStrategyModel
	CurrentIndex    int                             // for round-robin
	StickyModel     string                          // for weighted sticky: chosen model
	StickyChosenAt  time.Time                       // when sticky model was chosen
	CircuitBreakers map[string]*CircuitBreakerState // per-model circuit breaker
	ModelMetrics    map[string]*ModelMetrics        // per-model metrics
	mu              sync.Mutex
}

// CircuitBreakerStateEnum represents the circuit breaker state.
type CircuitBreakerStateEnum int

const (
	CircuitBreakerClosed CircuitBreakerStateEnum = iota
	CircuitBreakerOpen
	CircuitBreakerHalfOpen
)

// circuitBreakerCooldown is how long an Open circuit breaker stays out of
// rotation before it is given a HalfOpen probe. It must be enforced from the
// routing path as well (see breakerStateForRoutingLocked): a model whose
// breaker is Open is skipped by every strategy, so checkCircuitBreaker would
// otherwise never run for it and the breaker could never recover.
const circuitBreakerCooldown = 5 * time.Minute

// CircuitBreakerState represents the circuit breaker for a model.
type CircuitBreakerState struct {
	State           CircuitBreakerStateEnum
	FailureCount    int
	SuccessCount    int
	LastFailure     time.Time
	LastStateChange time.Time
	mu              sync.Mutex
}

// ModelMetrics tracks metrics for a model.
type ModelMetrics struct {
	RequestCount        int64
	ErrorCount          int64
	TotalLatency        time.Duration
	TotalTokens         int64
	ConsecutiveFailures int
	LastRequest         time.Time
	mu                  sync.Mutex
}

// SessionAffinity maps session ID to chosen model for sticky routing.
type SessionAffinity struct {
	mu      sync.Mutex
	mapping map[string]string // sessionID -> modelID
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

	// Strategy routing
	strategyMu      sync.RWMutex
	strategies      map[string]*StrategyState
	sessionAffinity *SessionAffinity
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
		cfg:             cfg,
		logger:          log.Default(),
		modelSets:       map[string]map[string]bool{},
		modelInfo:       map[string][]ModelInfo{},
		cacheAt:         map[string]time.Time{},
		tracker:         newShipTracker(),
		sat:             map[string]*saturationState{},
		strategies:      map[string]*StrategyState{},
		sessionAffinity: &SessionAffinity{mapping: make(map[string]string)},
	}
	p.mux = http.NewServeMux()
	p.mux.HandleFunc("/v1/models", p.handleModels)
	p.mux.HandleFunc("/v1/chat/completions", p.handleChat)
	p.mux.HandleFunc("/v1/ships", p.handleShips)
	p.mux.HandleFunc("/healthz", p.handleHealth)
	p.mux.HandleFunc("/v1/meta-models", p.handleMetaModels)
	p.mux.HandleFunc("/v1/meta-models/", p.handleMetaModelDispatch)
	p.mux.HandleFunc("/v1/meta-models/sessions", p.handleMetaModelSessions)
	p.mux.HandleFunc("/v1/meta-models/switch", p.handleMetaModelSwitch)
	p.mux.HandleFunc("/v1/meta-models/force", p.handleMetaModelForce)
	// Initialize strategy state
	p.initStrategies()
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

// initStrategies initializes the strategy state from config.
func (p *Proxy) initStrategies() {
	p.strategyMu.Lock()
	defer p.strategyMu.Unlock()
	for _, strat := range p.cfg.Strategies {
		ss := &StrategyState{
			Name:            strat.ID,
			Type:            config.ModelProxyStrategyType(strat.Strategy),
			EffectiveLimit:  strat.EffectiveLimit,
			Models:          strat.Models,
			CircuitBreakers: make(map[string]*CircuitBreakerState),
			ModelMetrics:    make(map[string]*ModelMetrics),
		}
		for _, m := range strat.Models {
			ss.CircuitBreakers[m.ID] = &CircuitBreakerState{
				State:           CircuitBreakerClosed,
				LastStateChange: time.Now(),
			}
			ss.ModelMetrics[m.ID] = &ModelMetrics{}
		}
		p.strategies[strat.ID] = ss
		p.logf("initialized strategy %s (type=%s, effective_limit=%d, models=%d)", strat.ID, strat.Strategy, strat.EffectiveLimit, len(strat.Models))
	}
}

// getStrategy returns the StrategyState for a given strategy ID.
func (p *Proxy) getStrategy(id string) *StrategyState {
	p.strategyMu.RLock()
	defer p.strategyMu.RUnlock()
	return p.strategies[id]
}

// getStrategyForModel finds the strategy that serves a given model. Per the
// meta-model architecture, a strategy is addressed by its own ID as a virtual
// model endpoint (e.g. "heavy-model" or "meta-model/heavy-model") — the real
// upstream models inside a strategy are never matched here, so requesting a
// real model (e.g. "big-pickle") always routes directly to its upstream and
// is never hijacked into a strategy.
func (p *Proxy) getStrategyForModel(model string) *StrategyState {
	// Accept an optional "<virtual-provider>/<strategy-id>" form so clients can
	// address strategies through the meta-model provider explicitly.
	if idx := strings.IndexByte(model, '/'); idx > 0 {
		for _, prov := range p.cfg.Providers {
			if prov.isVirtual() && prov.ID == model[:idx] {
				model = model[idx+1:]
				break
			}
		}
	}
	p.strategyMu.RLock()
	defer p.strategyMu.RUnlock()
	return p.strategies[model]
}

// routeStrategyModel picks the model to use for a request within a strategy.
// breakerStateForRoutingLocked returns the circuit-breaker state to use for a
// routing decision. An Open breaker whose cooldown has expired is advanced to
// HalfOpen here — making it selectable as a probe — and its measurement window
// is reset, so a model that has recovered can actually be tried again and
// closed. Without this, routeStrategyModel skips Open models forever and
// checkCircuitBreaker never runs for them, wedging the breaker until restart.
//
// The caller must hold ss.mu; this only locks the breaker and metrics.
func (p *Proxy) breakerStateForRoutingLocked(ss *StrategyState, modelID string) CircuitBreakerStateEnum {
	cb := ss.CircuitBreakers[modelID]
	if cb == nil {
		return CircuitBreakerClosed
	}
	cb.mu.Lock()
	state := cb.State
	if state == CircuitBreakerOpen && time.Since(cb.LastStateChange) > circuitBreakerCooldown {
		cb.State = CircuitBreakerHalfOpen
		cb.LastStateChange = time.Now()
		state = CircuitBreakerHalfOpen
		if metrics := ss.ModelMetrics[modelID]; metrics != nil {
			metrics.mu.Lock()
			metrics.RequestCount = 0
			metrics.ErrorCount = 0
			metrics.TotalLatency = 0
			metrics.TotalTokens = 0
			metrics.ConsecutiveFailures = 0
			metrics.mu.Unlock()
		}
		p.logf("circuit breaker HALF-OPEN for model %s in strategy %s (cooldown expired — probing)", modelID, ss.Name)
	}
	cb.mu.Unlock()
	return state
}

func (p *Proxy) routeStrategyModel(ss *StrategyState, sessionID, requestedModel string) (string, *Provider, error) {
	ss.mu.Lock()
	defer ss.mu.Unlock()

	if len(ss.Models) == 0 {
		return "", nil, fmt.Errorf("strategy %s has no models", ss.Name)
	}

	// Check if we have a sticky model for this session
	if ss.Type == config.ModelProxyStrategyTypeWeighted && ss.StickyModel != "" && sessionID != "" {
		if p.sessionAffinity != nil {
			p.sessionAffinity.mu.Lock()
			chosen, ok := p.sessionAffinity.mapping[sessionID]
			p.sessionAffinity.mu.Unlock()
			if ok && chosen != "" {
				// Keep the sticky model unless its breaker is Open (HalfOpen is
				// allowed, so a recovered model can be reused directly).
				if p.breakerStateForRoutingLocked(ss, chosen) != CircuitBreakerOpen {
					prov := ss.GetProviderForModel(chosen, p.cfg.Providers)
					if prov != nil {
						return chosen, prov, nil
					}
				}
			}
		}
	}

	var chosenModel string
	var chosenProv *Provider

	switch ss.Type {
	case config.ModelProxyStrategyTypeSingle:
		chosenModel = ss.Models[0].ID
		chosenProv = ss.GetProviderForModel(chosenModel, p.cfg.Providers)
	case config.ModelProxyStrategyTypeFallback:
		// Sort by priority (lower = first)
		for _, m := range ss.Models {
			if p.breakerStateForRoutingLocked(ss, m.ID) == CircuitBreakerOpen {
				continue // skip open circuit breakers (HalfOpen probes are allowed)
			}
			chosenModel = m.ID
			chosenProv = ss.GetProviderForModel(chosenModel, p.cfg.Providers)
			if chosenProv != nil {
				break
			}
		}
	case config.ModelProxyStrategyTypeRoundRobin:
		// Find next available model
		for i := 0; i < len(ss.Models); i++ {
			idx := (ss.CurrentIndex + i) % len(ss.Models)
			m := ss.Models[idx]
			if p.breakerStateForRoutingLocked(ss, m.ID) == CircuitBreakerOpen {
				continue
			}
			chosenModel = m.ID
			chosenProv = ss.GetProviderForModel(chosenModel, p.cfg.Providers)
			if chosenProv != nil {
				ss.CurrentIndex = (idx + 1) % len(ss.Models)
				break
			}
		}
	case config.ModelProxyStrategyTypeWeighted:
		// Sticky selection: choose a model weighted by weight, then stick to it
		if ss.StickyModel != "" && sessionID != "" {
			if p.sessionAffinity != nil {
				p.sessionAffinity.mu.Lock()
				chosen, ok := p.sessionAffinity.mapping[sessionID]
				p.sessionAffinity.mu.Unlock()
				if ok && chosen != "" {
					if p.breakerStateForRoutingLocked(ss, chosen) != CircuitBreakerOpen {
						chosenModel = chosen
						chosenProv = ss.GetProviderForModel(chosenModel, p.cfg.Providers)
					}
				}
			}
		}

		// If no sticky model or circuit breaker open, pick a new one
		if chosenModel == "" || chosenProv == nil {
			// Build weighted list of available models
			type weightedModel struct {
				model  string
				weight int
			}
			var available []weightedModel
			totalWeight := 0
			for _, m := range ss.Models {
				if p.breakerStateForRoutingLocked(ss, m.ID) == CircuitBreakerOpen {
					continue
				}
				prov := ss.GetProviderForModel(m.ID, p.cfg.Providers)
				if prov != nil {
					w := m.Weight
					if w <= 0 {
						w = 1
					}
					available = append(available, weightedModel{m.ID, w})
					totalWeight += w
				}
			}
			if len(available) == 0 {
				return "", nil, fmt.Errorf("no available models in strategy %s (all circuit breakers open)", ss.Name)
			}
			// Weighted random selection
			r := rand.Intn(totalWeight)
			accum := 0
			for _, am := range available {
				accum += am.weight
				if r < accum {
					chosenModel = am.model
					chosenProv = ss.GetProviderForModel(chosenModel, p.cfg.Providers)
					break
				}
			}
			// Set sticky model
			ss.StickyModel = chosenModel
			ss.StickyChosenAt = time.Now()
			if sessionID != "" && p.sessionAffinity != nil {
				p.sessionAffinity.mu.Lock()
				p.sessionAffinity.mapping[sessionID] = chosenModel
				p.sessionAffinity.mu.Unlock()
			}
		}
	}

	if chosenModel == "" || chosenProv == nil {
		return "", nil, fmt.Errorf("no available models in strategy %s", ss.Name)
	}

	return chosenModel, chosenProv, nil
}

// GetProviderForModel returns the Provider for a model in this strategy.
func (ss *StrategyState) GetProviderForModel(modelID string, providers []Provider) *Provider {
	for _, m := range ss.Models {
		if m.ID == modelID {
			for i := range providers {
				if providers[i].ID == m.Provider {
					return &providers[i]
				}
			}
		}
	}
	return nil
}

// recordStrategyMetrics records metrics for a model in a strategy.
func (p *Proxy) recordStrategyMetrics(ss *StrategyState, modelID string, latency time.Duration, tokens int, err error) {
	ss.mu.Lock()
	metrics := ss.ModelMetrics[modelID]
	ss.mu.Unlock()

	if metrics == nil {
		return
	}

	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	metrics.RequestCount++
	metrics.TotalLatency += latency
	metrics.TotalTokens += int64(tokens)
	metrics.LastRequest = time.Now()
	if err != nil {
		metrics.ErrorCount++
		metrics.ConsecutiveFailures++
	} else {
		metrics.ConsecutiveFailures = 0
	}
}

// checkCircuitBreaker checks and updates circuit breaker state based on heuristics.
func (p *Proxy) checkCircuitBreaker(ss *StrategyState, modelID string) {
	ss.mu.Lock()
	cb := ss.CircuitBreakers[modelID]
	metrics := ss.ModelMetrics[modelID]
	ss.mu.Unlock()

	if cb == nil || metrics == nil {
		return
	}

	cb.mu.Lock()
	defer cb.mu.Unlock()
	metrics.mu.Lock()
	defer metrics.mu.Unlock()

	// Probation: a HalfOpen breaker is judged only on the requests made since
	// it was re-probed (its window is reset on the Open→HalfOpen transition).
	// This must run before the lifetime heuristics below, otherwise a model
	// that had accumulated a high error rate could never close again.
	if cb.State == CircuitBreakerHalfOpen {
		if metrics.ConsecutiveFailures == 0 && metrics.RequestCount > 0 {
			p.logf("circuit breaker CLOSED for model %s in strategy %s (recovery)", modelID, ss.Name)
			cb.State = CircuitBreakerClosed
			cb.LastStateChange = time.Now()
		} else if metrics.ConsecutiveFailures >= 3 {
			p.logf("circuit breaker OPEN for model %s in strategy %s (half-open probe failed)", modelID, ss.Name)
			cb.State = CircuitBreakerOpen
			cb.LastStateChange = time.Now()
		}
		return
	}

	// Check consecutive failures threshold
	if metrics.ConsecutiveFailures >= 3 {
		if cb.State == CircuitBreakerClosed {
			p.logf("circuit breaker OPEN for model %s in strategy %s (consecutive failures: %d)", modelID, ss.Name, metrics.ConsecutiveFailures)
			cb.State = CircuitBreakerOpen
			cb.LastStateChange = time.Now()
		}
		return
	}

	// Check error rate threshold (20% in 5 minutes)
	if metrics.RequestCount > 10 {
		errorRate := float64(metrics.ErrorCount) / float64(metrics.RequestCount)
		if errorRate > 0.20 {
			if cb.State == CircuitBreakerClosed {
				p.logf("circuit breaker OPEN for model %s in strategy %s (error rate: %.2f%%)", modelID, ss.Name, errorRate*100)
				cb.State = CircuitBreakerOpen
				cb.LastStateChange = time.Now()
			}
			return
		}
	}

	// Check latency threshold (p95 > 30s)
	if metrics.RequestCount > 5 {
		avgLatency := metrics.TotalLatency / time.Duration(metrics.RequestCount)
		if avgLatency > 30*time.Second {
			if cb.State == CircuitBreakerClosed {
				p.logf("circuit breaker OPEN for model %s in strategy %s (avg latency: %v)", modelID, ss.Name, avgLatency)
				cb.State = CircuitBreakerOpen
				cb.LastStateChange = time.Now()
			}
			return
		}
	}

	// Check token throughput threshold (< 10 tokens/s)
	if metrics.RequestCount > 5 && metrics.TotalTokens > 0 {
		throughput := float64(metrics.TotalTokens) / metrics.TotalLatency.Seconds()
		if throughput < 10 {
			if cb.State == CircuitBreakerClosed {
				p.logf("circuit breaker OPEN for model %s in strategy %s (throughput: %.2f tokens/s)", modelID, ss.Name, throughput)
				cb.State = CircuitBreakerOpen
				cb.LastStateChange = time.Now()
			}
			return
		}
	}

	// Auto-recover: if open for more than the cooldown, go HalfOpen and reset
	// the measurement window so the probe is judged on fresh data. Routing also
	// performs this transition (breakerStateForRoutingLocked); this branch only
	// matters for a model that is not currently being selected.
	if cb.State == CircuitBreakerOpen && time.Since(cb.LastStateChange) > circuitBreakerCooldown {
		p.logf("circuit breaker HALF-OPEN for model %s in strategy %s (cooldown expired)", modelID, ss.Name)
		cb.State = CircuitBreakerHalfOpen
		cb.LastStateChange = time.Now()
		metrics.RequestCount = 0
		metrics.ErrorCount = 0
		metrics.TotalLatency = 0
		metrics.TotalTokens = 0
		metrics.ConsecutiveFailures = 0
	}
}
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
//
// A virtual meta-model provider contributes one model endpoint per strategy,
// named by the strategy ID (never the real upstream models inside a strategy —
// those stay listed under their own real provider).
func (p *Proxy) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}
	providerID := r.URL.Query().Get("provider")
	data := []ModelInfo{}

	// Add provider models
	for _, prov := range p.cfg.Providers {
		if providerID != "" && prov.ID != providerID {
			continue
		}
		if prov.isVirtual() {
			// Virtual meta-model provider: synthesize one model endpoint per
			// strategy, named by the strategy ID.
			for _, strat := range p.cfg.Strategies {
				data = append(data, ModelInfo{
					ID:            strat.ID,
					Object:        "model",
					OwnedBy:       prov.ID,
					Label:         strat.ID,
					ContextWindow: strat.EffectiveLimit,
				})
			}
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

// handleMetaModels handles GET /v1/meta-models — list all strategies.
func (p *Proxy) handleMetaModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}
	type StrategyResponse struct {
		ID             string                              `json:"id"`
		Description    string                              `json:"description"`
		DefaultModel   string                              `json:"default_model"`
		Models         []config.ModelProxyStrategyModel    `json:"models"`
		Type           string                              `json:"strategy"`
		EffectiveLimit int                                 `json:"effective_limit"`
		Triggers       config.ModelProxyStrategyTriggers   `json:"triggers"`
		Heuristics     config.ModelProxyStrategyHeuristics `json:"heuristics"`
	}
	var resp []StrategyResponse
	for _, s := range p.cfg.Strategies {
		resp = append(resp, StrategyResponse{
			ID:             s.ID,
			Description:    s.Description,
			DefaultModel:   s.DefaultModel,
			Models:         s.Models,
			Type:           s.Strategy,
			EffectiveLimit: s.EffectiveLimit,
			Triggers:       s.Triggers,
			Heuristics:     s.Heuristics,
		})
	}
	writeJSON(w, resp)
}

// handleMetaModelDispatch handles GET /v1/meta-models/<strategy> — get strategy details.
func (p *Proxy) handleMetaModelDispatch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}
	strategyID := strings.TrimPrefix(r.URL.Path, "/v1/meta-models/")
	if strategyID == "" {
		writeErr(w, 400, "strategy ID required")
		return
	}
	strat := p.getStrategy(strategyID)
	if strat == nil {
		writeErr(w, 404, "strategy not found")
		return
	}
	type StrategyDetailResponse struct {
		ID             string                              `json:"id"`
		Description    string                              `json:"description"`
		DefaultModel   string                              `json:"default_model"`
		Models         []config.ModelProxyStrategyModel    `json:"models"`
		Type           string                              `json:"strategy"`
		EffectiveLimit int                                 `json:"effective_limit"`
		Triggers       config.ModelProxyStrategyTriggers   `json:"triggers"`
		Heuristics     config.ModelProxyStrategyHeuristics `json:"heuristics"`
		// Runtime state
		CurrentIndex    int                    `json:"current_index"`
		StickyModel     string                 `json:"sticky_model"`
		CircuitBreakers map[string]string      `json:"circuit_breakers"`
		Metrics         map[string]interface{} `json:"metrics"`
	}
	cbStates := make(map[string]string)
	for modelID, cb := range strat.CircuitBreakers {
		cb.mu.Lock()
		cbStates[modelID] = fmt.Sprintf("%d", cb.State)
		cb.mu.Unlock()
	}
	metrics := make(map[string]interface{})
	for modelID, m := range strat.ModelMetrics {
		m.mu.Lock()
		metrics[modelID] = map[string]interface{}{
			"request_count":        m.RequestCount,
			"error_count":          m.ErrorCount,
			"avg_latency_ms":       float64(m.TotalLatency.Milliseconds()) / float64(max(1, m.RequestCount)),
			"total_tokens":         m.TotalTokens,
			"consecutive_failures": m.ConsecutiveFailures,
			"last_request":         m.LastRequest.Format(time.RFC3339),
		}
		m.mu.Unlock()
	}
	// Find the config for this strategy
	var stratConfig *config.ModelProxyStrategy
	for _, s := range p.cfg.Strategies {
		if s.ID == strategyID {
			stratConfig = &s
			break
		}
	}
	if stratConfig == nil {
		writeErr(w, 404, "strategy not found in config")
		return
	}

	resp := StrategyDetailResponse{
		ID:              strat.Name,
		Description:     stratConfig.Description,
		DefaultModel:    stratConfig.DefaultModel,
		Models:          strat.Models,
		Type:            stratConfig.Strategy,
		EffectiveLimit:  strat.EffectiveLimit,
		Triggers:        stratConfig.Triggers,
		Heuristics:      stratConfig.Heuristics,
		CurrentIndex:    strat.CurrentIndex,
		StickyModel:     strat.StickyModel,
		CircuitBreakers: cbStates,
		Metrics:         metrics,
	}
	writeJSON(w, resp)
}

// handleMetaModelSessions handles GET /v1/meta-models/sessions — list all sessions.
func (p *Proxy) handleMetaModelSessions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeErr(w, 405, "method not allowed")
		return
	}
	if p.sessionAffinity == nil {
		writeJSON(w, map[string]any{"sessions": []string{}})
		return
	}
	p.sessionAffinity.mu.Lock()
	sessions := make(map[string]string)
	for k, v := range p.sessionAffinity.mapping {
		sessions[k] = v
	}
	p.sessionAffinity.mu.Unlock()
	writeJSON(w, map[string]any{"sessions": sessions})
}

// handleMetaModelSwitch handles POST /v1/meta-models/switch — manually switch a session's model.
func (p *Proxy) handleMetaModelSwitch(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	var req struct {
		SessionID string `json:"session_id"`
		Strategy  string `json:"strategy"`
		Model     string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	if req.SessionID == "" || req.Strategy == "" || req.Model == "" {
		writeErr(w, 400, "session_id, strategy, and model are required")
		return
	}
	strat := p.getStrategy(req.Strategy)
	if strat == nil {
		writeErr(w, 404, "strategy not found")
		return
	}
	// Verify model is in strategy
	found := false
	for _, m := range strat.Models {
		if m.ID == req.Model {
			found = true
			break
		}
	}
	if !found {
		writeErr(w, 400, "model not in strategy")
		return
	}
	// Check circuit breaker
	strat.mu.Lock()
	cb := strat.CircuitBreakers[req.Model]
	strat.mu.Unlock()
	if cb != nil {
		cb.mu.Lock()
		if cb.State == CircuitBreakerOpen {
			cb.mu.Unlock()
			writeErr(w, 409, "model circuit breaker is open")
			return
		}
		cb.mu.Unlock()
	}
	// Set affinity
	if p.sessionAffinity != nil {
		p.sessionAffinity.mu.Lock()
		p.sessionAffinity.mapping[req.SessionID] = req.Model
		p.sessionAffinity.mu.Unlock()
	}
	// Update strategy sticky model
	strat.mu.Lock()
	strat.StickyModel = req.Model
	strat.StickyChosenAt = time.Now()
	strat.mu.Unlock()
	writeJSON(w, map[string]any{"ok": true, "session_id": req.SessionID, "model": req.Model})
}

// handleMetaModelForce handles POST /v1/meta-models/force — force all sessions to use a model.
func (p *Proxy) handleMetaModelForce(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeErr(w, 405, "method not allowed")
		return
	}
	var req struct {
		Strategy string `json:"strategy"`
		Model    string `json:"model"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, 400, "bad json: "+err.Error())
		return
	}
	if req.Strategy == "" || req.Model == "" {
		writeErr(w, 400, "strategy and model are required")
		return
	}
	strat := p.getStrategy(req.Strategy)
	if strat == nil {
		writeErr(w, 404, "strategy not found")
		return
	}
	// Verify model is in strategy
	found := false
	for _, m := range strat.Models {
		if m.ID == req.Model {
			found = true
			break
		}
	}
	if !found {
		writeErr(w, 400, "model not in strategy")
		return
	}
	// Check circuit breaker
	strat.mu.Lock()
	cb := strat.CircuitBreakers[req.Model]
	strat.mu.Unlock()
	if cb != nil {
		cb.mu.Lock()
		if cb.State == CircuitBreakerOpen {
			cb.mu.Unlock()
			writeErr(w, 409, "model circuit breaker is open")
			return
		}
		cb.mu.Unlock()
	}
	// Update all session affinities
	if p.sessionAffinity != nil {
		p.sessionAffinity.mu.Lock()
		for sessionID := range p.sessionAffinity.mapping {
			p.sessionAffinity.mapping[sessionID] = req.Model
		}
		p.sessionAffinity.mu.Unlock()
	}
	// Update strategy sticky model
	strat.mu.Lock()
	strat.StickyModel = req.Model
	strat.StickyChosenAt = time.Now()
	strat.mu.Unlock()
	writeJSON(w, map[string]any{"ok": true, "strategy": req.Strategy, "model": req.Model})
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
// Virtual meta-model providers are never routed directly — they synthesize
// their catalog from strategies and have no real upstream.
func (p *Proxy) routeModel(model string) (*Provider, string) {
	for i := range p.cfg.Providers {
		prov := &p.cfg.Providers[i]
		if prov.isVirtual() {
			continue
		}
		if p.providerModelSet(*prov)[model] {
			return prov, model
		}
	}
	// Fallback: "<providerID>/<model>" prefix routing.
	if idx := strings.IndexByte(model, '/'); idx > 0 {
		prefix := model[:idx]
		for i := range p.cfg.Providers {
			prov := &p.cfg.Providers[i]
			if prov.ID == prefix && !prov.isVirtual() {
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

	// Get session ID for strategy affinity
	sessionID := r.Header.Get("X-Session-Id")
	if sessionID == "" {
		sessionID = r.Header.Get("x-opencode-session")
	}

	// Check if the model is a strategy
	strat := p.getStrategyForModel(req.Model)
	var prov *Provider
	var upstreamModel string
	var effectiveLimit int

	if strat != nil {
		// Route through strategy
		chosenModel, chosenProv, err := p.routeStrategyModel(strat, sessionID, req.Model)
		if err != nil {
			writeErr(w, 503, err.Error())
			return
		}
		prov = chosenProv
		upstreamModel = chosenModel
		effectiveLimit = strat.EffectiveLimit
	} else {
		// Direct provider routing — real models are never intercepted by a
		// strategy; their context limit comes from the upstream catalog.
		prov, upstreamModel = p.routeModel(req.Model)
		if prov == nil {
			writeErr(w, 400, fmt.Sprintf("model %q not served by any configured model-proxy provider", req.Model))
			return
		}
	}

	// Set X-Context-Limit header for opencode
	if effectiveLimit > 0 {
		w.Header().Set("X-Context-Limit", fmt.Sprintf("%d", effectiveLimit))
	}

	ship := shipFromRequest(r.Header.Get("Authorization"))
	p.forwardChat(w, r, prov, upstreamModel, body, isStreamingRequest(body), ship, req.Model, effectiveLimit)
}

// forwardChat performs the (possibly retried) upstream chat request and
// records the outcome in the per-ship tracker.
func (p *Proxy) forwardChat(w http.ResponseWriter, r *http.Request, prov *Provider, model string, body []byte, streaming bool, ship, requestedModel string, effectiveLimit int) {
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
	startTime := time.Now()
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
			if strat := p.getStrategyForModel(requestedModel); strat != nil {
				p.recordStrategyMetrics(strat, model, time.Since(startTime), 0, err)
				p.checkCircuitBreaker(strat, model)
			}
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
			if strat := p.getStrategyForModel(requestedModel); strat != nil {
				p.recordStrategyMetrics(strat, model, time.Since(startTime), 0, fmt.Errorf("HTTP %d", resp.StatusCode))
				p.checkCircuitBreaker(strat, model)
			}
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
			if strat := p.getStrategyForModel(requestedModel); strat != nil {
				tokens := 0
				if usage != nil {
					tokens = int(usage.PromptTokens + usage.CompletionTokens)
				}
				p.recordStrategyMetrics(strat, model, time.Since(startTime), tokens, nil)
				if failed {
					p.checkCircuitBreaker(strat, model)
				}
			}
		} else {
			bodyBytes, err := io.ReadAll(resp.Body)
			resp.Body.Close()
			if err != nil {
				p.tracker.record(ship, prov.ID, requestedModel, nil, retries, true)
				if strat := p.getStrategyForModel(requestedModel); strat != nil {
					p.recordStrategyMetrics(strat, model, time.Since(startTime), 0, err)
					p.checkCircuitBreaker(strat, model)
				}
				writeErr(w, 502, fmt.Sprintf("upstream %s: read response: %v", prov.ID, err))
				return
			}
			usage := extractUsage(bodyBytes)
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(resp.StatusCode)
			_, _ = w.Write(bodyBytes)
			p.tracker.record(ship, prov.ID, requestedModel, usage, retries, false)
			if strat := p.getStrategyForModel(requestedModel); strat != nil {
				tokens := 0
				if usage != nil {
					tokens = int(usage.PromptTokens + usage.CompletionTokens)
				}
				p.recordStrategyMetrics(strat, model, time.Since(startTime), tokens, nil)
			}
		}
		return
	}
	p.tracker.record(ship, prov.ID, requestedModel, nil, retries, true)
	if strat := p.getStrategyForModel(requestedModel); strat != nil {
		p.recordStrategyMetrics(strat, model, time.Since(startTime), 0, lastErr)
		p.checkCircuitBreaker(strat, model)
	}
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
