// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package modelproxy

import (
	"sort"
	"strings"
	"sync"
	"time"
)

// Usage holds the token counters extracted from an upstream chat response.
type Usage struct {
	PromptTokens     int64 `json:"prompt_tokens"`
	CompletionTokens int64 `json:"completion_tokens"`
}

// ShipStats aggregates per-ship proxy usage. Ships are identified by the
// "mp-<shipID>-<token>" apiKey in their per-ship opencode config (see
// ShipKey); requests without one are attributed to "unknown".
type ShipStats struct {
	ShipID           string           `json:"ship"`
	TotalRequests    int64            `json:"total_requests"`
	Successes        int64            `json:"successes"`
	Failures         int64            `json:"failures"`
	Retries          int64            `json:"retries"`
	PromptTokens     int64            `json:"prompt_tokens"`
	CompletionTokens int64            `json:"completion_tokens"`
	ByProvider       map[string]int64 `json:"by_provider"`
	ByModel          map[string]int64 `json:"by_model"`
	LastRequest      time.Time        `json:"last_request"`
	LastModel        string           `json:"last_model"`
}

type shipTracker struct {
	mu    sync.Mutex
	stats map[string]*ShipStats
}

func newShipTracker() *shipTracker {
	return &shipTracker{stats: map[string]*ShipStats{}}
}

// shipFromRequest derives the ship identity from the request's Authorization
// header ("Bearer mp-<shipID>-<token>", see ShipKey). Unknown keys and
// missing headers fall back to "unknown".
func shipFromRequest(authHeader string) string {
	token := strings.TrimSpace(strings.TrimPrefix(authHeader, "Bearer "))
	if !strings.HasPrefix(token, "mp-") {
		return "unknown"
	}
	rest := token[len("mp-"):]
	if idx := strings.LastIndexByte(rest, '-'); idx > 0 {
		return rest[:idx]
	}
	return "unknown"
}

func (t *shipTracker) record(ship, provider, model string, usage *Usage, retries int, failed bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	s := t.stats[ship]
	if s == nil {
		s = &ShipStats{ShipID: ship, ByProvider: map[string]int64{}, ByModel: map[string]int64{}}
		t.stats[ship] = s
	}
	s.TotalRequests++
	s.Retries += int64(retries)
	s.ByProvider[provider]++
	s.ByModel[model]++
	s.LastRequest = time.Now()
	s.LastModel = model
	if usage != nil {
		s.PromptTokens += usage.PromptTokens
		s.CompletionTokens += usage.CompletionTokens
	}
	if failed {
		s.Failures++
	} else {
		s.Successes++
	}
}

// snapshot returns a deep-copied, ship-sorted view of the tracked stats.
func (t *shipTracker) snapshot() []ShipStats {
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make([]ShipStats, 0, len(t.stats))
	for _, s := range t.stats {
		cp := *s
		cp.ByProvider = make(map[string]int64, len(s.ByProvider))
		for k, v := range s.ByProvider {
			cp.ByProvider[k] = v
		}
		cp.ByModel = make(map[string]int64, len(s.ByModel))
		for k, v := range s.ByModel {
			cp.ByModel[k] = v
		}
		out = append(out, cp)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ShipID < out[j].ShipID })
	return out
}
