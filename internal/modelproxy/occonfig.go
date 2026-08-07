// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package modelproxy

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"time"
)

// jsonDecode decodes a JSON response body into v (and closes it).
func jsonDecode(resp *http.Response, v any) error {
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}

// ShipKey returns the per-ship API key written into a ship's opencode
// provider config (format "mp-<shipID>-<token>"). The proxy derives the ship
// identity from it, so per-ship usage/status tracking works even though all
// ships connect from 127.0.0.1. The token is random per generated config.
func ShipKey(shipID string) string {
	var b [6]byte
	if _, err := rand.Read(b[:]); err != nil {
		// crypto/rand failing is virtually impossible; fall back to a fixed
		// suffix so config generation never hard-fails on it.
		return "mp-" + shipID + "-insecure"
	}
	return "mp-" + shipID + "-" + hex.EncodeToString(b[:])
}

// proxyBaseURL returns the baseURL that should be written into the opencode
// provider config, i.e. the proxy's own OpenAI endpoint.
func (c *Config) proxyBaseURL() string {
	return "http://" + c.ListenAddr + "/v1"
}

// modelInfoFor returns the full model metadata (with label/context/caps) one
// provider serves, best-effort: first via the running local proxy, falling
// back to a direct upstream query. Mirrors modelListFor but keeps metadata.
func (c *Config) modelInfoFor(prov Provider) []ModelInfo {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(c.proxyBaseURL() + "/models?provider=" + prov.ID)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			var out struct {
				Data []ModelInfo `json:"data"`
			}
			if decodeErr := jsonDecode(resp, &out); decodeErr == nil {
				return out.Data
			}
		}
	}
	raw, ferr := fetchModelInfo(prov)
	if ferr != nil {
		return nil
	}
	infos := make([]ModelInfo, len(raw))
	for i, m := range raw {
		m.OwnedBy = prov.ID
		infos[i] = m
	}
	return infos
}

// modelListFor returns the model IDs served by one provider, best-effort:
// first via the running local proxy (its /v1/models query), falling back to a
// direct upstream query so config generation still works before the daemon
// has been started.
func (c *Config) modelListFor(prov Provider) []string {
	client := &http.Client{Timeout: 10 * time.Second}
	// Prefer the local proxy: it has fresher data and shares the code path
	// ships actually use. Query is per-provider, so no cross-talk.
	resp, err := client.Get(c.proxyBaseURL() + "/models?provider=" + prov.ID)
	if err == nil {
		defer resp.Body.Close()
		if resp.StatusCode == http.StatusOK {
			var out struct {
				Data []struct {
					ID string `json:"id"`
				} `json:"data"`
			}
			if decodeErr := jsonDecode(resp, &out); decodeErr == nil {
				var ids []string
				for _, m := range out.Data {
					if m.ID != "" {
						ids = append(ids, m.ID)
					}
				}
				sort.Strings(ids)
				return ids
			}
		}
	}
	// Fallback: direct upstream query (no local proxy running).
	ids, ferr := fetchModels(prov)
	if ferr != nil {
		return nil
	}
	sort.Strings(ids)
	return ids
}

// ProviderConfigs returns opencode provider entries for every configured
// model-proxy backend (e.g. {"nim-proxy": {...}, "zen-proxy": {...}}), each
// pointing at the local proxy and enumerating the discovered models. It
// returns nil when no model-proxy configuration exists. The returned map can
// be merged directly into a generated opencode config's "provider" section.
// shipID is embedded into each provider's apiKey (see ShipKey) so the proxy
// can attribute requests to the ship.
// For providers with Direct=true, the entry uses the upstream's actual
// BaseURL and APIKey (bypassing the local proxy).
func ProviderConfigs(root, shipID string) map[string]any {
	cfg, err := Load(root)
	if err != nil {
		return nil
	}
	if len(cfg.Providers) == 0 {
		return nil
	}
	base := cfg.proxyBaseURL()
	key := ShipKey(shipID)
	out := map[string]any{}
	for _, prov := range cfg.Providers {
		// For direct providers, use upstream endpoint and key directly
		var entryBaseURL, entryAPIKey string
		if prov.Direct {
			entryBaseURL = prov.BaseURL
			entryAPIKey = prov.APIKey
		} else {
			entryBaseURL = base
			entryAPIKey = key
		}
		entry := map[string]any{
			"npm":  "@ai-sdk/openai-compatible",
			"name": prov.Name,
			"options": map[string]any{
				"baseURL": entryBaseURL,
				"apiKey":  entryAPIKey,
			},
		}
		// Model info: for direct providers, we could query upstream directly,
		// but the proxy's /models endpoint also proxies the catalog. We use
		// the existing modelInfoFor which tries proxy first then falls back to
		// direct upstream query.
		if infos := cfg.modelInfoFor(prov); len(infos) > 0 {
			mm := map[string]any{}
			for _, inf := range infos {
				mm[inf.ID] = modelEntryFor(inf)
			}
			entry["models"] = mm
		}
		out[prov.ID] = entry
	}
	if len(out) == 0 {
		return nil
	}
	return out
}

// modelEntryFor converts a proxy ModelInfo into an opencode provider "models"
// entry. opencode pulls per-model metadata (name, context limit, capability
// flags, modalities) from this map — the same surface it has for direct
// upstream providers via models.dev — so the injected config carries the
// proxy-enriched data instead of bare ids.
func modelEntryFor(inf ModelInfo) map[string]any {
	entry := map[string]any{"name": inf.ID}
	if inf.Label != "" {
		entry["name"] = inf.Label
	}
	if inf.Context > 0 && inf.Output > 0 {
		entry["limit"] = map[string]any{"context": inf.Context, "output": inf.Output}
	}
	for _, c := range inf.Caps {
		switch c {
		case "reasoning":
			entry["reasoning"] = true
		case "attachment":
			entry["attachment"] = true
		case "temperature":
			entry["temperature"] = true
		case "toolcall":
			entry["tool_call"] = true
		}
	}
	if in, out := modalitiesFromCaps(inf.Caps); in != nil || out != nil {
		m := map[string]any{}
		if in != nil {
			m["input"] = in
		}
		if out != nil {
			m["output"] = out
		}
		entry["modalities"] = m
	}
	return entry
}

// modalitiesFromCaps maps the proxy capability tags to the opencode
// modalities.input/output arrays.
func modalitiesFromCaps(caps []string) ([]string, []string) {
	var in, out []string
	for _, c := range caps {
		switch {
		case strings.HasSuffix(c, "-in"):
			in = append(in, strings.TrimSuffix(c, "-in"))
		case strings.HasSuffix(c, "-out"):
			out = append(out, strings.TrimSuffix(c, "-out"))
		}
	}
	return in, out
}

// ProxyModelInfos returns the full model catalog (with label/context/caps)
// for every configured model-proxy backend. The data is sourced from the
// running local proxy's /v1/models when reachable (the same code path ships
// use), otherwise from a direct upstream query. It is used by `models sync`
// to make the proxy models selectable in the web console.
func ProxyModelInfos(root string) []ModelInfo {
	cfg, err := Load(root)
	if err != nil {
		return nil
	}
	if len(cfg.Providers) == 0 {
		return nil
	}
	var out []ModelInfo
	for _, prov := range cfg.Providers {
		infos, ok := proxyModelInfosViaLocal(cfg, prov)
		if !ok {
			// No local proxy running — query the upstream directly (no
			// catalog enrichment without the proxy, but ids still valid).
			raw, ferr := fetchModelInfo(prov)
			if ferr != nil {
				continue
			}
			infos = raw
		}
		for _, m := range infos {
			m.OwnedBy = prov.ID
			out = append(out, m)
		}
	}
	return out
}

// proxyModelInfosViaLocal queries the running local proxy for one provider's
// model catalog (enriched with label/context/caps).
func proxyModelInfosViaLocal(cfg *Config, prov Provider) ([]ModelInfo, bool) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(cfg.proxyBaseURL() + "/models?provider=" + prov.ID)
	if err != nil {
		return nil, false
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, false
	}
	var out struct {
		Data []ModelInfo `json:"data"`
	}
	if err := jsonDecode(resp, &out); err != nil {
		return nil, false
	}
	return out.Data, true
}

// checkModels prints the model catalog the proxy knows for each configured
// provider (diagnostics for `model-proxy models`).
func checkModels(root string) error {
	cfg, err := Load(root)
	if err != nil {
		return err
	}
	if len(cfg.Providers) == 0 {
		fmt.Println("model-proxy: no providers configured in model-proxy.yaml")
		return nil
	}
	for _, prov := range cfg.Providers {
		models := cfg.modelListFor(prov)
		fmt.Printf("%s (%s): %d models\n", prov.ID, prov.BaseURL, len(models))
		for _, id := range models {
			fmt.Printf("  %s\n", id)
		}
	}
	return nil
}
