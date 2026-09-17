// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package modelproxy

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
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

// seedCheckConfig writes a model-proxy.yaml with one direct provider pointing
// at the given upstream, so RunCheck's catalog source (ProxyModelInfos) and
// served-set fetch both hit the same /v1/models endpoint.
func seedCheckConfig(t *testing.T, root, upstreamURL string, maxRetries int) {
	t.Helper()
	confDir := filepath.Join(root, ".starfleet-ai", "conf")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatal(err)
	}
	proxyYaml := "model_proxy:\n" +
		"  listen_addr: \"127.0.0.1:12345\"\n" +
		"  providers:\n" +
		"    - id: \"nim-proxy\"\n" +
		"      name: \"NVIDIA NIM\"\n" +
		"      base_url: \"" + upstreamURL + "\"\n" +
		"      api_key: \"test-key\"\n" +
		"      model_filter: \"all\"\n" +
		"      direct: true\n" +
		"      max_retries: " + strconv.Itoa(maxRetries) + "\n" +
		"      retry_delay_ms: 1\n"
	if err := os.WriteFile(filepath.Join(confDir, "model-proxy.yaml"), []byte(proxyYaml), 0o644); err != nil {
		t.Fatal(err)
	}
}

// upstreamServingModels returns an httptest server that serves /v1/models and
// (optionally) /chat/completions.
func upstreamServingModels(t *testing.T, chat func(w http.ResponseWriter, r *http.Request)) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/models":
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"data":[{"id":"model-a","object":"model"},{"id":"model-b","object":"model"}]}`))
		case "/chat/completions":
			if chat != nil {
				chat(w, r)
				return
			}
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusOK)
			w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	return srv
}

func TestRunCheck_ServedOK(t *testing.T) {
	upstream := upstreamServingModels(t, nil)
	defer upstream.Close()

	root := t.TempDir()
	seedCheckConfig(t, root, upstream.URL, 0)

	report, err := RunCheck(root, false)
	if err != nil {
		t.Fatalf("RunCheck: %v", err)
	}
	if report.OK != 2 {
		t.Errorf("OK=2, got %d", report.OK)
	}
	for _, mh := range report.Models {
		if mh.ID != "model-a" && mh.ID != "model-b" {
			t.Errorf("unexpected model id: %s", mh.ID)
		}
		if mh.Status != StatusOK {
			t.Errorf("model %s status: %s, want %s", mh.ID, mh.Status, StatusOK)
		}
		if !mh.Served {
			t.Errorf("model %s should be served", mh.ID)
		}
	}
}

func TestRunCheck_Probe_Success(t *testing.T) {
	upstream := upstreamServingModels(t, nil)
	defer upstream.Close()

	root := t.TempDir()
	seedCheckConfig(t, root, upstream.URL, 0)

	report, err := RunCheck(root, true)
	if err != nil {
		t.Fatalf("RunCheck: %v", err)
	}
	if report.OK != 2 {
		t.Errorf("OK=2, got %d", report.OK)
	}
	for _, mh := range report.Models {
		if mh.Status != StatusOK {
			t.Errorf("model %s status: %s, want %s", mh.ID, mh.Status, StatusOK)
		}
		if mh.Retries != 0 {
			t.Errorf("model %s retries should be 0, got %d", mh.ID, mh.Retries)
		}
	}
}

func TestRunCheck_Probe_HardFail_404(t *testing.T) {
	upstream := upstreamServingModels(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte(`{"error":{"message":"Not found for account","type":"not_found"}}`))
	})
	defer upstream.Close()

	root := t.TempDir()
	seedCheckConfig(t, root, upstream.URL, 0)

	report, err := RunCheck(root, true)
	if err != nil {
		t.Fatalf("RunCheck: %v", err)
	}
	if report.Failed != 2 {
		t.Errorf("Failed=2, got %d", report.Failed)
	}
	for _, mh := range report.Models {
		if mh.Status != StatusFailed {
			t.Errorf("model %s status: %s, want %s", mh.ID, mh.Status, StatusFailed)
		}
		if !strings.Contains(mh.Detail, "HTTP 404") {
			t.Errorf("detail should contain HTTP 404: %s", mh.Detail)
		}
	}
}

func TestRunCheck_Probe_TransientRetries_ThenOK(t *testing.T) {
	attempts := map[string]int{}
	var attemptsMu sync.Mutex
	upstream := upstreamServingModels(t, func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Model string `json:"model"`
		}
		json.NewDecoder(r.Body).Decode(&req)
		attemptsMu.Lock()
		attempts[req.Model]++
		n := attempts[req.Model]
		attemptsMu.Unlock()
		if n <= 2 {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusTooManyRequests)
			w.Write([]byte(`{"error":{"message":"ResourceExhausted: rate limit","type":"rate_limit"}}`))
			return
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write([]byte(`{"choices":[{"message":{"content":"pong"}}]}`))
	})
	defer upstream.Close()

	root := t.TempDir()
	seedCheckConfig(t, root, upstream.URL, 2)

	report, err := RunCheck(root, true)
	if err != nil {
		t.Fatalf("RunCheck: %v", err)
	}
	if report.OK != 2 {
		t.Errorf("OK=2, got %d", report.OK)
	}
	for _, mh := range report.Models {
		if mh.Status != StatusOK {
			t.Errorf("model %s status: %s, want %s", mh.ID, mh.Status, StatusOK)
		}
		if mh.Retries != 2 {
			t.Errorf("model %s retries should be 2, got %d", mh.ID, mh.Retries)
		}
	}
}

func TestRunCheck_Probe_TransientExhausted_Degraded(t *testing.T) {
	upstream := upstreamServingModels(t, func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		w.Write([]byte(`{"error":{"message":"ResourceExhausted: rate limit","type":"rate_limit"}}`))
	})
	defer upstream.Close()

	root := t.TempDir()
	seedCheckConfig(t, root, upstream.URL, 2)

	report, err := RunCheck(root, true)
	if err != nil {
		t.Fatalf("RunCheck: %v", err)
	}
	if report.Degraded != 2 {
		t.Errorf("Degraded=2, got %d", report.Degraded)
	}
	for _, mh := range report.Models {
		if mh.Status != StatusDegraded {
			t.Errorf("model %s status: %s, want %s", mh.ID, mh.Status, StatusDegraded)
		}
		if mh.Retries != 2 {
			t.Errorf("model %s retries should be 2, got %d", mh.ID, mh.Retries)
		}
		if !strings.HasPrefix(mh.Detail, "transient:") {
			t.Errorf("detail should be transient-prefixed: %s", mh.Detail)
		}
	}
}
