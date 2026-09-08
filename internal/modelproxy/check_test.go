// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package modelproxy

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestHealthStateRoundtrip(t *testing.T) {
	root := t.TempDir()
	state := &HealthState{
		At:    time.Now(),
		Probe: true,
		Models: map[string]ModelHealth{
			"nim-proxy/nvidia/nemotron-3-ultra-550b-a55b": {
				ID:       "nim-proxy/nvidia/nemotron-3-ultra-550b-a55b",
				Provider: "nim-proxy",
				Served:   true,
				Status:   StatusOK,
				Detail:   "ok",
			},
		},
	}
	if err := WriteHealthState(root, state); err != nil {
		t.Fatalf("WriteHealthState: %v", err)
	}
	loaded := LoadHealthState(root)
	if len(loaded.Models) != 1 {
		t.Fatalf("expected 1 model, got %d", len(loaded.Models))
	}
	mh, ok := loaded.Get("nim-proxy/nvidia/nemotron-3-ultra-550b-a55b")
	if !ok {
		t.Fatal("model not found after roundtrip")
	}
	if mh.Status != StatusOK {
		t.Errorf("status mismatch: %s != %s", mh.Status, StatusOK)
	}
	if !loaded.Probe {
		t.Error("Probe flag not preserved")
	}
}

func TestRunCheck_NotServed(t *testing.T) {
	// Upstream serves only model A; catalog lists A + B.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"data":[{"id":"model-a","object":"model"}]}`))
			return
		}
		if r.URL.Path == "/chat/completions" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	root := t.TempDir()
	confDir := filepath.Join(root, ".starfleet-ai", "conf")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatal(err)
	}
	proxyYaml := `model_proxy:
  listen_addr: "127.0.0.1:12345"
  providers:
    - id: "nim-proxy"
      name: "NVIDIA NIM"
      base_url: "` + upstream.URL + `"
      api_key: "test-key"
      model_filter: "all"
`
	if err := os.WriteFile(filepath.Join(confDir, "model-proxy.yaml"), []byte(proxyYaml), 0o644); err != nil {
		t.Fatal(err)
	}
	// Write models.yaml with model-a (served) and model-b (not served)
	modelsYaml := `
models:
  - id: "nim-proxy/model-a"
    provider: "nim-proxy"
    label: "Model A"
    context: 8192
    caps: ["toolcall"]
  - id: "nim-proxy/model-b"
    provider: "nim-proxy"
    label: "Model B"
    context: 8192
    caps: ["toolcall"]
`
	if err := os.WriteFile(filepath.Join(confDir, "models.yaml"), []byte(modelsYaml), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := RunCheck(root, false)
	if err != nil {
		t.Fatalf("RunCheck: %v", err)
	}
	if report.Total != 2 {
		t.Fatalf("Total=2, got %d", report.Total)
	}
	if report.OK != 1 {
		t.Errorf("OK=1, got %d", report.OK)
	}
	if report.NotServed != 1 {
		t.Errorf("NotServed=1, got %d", report.NotServed)
	}
	// model-a should be OK
	var foundA, foundB bool
	for _, mh := range report.Models {
		if mh.ID == "nim-proxy/model-a" {
			foundA = true
			if mh.Status != StatusOK {
				t.Errorf("model-a status: %s, want %s", mh.Status, StatusOK)
			}
			if !mh.Served {
				t.Error("model-a should be served")
			}
		}
		if mh.ID == "nim-proxy/model-b" {
			foundB = true
			if mh.Status != StatusNotServed {
				t.Errorf("model-b status: %s, want %s", mh.Status, StatusNotServed)
			}
			if mh.Served {
				t.Error("model-b should not be served")
			}
		}
	}
	if !foundA || !foundB {
		t.Error("both models not found in report")
	}
}

func TestRunCheck_Probe_Success(t *testing.T) {
	// Upstream serves model-a AND accepts chat.
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"data":[{"id":"model-a","object":"model"}]}`))
			return
		}
		if r.URL.Path == "/chat/completions" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	root := t.TempDir()
	confDir := filepath.Join(root, ".starfleet-ai", "conf")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatal(err)
	}
	proxyYaml := `model_proxy:
  listen_addr: "127.0.0.1:12345"
  providers:
    - id: "nim-proxy"
      name: "NVIDIA NIM"
      base_url: "` + upstream.URL + `"
      api_key: "test-key"
      model_filter: "all"
      max_retries: 0
      retry_delay_ms: 0
`
	if err := os.WriteFile(filepath.Join(confDir, "model-proxy.yaml"), []byte(proxyYaml), 0o644); err != nil {
		t.Fatal(err)
	}
	modelsYaml := `
models:
  - id: "nim-proxy/model-a"
    provider: "nim-proxy"
    label: "Model A"
    context: 8192
    caps: ["toolcall"]
`
	if err := os.WriteFile(filepath.Join(confDir, "models.yaml"), []byte(modelsYaml), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := RunCheck(root, true)
	if err != nil {
		t.Fatalf("RunCheck: %v", err)
	}
	if report.OK != 1 {
		t.Errorf("OK=1, got %d", report.OK)
	}
	for _, mh := range report.Models {
		if mh.ID == "nim-proxy/model-a" {
			if mh.Status != StatusOK {
				t.Errorf("model-a status: %s, want %s", mh.Status, StatusOK)
			}
			if mh.Retries != 0 {
				t.Errorf("retries should be 0, got %d", mh.Retries)
			}
		}
	}
}

func TestRunCheck_Probe_HardFail_404(t *testing.T) {
	// Upstream serves model-a but chat returns 404 (no-account / not-found).
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"data":[{"id":"model-a","object":"model"}]}`))
			return
		}
		if r.URL.Path == "/chat/completions" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusNotFound)
			w.Write([]byte(`{"error":{"message":"Not found for account","type":"not_found"}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	root := t.TempDir()
	confDir := filepath.Join(root, ".starfleet-ai", "conf")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatal(err)
	}
	proxyYaml := `model_proxy:
  listen_addr: "127.0.0.1:12345"
  providers:
    - id: "nim-proxy"
      name: "NVIDIA NIM"
      base_url: "` + upstream.URL + `"
      api_key: "test-key"
      model_filter: "all"
      max_retries: 0
      retry_delay_ms: 0
`
	if err := os.WriteFile(filepath.Join(confDir, "model-proxy.yaml"), []byte(proxyYaml), 0o644); err != nil {
		t.Fatal(err)
	}
	modelsYaml := `
models:
  - id: "nim-proxy/model-a"
    provider: "nim-proxy"
    label: "Model A"
    context: 8192
    caps: ["toolcall"]
`
	if err := os.WriteFile(filepath.Join(confDir, "models.yaml"), []byte(modelsYaml), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := RunCheck(root, true)
	if err != nil {
		t.Fatalf("RunCheck: %v", err)
	}
	if report.Failed != 1 {
		t.Errorf("Failed=1, got %d", report.Failed)
	}
	for _, mh := range report.Models {
		if mh.ID == "nim-proxy/model-a" {
			if mh.Status != StatusFailed {
				t.Errorf("model-a status: %s, want %s", mh.Status, StatusFailed)
			}
			if !strings.Contains(mh.Detail, "HTTP 404") {
				t.Errorf("detail should contain HTTP 404: %s", mh.Detail)
			}
		}
	}
}

func TestRunCheck_Probe_TransientRetries_ThenOK(t *testing.T) {
	// Upstream returns 429 twice then 200 on third attempt.
	attempts := 0
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"data":[{"id":"model-a","object":"model"}]}`))
			return
		}
		if r.URL.Path == "/chat/completions" {
			attempts++
			if attempts <= 2 {
				w.Header().Set("Content-Type", "application/json")
				w.WriteHeader(http.StatusTooManyRequests)
				w.Write([]byte(`{"error":{"message":"ResourceExhausted: rate limit","type":"rate_limit"}}`))
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	root := t.TempDir()
	confDir := filepath.Join(root, ".starfleet-ai", "conf")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatal(err)
	}
	proxyYaml := `model_proxy:
  listen_addr: "127.0.0.1:12345"
  providers:
    - id: "nim-proxy"
      name: "NVIDIA NIM"
      base_url: "` + upstream.URL + `"
      api_key: "test-key"
      model_filter: "all"
      max_retries: 2
      retry_delay_ms: 1
`
	if err := os.WriteFile(filepath.Join(confDir, "model-proxy.yaml"), []byte(proxyYaml), 0o644); err != nil {
		t.Fatal(err)
	}
	modelsYaml := `
models:
  - id: "nim-proxy/model-a"
    provider: "nim-proxy"
    label: "Model A"
    context: 8192
    caps: ["toolcall"]
`
	if err := os.WriteFile(filepath.Join(confDir, "models.yaml"), []byte(modelsYaml), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := RunCheck(root, true)
	if err != nil {
		t.Fatalf("RunCheck: %v", err)
	}
	// Probe eventually succeeds after retries -> should be OK, not degraded.
	if report.OK != 1 {
		t.Errorf("OK=1, got %d", report.OK)
	}
	for _, mh := range report.Models {
		if mh.ID == "nim-proxy/model-a" {
			if mh.Status != StatusOK {
				t.Errorf("model-a status: %s, want %s", mh.Status, StatusOK)
			}
			if mh.Retries != 2 {
				t.Errorf("retries should be 2, got %d", mh.Retries)
			}
		}
	}
}

func TestRunCheck_Probe_TransientExhausted_Degraded(t *testing.T) {
	// Upstream returns 429 on ALL attempts (max_retries=2 -> 3 total attempts).
	upstream := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"data":[{"id":"model-a","object":"model"}]}`))
			return
		}
		if r.URL.Path == "/chat/completions" {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":{"message":"ResourceExhausted: rate limit","type":"rate_limit"}}`))
			return
		}
		http.NotFound(w, r)
	}))
	defer upstream.Close()

	root := t.TempDir()
	confDir := filepath.Join(root, ".starfleet-ai", "conf")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatal(err)
	}
	proxyYaml := `model_proxy:
  listen_addr: "127.0.0.1:12345"
  providers:
    - id: "nim-proxy"
      name: "NVIDIA NIM"
      base_url: "` + upstream.URL + `"
      api_key: "test-key"
      model_filter: "all"
      max_retries: 2
      retry_delay_ms: 1
`
	if err := os.WriteFile(filepath.Join(confDir, "model-proxy.yaml"), []byte(proxyYaml), 0o644); err != nil {
		t.Fatal(err)
	}
	modelsYaml := `
models:
  - id: "nim-proxy/model-a"
    provider: "nim-proxy"
    label: "Model A"
    context: 8192
    caps: ["toolcall"]
`
	if err := os.WriteFile(filepath.Join(confDir, "models.yaml"), []byte(modelsYaml), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := RunCheck(root, true)
	if err != nil {
		t.Fatalf("RunCheck: %v", err)
	}
	if report.Degraded != 1 {
		t.Errorf("Degraded=1, got %d", report.Degraded)
	}
	for _, mh := range report.Models {
		if mh.ID == "nim-proxy/model-a" {
			if mh.Status != StatusDegraded {
				t.Errorf("model-a status: %s, want %s", mh.Status, StatusDegraded)
			}
			if mh.Retries != 2 {
				t.Errorf("retries should be 2, got %d", mh.Retries)
			}
			if !strings.HasPrefix(mh.Detail, "transient:") {
				t.Errorf("detail should be transient-prefixed: %s", mh.Detail)
			}
		}
	}
}

func TestRunCheck_Unknown_WhenProviderDown(t *testing.T) {
	// Provider unreachable (base_url points to nowhere).
	root := t.TempDir()
	confDir := filepath.Join(root, ".starfleet-ai", "conf")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatal(err)
	}
	proxyYaml := `model_proxy:
  listen_addr: "127.0.0.1:12345"
  providers:
    - id: "nim-proxy"
      name: "NVIDIA NIM"
      base_url: "http://127.0.0.1:1"
      api_key: "test-key"
      model_filter: "all"
      max_retries: 0
      retry_delay_ms: 0
`
	if err := os.WriteFile(filepath.Join(confDir, "model-proxy.yaml"), []byte(proxyYaml), 0o644); err != nil {
		t.Fatal(err)
	}
	modelsYaml := `
models:
  - id: "nim-proxy/model-a"
    provider: "nim-proxy"
    label: "Model A"
    context: 8192
    caps: ["toolcall"]
`
	if err := os.WriteFile(filepath.Join(confDir, "models.yaml"), []byte(modelsYaml), 0o644); err != nil {
		t.Fatal(err)
	}

	report, err := RunCheck(root, false)
	if err != nil {
		t.Fatalf("RunCheck: %v", err)
	}
	if report.Unknown != 1 {
		t.Errorf("Unknown=1, got %d", report.Unknown)
	}
	for _, mh := range report.Models {
		if mh.Status != StatusUnknown {
			t.Errorf("model-a status: %s, want %s", mh.Status, StatusUnknown)
		}
	}
}

func TestLoadCatalogEntries_IgnoresDisabled(t *testing.T) {
	root := t.TempDir()
	confDir := filepath.Join(root, ".starfleet-ai", "conf")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatal(err)
	}
	modelsYaml := `
models:
  - id: "nim-proxy/model-a"
    provider: "nim-proxy"
    label: "Model A"
    context: 8192
    caps: ["toolcall"]
  - id: "nim-proxy/model-b"
    provider: "nim-proxy"
    label: "Model B"
    context: 8192
    caps: ["toolcall"]
    disabled: true
  - id: "zen-proxy/model-c"
    provider: "zen-proxy"
    label: "Model C"
    context: 8192
    caps: ["toolcall"]
`
	if err := os.WriteFile(filepath.Join(confDir, "models.yaml"), []byte(modelsYaml), 0o644); err != nil {
		t.Fatal(err)
	}
	entries := loadCatalogEntries(root)["nim-proxy"]
	if len(entries) != 1 {
		t.Errorf("expected 1 nim-proxy entry, got %d", len(entries))
	}
	if entries[0].ID != "nim-proxy/model-a" {
		t.Errorf("expected model-a, got %s", entries[0].ID)
	}
}
