// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package modelproxy

import (
	"bufio"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
)

// testProxy builds a Proxy pointing at a single upstream test server that
// serves /models and /chat/completions via the given handler.
func testProxy(t *testing.T, upstream http.Handler) (*Proxy, *httptest.Server) {
	t.Helper()
	srv := httptest.NewServer(upstream)
	t.Cleanup(srv.Close)
	cfg := &Config{
		ListenAddr: "127.0.0.1:1",
		Providers: []Provider{{
			ID:           "test",
			Name:         "Test",
			BaseURL:      srv.URL,
			APIKey:       "k",
			MaxRetries:   2,
			RetryDelayMS: 1,
		}},
	}
	return New(cfg), srv
}

// modelsHandler serves a fixed model catalog.
func modelsHandler(models ...string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			data := make([]map[string]string, 0, len(models))
			for _, id := range models {
				data = append(data, map[string]string{"id": id})
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"data": data})
			return
		}
		w.WriteHeader(404)
	})
}

func postChat(t *testing.T, p *Proxy, model string, stream bool) *http.Response {
	return postChatAuth(t, p, model, stream, "")
}

func postChatAuth(t *testing.T, p *Proxy, model string, stream bool, auth string) *http.Response {
	t.Helper()
	body, _ := json.Marshal(map[string]any{"model": model, "messages": []any{}, "stream": stream})
	req := httptest.NewRequest(http.MethodPost, "/v1/chat/completions", strings.NewReader(string(body)))
	if auth != "" {
		req.Header.Set("Authorization", "Bearer "+auth)
	}
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)
	return w.Result()
}

func getShips(t *testing.T, p *Proxy) []ShipStats {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, "/v1/ships", nil)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)
	var out struct {
		Ships []ShipStats `json:"ships"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode ships: %v", err)
	}
	return out.Ships
}

func TestModelsConsolidated(t *testing.T) {
	p, _ := testProxy(t, modelsHandler("m-a", "m-b"))
	req := httptest.NewRequest(http.MethodGet, "/v1/models", nil)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode: %v", err)
	}
	seen := map[string]bool{}
	for _, m := range out.Data {
		seen[m.ID] = true
	}
	if !seen["m-a"] || !seen["m-b"] {
		t.Fatalf("models = %v, want m-a and m-b", out.Data)
	}
}

func TestChatRoutesToUpstream(t *testing.T) {
	var gotAuth string
	var gotModel string
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m1"}}})
			return
		}
		gotAuth = r.Header.Get("Authorization")
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotModel = body.Model
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "resp", "choices": []any{}})
	})
	p, _ := testProxy(t, upstream)
	resp := postChat(t, p, "m1", false)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if gotAuth != "Bearer k" {
		t.Fatalf("authorization = %q, want Bearer k", gotAuth)
	}
	if gotModel != "m1" {
		t.Fatalf("upstream model = %q, want m1", gotModel)
	}
}

func TestChatPrefixRouting(t *testing.T) {
	var gotModel string
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m1"}}})
			return
		}
		var body struct {
			Model string `json:"model"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		gotModel = body.Model
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "resp"})
	})
	p, _ := testProxy(t, upstream)
	resp := postChat(t, p, "test/m1", false)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	if gotModel != "m1" {
		t.Fatalf("upstream model = %q, want m1 (prefix stripped)", gotModel)
	}
}

func TestChatUnknownModel(t *testing.T) {
	p, _ := testProxy(t, modelsHandler("m1"))
	resp := postChat(t, p, "nope", false)
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
}

func TestChatRetriesTransient(t *testing.T) {
	var calls atomic.Int32
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m1"}}})
			return
		}
		if calls.Add(1) == 1 {
			w.WriteHeader(http.StatusServiceUnavailable)
			_, _ = io.WriteString(w, `{"error":{"message":"transient"}}`)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{"id": "ok"})
	})
	p, _ := testProxy(t, upstream)
	resp := postChat(t, p, "m1", false)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200 after retry (calls=%d)", resp.StatusCode, calls.Load())
	}
	if calls.Load() != 2 {
		t.Fatalf("upstream calls = %d, want 2", calls.Load())
	}
}

func TestChatNonTransientNotRetried(t *testing.T) {
	var calls atomic.Int32
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m1"}}})
			return
		}
		calls.Add(1)
		w.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(w, `{"error":{"message":"bad request"}}`)
	})
	p, _ := testProxy(t, upstream)
	resp := postChat(t, p, "m1", false)
	if resp.StatusCode != 400 {
		t.Fatalf("status = %d, want 400", resp.StatusCode)
	}
	if calls.Load() != 1 {
		t.Fatalf("upstream calls = %d, want 1", calls.Load())
	}
}

func TestChatStreamingPassthrough(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m1"}}})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		f.Flush()
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		f.Flush()
	})
	p, _ := testProxy(t, upstream)
	resp := postChat(t, p, "m1", true)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	if !strings.Contains(string(body), "data: [DONE]") {
		t.Fatalf("missing [DONE] sentinel in stream: %q", string(body))
	}
}

// TestChatStreamingInterrupted simulates an upstream that cuts the connection
// mid-stream (after real chunks, before [DONE]). The proxy must emit a
// structured error event + [DONE] instead of returning a silently truncated
// stream.
func TestChatStreamingInterrupted(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m1"}}})
			return
		}
		hj := w.(http.Hijacker)
		conn, buf, err := hj.Hijack()
		if err != nil {
			t.Errorf("hijack: %v", err)
			return
		}
		defer conn.Close()
		_, _ = fmt.Fprint(buf, "HTTP/1.1 200 OK\r\nContent-Type: text/event-stream\r\n\r\n")
		_, _ = fmt.Fprint(buf, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		_ = buf.Flush()
		conn.Close()
	})
	p, _ := testProxy(t, upstream)
	resp := postChat(t, p, "m1", true)
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	body, _ := io.ReadAll(resp.Body)
	s := string(body)
	if !strings.Contains(s, "stream_interrupted") {
		t.Fatalf("missing error event after interrupted stream: %q", s)
	}
	if !strings.Contains(s, "data: [DONE]") {
		t.Fatalf("missing [DONE] after interrupted stream: %q", s)
	}
}

func TestHealth(t *testing.T) {
	p, _ := testProxy(t, modelsHandler())
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	w := httptest.NewRecorder()
	p.ServeHTTP(w, req)
	if w.Code != 200 {
		t.Fatalf("status = %d, want 200", w.Code)
	}
}

func TestShipKeyFormat(t *testing.T) {
	k := ShipKey("Phoenix")
	if !strings.HasPrefix(k, "mp-Phoenix-") || len(k) <= len("mp-Phoenix-") {
		t.Fatalf("ShipKey = %q, want mp-Phoenix-<token>", k)
	}
	if k == ShipKey("Phoenix") {
		t.Fatalf("ShipKey must be unique per call")
	}
}

func TestShipFromRequest(t *testing.T) {
	cases := []struct {
		auth string
		want string
	}{
		{"Bearer mp-Phoenix-aabbcc", "Phoenix"},
		{"mp-Phoenix-aabbcc", "Phoenix"},
		{"Bearer starfleet-model-proxy", "unknown"},
		{"", "unknown"},
		{"Bearer mp-", "unknown"},
	}
	for _, c := range cases {
		if got := shipFromRequest(c.auth); got != c.want {
			t.Errorf("shipFromRequest(%q) = %q, want %q", c.auth, got, c.want)
		}
	}
}

// TestShipsTracking verifies that chat requests carrying a per-ship apiKey are
// attributed to that ship in /v1/ships, including token usage from the
// response.
func TestShipsTracking(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m1"}}})
			return
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(map[string]any{
			"id":      "resp",
			"choices": []any{map[string]any{"message": map[string]any{"role": "assistant", "content": "hi"}}},
			"usage":   map[string]any{"prompt_tokens": 10, "completion_tokens": 5},
		})
	})
	p, _ := testProxy(t, upstream)

	resp := postChatAuth(t, p, "m1", false, ShipKey("Phoenix"))
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	ships := getShips(t, p)
	if len(ships) != 1 {
		t.Fatalf("ships = %d, want 1: %+v", len(ships), ships)
	}
	s := ships[0]
	if s.ShipID != "Phoenix" {
		t.Fatalf("ship = %q, want Phoenix", s.ShipID)
	}
	if s.TotalRequests != 1 || s.Successes != 1 || s.Failures != 0 {
		t.Fatalf("counters = %+v", s)
	}
	if s.PromptTokens != 10 || s.CompletionTokens != 5 {
		t.Fatalf("tokens = %d/%d, want 10/5", s.PromptTokens, s.CompletionTokens)
	}
	if s.ByProvider["test"] != 1 || s.ByModel["m1"] != 1 {
		t.Fatalf("by provider/model = %+v / %+v", s.ByProvider, s.ByModel)
	}
}

// TestShipsTrackingStreaming verifies usage is picked up from the trailing
// usage chunk of a streaming response.
func TestShipsTrackingStreaming(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m1"}}})
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		f, _ := w.(http.Flusher)
		_, _ = io.WriteString(w, "data: {\"choices\":[{\"delta\":{\"content\":\"hi\"}}]}\n\n")
		f.Flush()
		_, _ = io.WriteString(w, "data: {\"choices\":[],\"usage\":{\"prompt_tokens\":3,\"completion_tokens\":2}}\n\n")
		f.Flush()
		_, _ = io.WriteString(w, "data: [DONE]\n\n")
		f.Flush()
	})
	p, _ := testProxy(t, upstream)

	resp := postChatAuth(t, p, "m1", true, ShipKey("Voyager"))
	if resp.StatusCode != 200 {
		t.Fatalf("status = %d, want 200", resp.StatusCode)
	}
	ships := getShips(t, p)
	if len(ships) != 1 {
		t.Fatalf("ships = %d, want 1", len(ships))
	}
	s := ships[0]
	if s.ShipID != "Voyager" {
		t.Fatalf("ship = %q, want Voyager", s.ShipID)
	}
	if s.PromptTokens != 3 || s.CompletionTokens != 2 {
		t.Fatalf("tokens = %d/%d, want 3/2", s.PromptTokens, s.CompletionTokens)
	}
	if s.Failures != 0 || s.Successes != 1 {
		t.Fatalf("counters = %+v", s)
	}
}

// TestShipsTrackingFailure verifies failed requests increment the failure
// counter (and no token usage is recorded).
func TestShipsTrackingFailure(t *testing.T) {
	upstream := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/models" {
			_ = json.NewEncoder(w).Encode(map[string]any{"data": []map[string]string{{"id": "m1"}}})
			return
		}
		w.WriteHeader(http.StatusServiceUnavailable)
		_, _ = io.WriteString(w, `{"error":{"message":"down"}}`)
	})
	p, _ := testProxy(t, upstream)

	resp := postChatAuth(t, p, "m1", false, ShipKey("Reliant"))
	// After exhausting retries (test provider MaxRetries=2 → 3 attempts),
	// the proxy answers 503.
	if resp.StatusCode != 503 {
		t.Fatalf("status = %d, want 503", resp.StatusCode)
	}
	ships := getShips(t, p)
	if len(ships) != 1 {
		t.Fatalf("ships = %d, want 1", len(ships))
	}
	s := ships[0]
	if s.Failures != 1 || s.Successes != 0 {
		t.Fatalf("counters = %+v, want failures=1", s)
	}
	if s.Retries != 2 {
		t.Fatalf("retries = %d, want 2", s.Retries)
	}
}

var _ = bufio.NewReader // keep import if refactored
