// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package web

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"
)

func TestBareModelID(t *testing.T) {
	cases := []struct {
		provider, id, want string
	}{
		{"nim-proxy", "nim-proxy/nvidia/nemotron-3-ultra-550b-a55b", "nvidia/nemotron-3-ultra-550b-a55b"},
		{"zen-proxy", "zen-proxy/openai/chatgpt", "openai/chatgpt"},
		{"nim-proxy", "nvidia/nemotron-3-ultra-550b-a55b", "nvidia/nemotron-3-ultra-550b-a55b"},
		{"groq", "nim-proxy/groq/x", "nim-proxy/groq/x"},
	}
	for _, c := range cases {
		if got := bareModelID(c.provider, c.id); got != c.want {
			t.Errorf("bareModelID(%q, %q) = %q, want %q", c.provider, c.id, got, c.want)
		}
	}
}

func TestModelSetHas(t *testing.T) {
	s := modelSet{"a": true}
	if !s.has("a") {
		t.Error("has(a) = false, want true")
	}
	if s.has("b") {
		t.Error("has(b) = true, want false")
	}
}

// TestFilterAvailableModelsNoProxyConfig: when there is no model-proxy config
// (or it cannot be loaded), filterAvailableModels must keep every model — an
// empty dropdown is worse than showing all models.
func TestFilterAvailableModelsNoProxyConfig(t *testing.T) {
	root := t.TempDir()
	models := []modelEntry{
		{ID: "nim-proxy/nvidia/nemotron-3-ultra-550b-a55b", Provider: "nim-proxy", Label: "Nemotron Ultra"},
		{ID: "groq/openai/gpt-oss-120b", Provider: "groq", Label: "GPT OSS 120B"},
	}
	got := filterAvailableModels(root, models)
	if len(got) != len(models) {
		t.Fatalf("filtered %d models with no proxy config, want %d (keep all)", len(got), len(models))
	}
}

// TestFilterAvailableModelsNonProxiedProvider: models whose provider is not in
// the model-proxy config are kept regardless of the served list.
func TestFilterAvailableModelsNonProxiedProvider(t *testing.T) {
	root := t.TempDir()
	models := []modelEntry{
		{ID: "groq/openai/gpt-oss-120b", Provider: "groq", Label: "GPT OSS 120B"},
		{ID: "opencode/big-pickle", Provider: "opencode", Label: "Big Pickle"},
	}
	got := filterAvailableModels(root, models)
	if len(got) != len(models) {
		t.Fatalf("non-proxied models were filtered (%d of %d kept), want all", len(got), len(models))
	}
}

// TestFilterAvailableModelsDropsUnserved: models.yaml entries marked as
// proxied (placed under a model-proxy config dir with a provider entry) are
// dropped when the provider's served list does not contain the bare id.
func TestFilterAvailableModelsDropsUnserved(t *testing.T) {
	root := t.TempDir()
	addr := seedModelProxyConfig(t, root, "nim-proxy")

	models := []modelEntry{
		{ID: "nim-proxy/nvidia/nemotron-3-ultra-550b-a55b", Provider: "nim-proxy", Label: "Served"},
		{ID: "nim-proxy/deepseek-ai/deepseek-v4-flash-0731", Provider: "nim-proxy", Label: "Not served"},
	}
	got := filterAvailableModels(root, models)
	if len(got) != 1 || got[0].ID != "nim-proxy/nvidia/nemotron-3-ultra-550b-a55b" {
		t.Fatalf("unexpected filter result: %+v", got)
	}
	if addr == "" {
		t.Fatal("seedModelProxyConfig returned empty addr")
	}
}

// TestFilterAvailableModelsKeepsServed: all served proxied models survive the
// filter (they were listed by the provider's /v1/models).
func TestFilterAvailableModelsKeepsServed(t *testing.T) {
	root := t.TempDir()
	addr := seedModelProxyConfig(t, root, "nim-proxy")

	models := []modelEntry{
		{ID: "nim-proxy/nvidia/nemotron-3-ultra-550b-a55b", Provider: "nim-proxy", Label: "Ultra"},
		{ID: "nim-proxy/nvidia/nemotron-3-super-120b-a12b", Provider: "nim-proxy", Label: "Super"},
	}
	got := filterAvailableModels(root, models)
	if len(got) != 2 {
		t.Fatalf("filtered served models, got %d, want 2: %+v", len(got), got)
	}
	_ = addr
}

// seedModelProxyConfig writes a minimal model-proxy config into root whose
// listen_addr points at a test HTTP server that serves an explicit bare model
// set (the "free-tier" list) and returns the server address. ModelListFor
// then deterministically sees exactly those models.
func seedModelProxyConfig(t *testing.T, root, prov string) string {
	t.Helper()
	served := []string{
		"nvidia/nemotron-3-ultra-550b-a55b",
		"nvidia/nemotron-3-super-120b-a12b",
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		items := make([]map[string]string, len(served))
		for i, id := range served {
			items[i] = map[string]string{"id": id}
		}
		_ = json.NewEncoder(w).Encode(map[string]any{"data": items})
	}))
	t.Cleanup(srv.Close)
	addr := srv.Listener.Addr().String()

	confDir := filepath.Join(root, ".starfleet-ai", "conf")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatal(err)
	}
	cfg := "model_proxy:\n" +
		"  listen_addr: \"" + addr + "\"\n" +
		"  providers:\n" +
		"    - id: " + prov + "\n" +
		"      base_url: \"http://127.0.0.1:9/v1\"\n" +
		"      api_key: \"test\"\n"
	if err := os.WriteFile(filepath.Join(confDir, "model-proxy.yaml"), []byte(cfg), 0o644); err != nil {
		t.Fatal(err)
	}
	return addr
}
