// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Model health check — verifies that every model listed in models.yaml for a
// proxied provider is actually served and, optionally, that a minimal chat
// request succeeds. Transient upstream failures (429/5xx, timeouts,
// gRPC-style saturation) are retried; only hard errors mark a model as failed.
//
// The persisted health state lives in .starfleet-ai/var/model-health.json and
// is consumed by the web console (model dropdown badges) and by the
// `model-proxy check` diagnostics command.

package modelproxy

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"gopkg.in/yaml.v3"
)

// CheckStatus is the health verdict for a single model.
type CheckStatus string

const (
	// StatusOK — served and (with probe) replies successfully.
	StatusOK CheckStatus = "ok"
	// StatusNotServed — listed in the catalog but not served by the provider
	// (filtered out upstream). opencode would silently fall back to another
	// model, so this is a (typically pre-existing) config issue, not a fault.
	StatusNotServed CheckStatus = "not-served"
	// StatusFailed — a hard upstream error (e.g. no-account/not-found, auth)
	// survived all retries.
	StatusFailed CheckStatus = "failed"
	// StatusDegraded — only transient errors (saturation, timeouts) persisted;
	// the model may work again shortly, but is currently unreliable.
	StatusDegraded CheckStatus = "degraded"
	// StatusUnknown — the served set could not be queried (provider down), so
	// no verdict beyond "listed" is possible.
	StatusUnknown CheckStatus = "unknown"
)

// ModelHealth is the per-model check result.
type ModelHealth struct {
	ID       string      `json:"id"`                // full catalog id ("<provider>/<model>")
	Provider string      `json:"provider"`          // proxied provider id
	Served   bool        `json:"served"`            // present in the provider's served set
	Status   CheckStatus `json:"status"`            // overall verdict
	Detail   string      `json:"detail,omitempty"`  // short human-readable reason
	Retries  int         `json:"retries,omitempty"` // probe retries performed
}

// CheckReport is the full result of one check run.
type CheckReport struct {
	At     time.Time     `json:"at"`
	Probe  bool          `json:"probe"` // whether chat probes were sent
	Models []ModelHealth `json:"models"`
	// Aggregates.
	Total     int `json:"total"`
	OK        int `json:"ok"`
	NotServed int `json:"not_served"`
	Failed    int `json:"failed"`
	Degraded  int `json:"degraded"`
	Unknown   int `json:"unknown"`
}

// HealthState is the persisted check state, keyed by full model id.
type HealthState struct {
	At     time.Time              `json:"at"`
	Probe  bool                   `json:"probe"`
	Models map[string]ModelHealth `json:"models"`
}

// HealthStatePath returns the persisted health-state file location.
func HealthStatePath(root string) string {
	return filepath.Join(root, ".starfleet-ai", "var", "model-health.json")
}

// LoadHealthState reads the persisted health state. A missing or corrupt file
// yields an empty state (not an error).
func LoadHealthState(root string) *HealthState {
	data, err := os.ReadFile(HealthStatePath(root))
	if err != nil {
		return &HealthState{Models: map[string]ModelHealth{}}
	}
	st := &HealthState{}
	if err := json.Unmarshal(data, st); err != nil {
		return &HealthState{Models: map[string]ModelHealth{}}
	}
	if st.Models == nil {
		st.Models = map[string]ModelHealth{}
	}
	return st
}

// WriteHealthState persists the health state atomically.
func WriteHealthState(root string, st *HealthState) error {
	path := HealthStatePath(root)
	if st.Models == nil {
		st.Models = map[string]ModelHealth{}
	}
	data, err := json.MarshalIndent(st, "", "  ")
	if err != nil {
		return fmt.Errorf("health state: marshal: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return fmt.Errorf("health state: mkdir: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, append(data, '\n'), 0o644); err != nil {
		return fmt.Errorf("health state: write: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("health state: rename: %w", err)
	}
	return nil
}

// Merge folds a fresh check report into the persisted state. Model ids not
// present in the newest run are dropped so removed models never linger.
func (st *HealthState) Merge(report *CheckReport) {
	merged := map[string]ModelHealth{}
	for _, mh := range report.Models {
		merged[mh.ID] = mh
	}
	st.Models = merged
	st.At = report.At
	st.Probe = report.Probe
}

// Get returns the persisted health for a model id and whether any is known.
func (st *HealthState) Get(id string) (ModelHealth, bool) {
	mh, ok := st.Models[id]
	return mh, ok
}

// catalogEntry is one models.yaml entry.
type catalogEntry struct {
	ID       string
	Provider string
	Label    string
}

// loadCatalogEntries parses .starfleet-ai/conf/models.yaml (the format the
// `models sync` subcommand maintains) and returns the active entries, grouped
// by provider id. A missing/corrupt file yields an empty map.
func loadCatalogEntries(root string) map[string][]catalogEntry {
	data, err := os.ReadFile(filepath.Join(root, ".starfleet-ai", "conf", "models.yaml"))
	if err != nil {
		return nil
	}
	// Strip the free-form header comments / "---" delimiter before parsing.
	lines := strings.Split(string(data), "\n")
	var clean []string
	started := false
	for _, l := range lines {
		if !started {
			if strings.HasPrefix(l, "---") {
				started = true
				clean = append(clean, l)
			} else if strings.HasPrefix(l, "#") || strings.TrimSpace(l) == "" {
				continue
			} else {
				started = true
				clean = append(clean, l)
			}
			continue
		}
		clean = append(clean, l)
	}
	var doc struct {
		Models []struct {
			ID       string `yaml:"id"`
			Provider string `yaml:"provider"`
			Label    string `yaml:"label"`
			Disabled bool   `yaml:"disabled"`
		} `yaml:"models"`
	}
	if err := yaml.Unmarshal([]byte(strings.Join(clean, "\n")), &doc); err != nil {
		return nil
	}
	out := map[string][]catalogEntry{}
	for _, m := range doc.Models {
		if m.ID == "" || m.Provider == "" || m.Disabled {
			continue
		}
		out[m.Provider] = append(out[m.Provider], catalogEntry{
			ID:       m.ID,
			Provider: m.Provider,
			Label:    m.Label,
		})
	}
	return out
}

// RunCheck runs the health check over all active models.yaml catalog entries
// of the configured proxied providers. With probe=true, one minimal chat
// request per served model validates that it actually accepts requests;
// transient failures are retried and only persist as "degraded", hard errors
// mark the model as "failed".
func RunCheck(root string, probe bool) (*CheckReport, error) {
	cfg, err := Load(root)
	if err != nil {
		return nil, err
	}
	report := &CheckReport{At: time.Now(), Probe: probe}
	if len(cfg.Providers) == 0 {
		return report, nil
	}

	catalog := loadCatalogEntries(root)
	for _, entries := range catalog {
		report.Total += len(entries)
	}

	for _, prov := range cfg.Providers {
		entries := catalog[prov.ID]
		// Served set (already filtered) fetched once per provider.
		served, servedErr := fetchModelsRetry(prov)
		servedSet := modelSet(served)
		for _, entry := range entries {
			if servedErr != nil {
				// Provider unreachable — no verdicts possible beyond "listed".
				report.Models = append(report.Models, ModelHealth{
					ID:       entry.ID,
					Provider: prov.ID,
					Served:   false,
					Status:   StatusUnknown,
					Detail:   "served listing unavailable: " + servedErr.Error(),
				})
				report.Unknown++
				continue
			}
			bareID := strings.TrimPrefix(entry.ID, prov.ID+"/")
			if !servedSet[bareID] {
				report.Models = append(report.Models, ModelHealth{
					ID:       entry.ID,
					Provider: prov.ID,
					Served:   false,
					Status:   StatusNotServed,
					Detail:   "not in provider /v1/models listing (filtered or removed upstream)",
				})
				report.NotServed++
				continue
			}
			mh := ModelHealth{ID: entry.ID, Provider: prov.ID, Served: true, Status: StatusOK}
			if probe {
				detail, retries, ok := probeModel(prov, bareID)
				mh.Retries = retries
				if !ok {
					if strings.HasPrefix(detail, "transient:") {
						mh.Status = StatusDegraded
						report.Degraded++
					} else {
						mh.Status = StatusFailed
						report.Failed++
					}
					mh.Detail = detail
					report.Models = append(report.Models, mh)
					continue
				}
			}
			report.OK++
			report.Models = append(report.Models, mh)
		}
	}
	return report, nil
}

// fetchModelsRetry wraps fetchModels with the provider's transient-retry
// policy so a saturated upstream doesn't masquerade as "no models served".
func fetchModelsRetry(prov Provider) ([]string, error) {
	attempts := prov.MaxRetries + 1
	if attempts < 1 {
		attempts = 1
	}
	var lastErr error
	for attempt := 1; attempt <= attempts; attempt++ {
		ids, err := fetchModels(prov)
		if err == nil {
			return ids, nil
		}
		lastErr = err
		if attempt < attempts && (retryableError(err) || transientStatus(statusOfError(err))) {
			time.Sleep(time.Duration(prov.RetryDelayMS) * time.Millisecond)
			continue
		}
		return nil, lastErr
	}
	return nil, lastErr
}

// statusOfError extracts the HTTP status code from a fetchModels error message
// ("models: upstream HTTP %d"); 0 when there is none.
func statusOfError(err error) int {
	if err == nil {
		return 0
	}
	var code int
	if _, serr := fmt.Sscanf(err.Error(), "models: upstream HTTP %d", &code); serr == nil {
		return code
	}
	return 0
}

// probeModel sends a minimal 1-token chat request to the provider and returns
// (detail, retries, ok). Transient failures (429/5xx, timeouts, saturation
// text) are retried; detail is prefixed "transient:" when only those
// persisted. Hard errors (e.g. 404 no-account, 401 auth) come back with a
// plain detail and ok=false.
func probeModel(prov Provider, model string) (string, int, bool) {
	client := &http.Client{Timeout: 30 * time.Second}
	payload, _ := json.Marshal(map[string]any{
		"model":      model,
		"messages":   []map[string]string{{"role": "user", "content": "ping"}},
		"max_tokens": 1,
		"stream":     false,
	})
	attempts := prov.MaxRetries + 1
	if attempts < 1 {
		attempts = 1
	}
	var lastFail string
	retries := 0
	for attempt := 1; attempt <= attempts; attempt++ {
		req, err := http.NewRequest(http.MethodPost, prov.BaseURL+"/chat/completions", bytes.NewReader(payload))
		if err != nil {
			// Request construction errors are local and never transient.
			return "request: " + err.Error(), retries, false
		}
		req.Header.Set("Content-Type", "application/json")
		if prov.APIKey != "" {
			req.Header.Set("Authorization", "Bearer "+prov.APIKey)
		}
		resp, err := client.Do(req)
		if err != nil {
			msg := err.Error()
			if retryableError(err) {
				if attempt < attempts {
					lastFail = msg
					retries++
					time.Sleep(time.Duration(prov.RetryDelayMS) * time.Millisecond)
					continue
				}
				return "transient:" + msg, retries, false
			}
			return msg, retries, false
		}
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
		resp.Body.Close()
		if resp.StatusCode >= 400 {
			errText := string(body)
			transient := transientStatus(resp.StatusCode) || retryableErrorText(errText)
			if transient {
				if attempt < attempts {
					lastFail = fmt.Sprintf("HTTP %d: %s", resp.StatusCode, firstLine(errText))
					retries++
					time.Sleep(time.Duration(prov.RetryDelayMS) * time.Millisecond)
					continue
				}
				return "transient:" + fmt.Sprintf("HTTP %d: %s", resp.StatusCode, firstLine(errText)), retries, false
			}
			return fmt.Sprintf("HTTP %d: %s", resp.StatusCode, firstLine(errText)), retries, false
		}
		// 2xx — the request round-tripped. Some backends answer saturation
		// with a 200 + error payload; treat that as transient.
		if msg := retryableErrorText(string(body)); msg {
			if attempt < attempts {
				lastFail = "200 + saturation payload: " + firstLine(string(body))
				retries++
				time.Sleep(time.Duration(prov.RetryDelayMS) * time.Millisecond)
				continue
			}
			return "transient:200 + saturation payload: " + firstLine(string(body)), retries, false
		}
		return "", retries, true
	}
	if lastFail != "" {
		return "transient:" + lastFail, retries, false
	}
	return "transient:request failed", retries, false
}

// firstLine returns the first non-empty line of s, trimmed and bounded (for
// report bodies).
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	const max = 240
	if len(s) > max {
		s = s[:max] + "…"
	}
	return s
}

// modelSet converts a model id list into a set.
func modelSet(ids []string) map[string]bool {
	set := make(map[string]bool, len(ids))
	for _, id := range ids {
		set[id] = true
	}
	return set
}
