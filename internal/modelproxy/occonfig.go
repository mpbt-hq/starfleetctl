// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package modelproxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sort"
	"time"
)

// jsonDecode decodes a JSON response body into v (and closes it).
func jsonDecode(resp *http.Response, v any) error {
	defer resp.Body.Close()
	return json.NewDecoder(resp.Body).Decode(v)
}

// proxyDummyKey is the API key written into the per-ship opencode provider
// config. The proxy itself holds the real upstream keys (from model-proxy.yaml
// + env) and does not authenticate local clients, so the per-ship config never
// carries a real secret.
const proxyDummyKey = "starfleet-model-proxy"

// proxyBaseURL returns the baseURL that should be written into the opencode
// provider config, i.e. the proxy's own OpenAI endpoint.
func (c *Config) proxyBaseURL() string {
	return "http://" + c.ListenAddr + "/v1"
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
func ProviderConfigs(root string) map[string]any {
	cfg, err := Load(root)
	if err != nil {
		return nil
	}
	if len(cfg.Providers) == 0 {
		return nil
	}
	base := cfg.proxyBaseURL()
	out := map[string]any{}
	for _, prov := range cfg.Providers {
		entry := map[string]any{
			"npm":  "@ai-sdk/openai-compatible",
			"name": prov.Name,
			"options": map[string]any{
				"baseURL": base,
				"apiKey":  proxyDummyKey,
			},
		}
		if models := cfg.modelListFor(prov); len(models) > 0 {
			mm := map[string]any{}
			for _, id := range models {
				mm[id] = map[string]any{"name": id}
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
