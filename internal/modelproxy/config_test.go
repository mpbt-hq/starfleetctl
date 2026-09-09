// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package modelproxy

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

func TestExpandEnv(t *testing.T) {
	os.Setenv("MP_TEST_A", "alice")
	os.Setenv("MP_TEST_B", "bob")
	defer os.Unsetenv("MP_TEST_A")
	defer os.Unsetenv("MP_TEST_B")

	cases := []struct {
		in   string
		want string
	}{
		{"{env:MP_TEST_A}", "alice"},
		{"${MP_TEST_B}", "bob"},
		{"$MP_TEST_A-$MP_TEST_B", "alice-bob"},
		{"x{env:MP_TEST_A}y", "xalicey"},
		{"{env:MP_TEST_NOPE}", ""},
		{"plain", "plain"},
	}
	for _, c := range cases {
		if got := expandEnv(c.in); got != c.want {
			t.Errorf("expandEnv(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func TestExtractEnvRefs(t *testing.T) {
	refs := map[string]bool{}
	extractEnvRefs("{env:AAA} ${BBB} $CCC plain {env:AAA}", refs)
	got := []string{}
	for r := range refs {
		got = append(got, r)
	}
	sort.Strings(got)
	if !reflect.DeepEqual(got, []string{"AAA", "BBB", "CCC"}) {
		t.Fatalf("refs = %v, want [AAA BBB CCC]", got)
	}
}

func TestLoadEnvExpansionAndPaths(t *testing.T) {
	os.Setenv("MP_TEST_KEY", "sekret")
	defer os.Unsetenv("MP_TEST_KEY")

	root := t.TempDir()
	confDir := filepath.Join(root, ".starfleet-ai", "conf")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatal(err)
	}
	yaml := `
model_proxy:
  listen_addr: "127.0.0.1:9999"
  providers:
    - id: nim
      base_url: "https://integrate.api.nvidia.com/v1"
      api_key: "{env:MP_TEST_KEY}"
      max_retries: 5
      retry_delay_ms: 250
`
	if err := os.WriteFile(filepath.Join(confDir, "model-proxy.yaml"), []byte(yaml), 0o644); err != nil {
		t.Fatal(err)
	}

	cfg, err := Load(root)
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ListenAddr != "127.0.0.1:9999" {
		t.Fatalf("listen_addr = %q", cfg.ListenAddr)
	}
	if len(cfg.Providers) != 1 {
		t.Fatalf("providers = %d, want 1", len(cfg.Providers))
	}
	p := cfg.Providers[0]
	if p.APIKey != "sekret" {
		t.Fatalf("api_key = %q, want sekret (env expanded)", p.APIKey)
	}
	if p.MaxRetries != 5 || p.RetryDelayMS != 250 {
		t.Fatalf("retry = %d/%d, want 5/250", p.MaxRetries, p.RetryDelayMS)
	}
	if cfg.PIDFile != filepath.Join(root, ".starfleet-ai", "var", "model-proxy.pid") {
		t.Fatalf("pidfile = %q", cfg.PIDFile)
	}
	if !reflect.DeepEqual(cfg.EnvRefs, []string{"MP_TEST_KEY"}) {
		t.Fatalf("env refs = %v, want [MP_TEST_KEY]", cfg.EnvRefs)
	}
}

func TestLoadNoConfigFile(t *testing.T) {
	cfg, err := Load(t.TempDir())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if cfg.ListenAddr != DefaultListenAddr {
		t.Fatalf("listen_addr = %q, want default %q", cfg.ListenAddr, DefaultListenAddr)
	}
	if len(cfg.Providers) != 0 {
		t.Fatalf("providers = %d, want 0", len(cfg.Providers))
	}
}

func TestApplyUpstreamHeaders(t *testing.T) {
	tests := []struct {
		name          string
		prov          Provider
		from          map[string]string // client request headers ("" = absent)
		wantUA        string
		wantSession   string
		wantClient    string
		wantProject   string
		wantReqPrefix string
	}{
		{
			name:   "generic provider untouched",
			prov:   Provider{ID: "nim-proxy", Type: ""},
			from:   map[string]string{"User-Agent": "opencode/1.18.30"},
			wantUA: "",
		},
		{
			name:        "opencode-zen forwards client session id as x-opencode-session",
			prov:        Provider{ID: "zen-proxy", Type: "opencode-zen"},
			from:        map[string]string{"X-Session-Id": "ses_realid", "User-Agent": "opencode/1.18.30"},
			wantUA:      "opencode/1.18.30",
			wantSession: "ses_realid",
			wantClient:  "cli",
			wantProject: "global",
		},
		{
			name:        "opencode-zen prefers explicit x-opencode-session over X-Session-Id",
			prov:        Provider{ID: "zen-proxy", Type: "opencode-zen"},
			from:        map[string]string{"x-opencode-session": "ses_explicit", "X-Session-Id": "ses_client"},
			wantUA:      "opencode/1.18.30",
			wantSession: "ses_explicit",
			wantClient:  "cli",
			wantProject: "global",
		},
		{
			name:        "no client request synthesizes session and default UA",
			prov:        Provider{ID: "zen-proxy", Type: "opencode-zen"},
			from:        nil,
			wantUA:      "opencode/1.18.30",
			wantSession: "ses_",
			wantClient:  "cli",
			wantProject: "global",
		},
		{
			name:        "non-opencode client UA falls back to zen UA",
			prov:        Provider{ID: "zen-proxy", Type: "opencode-zen"},
			from:        map[string]string{"User-Agent": "curl/8.0"},
			wantUA:      "opencode/1.18.30",
			wantSession: "ses_",
			wantClient:  "cli",
			wantProject: "global",
		},
		{
			name:        "user_agent override wins over default",
			prov:        Provider{ID: "zen-proxy", Type: "opencode-zen", UserAgent: "opencode/9.9.9"},
			from:        map[string]string{"User-Agent": "curl/8.0"},
			wantUA:      "opencode/9.9.9",
			wantSession: "ses_",
			wantClient:  "cli",
			wantProject: "global",
		},
		{
			name:        "legacy zen- prefixed id still classified",
			prov:        Provider{ID: "zen-proxy"},
			from:        nil,
			wantUA:      "opencode/1.18.30",
			wantSession: "ses_",
			wantClient:  "cli",
			wantProject: "global",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			up := httptest.NewRequest(http.MethodPost, "/v1", nil)
			var from *http.Request
			if tt.from != nil {
				from = httptest.NewRequest(http.MethodPost, "/v1", nil)
				for k, v := range tt.from {
					from.Header.Set(k, v)
				}
			}
			tt.prov.applyUpstreamHeaders(up, from)

			if got := up.Header.Get("User-Agent"); got != tt.wantUA {
				t.Errorf("User-Agent = %q, want %q", got, tt.wantUA)
			}

			zen := tt.prov.Type == "opencode-zen" || strings.HasPrefix(tt.prov.ID, "zen-")
			if !zen {
				return // generic providers must not touch zen headers
			}

			if tt.wantSession == "ses_" {
				// synthesized session ids are random: only check the prefix
				if got := up.Header.Get("x-opencode-session"); !strings.HasPrefix(got, "ses_") || len(got) < 5 {
					t.Errorf("x-opencode-session = %q, want random ses_ id", got)
				}
			} else if got := up.Header.Get("x-opencode-session"); got != tt.wantSession {
				t.Errorf("x-opencode-session = %q, want %q", got, tt.wantSession)
			}
			if got := up.Header.Get("x-opencode-client"); got != tt.wantClient {
				t.Errorf("x-opencode-client = %q, want %q", got, tt.wantClient)
			}
			if got := up.Header.Get("x-opencode-project"); got != tt.wantProject {
				t.Errorf("x-opencode-project = %q, want %q", got, tt.wantProject)
			}
			if got := up.Header.Get("x-opencode-request"); !strings.HasPrefix(got, "msg_") {
				t.Errorf("x-opencode-request = %q, want msg_ prefixed", got)
			}
		})
	}
}

func TestForcedModelCaps(t *testing.T) {
	tests := []struct {
		name string
		prov Provider
		want []string
	}{
		{
			name: "generic provider has no forced caps",
			prov: Provider{ID: "nim-proxy"},
			want: nil,
		},
		{
			name: "ollama type applies default caps",
			prov: Provider{ID: "ollama", Type: typeOpenCodeOllama},
			want: []string{"toolcall", "temperature"},
		},
		{
			name: "explicit capabilities override the ollama default",
			prov: Provider{ID: "ollama", Type: typeOpenCodeOllama, Capabilities: []string{"toolcall"}},
			want: []string{"toolcall"},
		},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := tt.prov.forcedModelCaps()
			if !reflect.DeepEqual(got, tt.want) {
				t.Errorf("forcedModelCaps() = %v, want %v", got, tt.want)
			}
		})
	}
}

func TestModelEntryFor(t *testing.T) {
	// A bare catalog entry (no caps) — what Ollama serves via /v1/models.
	bare := modelEntryFor(ModelInfo{ID: "qwen3:0.6b"}, nil)
	if _, ok := bare["tool_call"]; ok {
		t.Errorf("bare entry without forced caps unexpectedly has tool_call")
	}

	// The same bare entry with forced caps must get them switched on.
	ollama := modelEntryFor(ModelInfo{ID: "qwen3:0.6b"}, defaultOllamaCapabilities)
	for _, field := range []string{"tool_call", "temperature"} {
		if v, ok := ollama[field]; !ok || v != true {
			t.Errorf("ollama entry %s = %v (present %v), want true", field, v, ok)
		}
	}

	// Forced caps merge with catalog caps without duplicates (toolcall from
	// both sides) and never downgrade existing flags.
	inf := ModelInfo{ID: "m", Caps: []string{"toolcall", "reasoning"}}
	got := modelEntryFor(inf, []string{"toolcall", "attachment"})
	for _, field := range []string{"tool_call", "reasoning", "attachment"} {
		if v, ok := got[field]; !ok || v != true {
			t.Errorf("merged entry %s = %v (present %v), want true", field, v, ok)
		}
	}

	// A model-proxy-yaml capabilities list (without the ollama type) works too.
	prov := Provider{ID: "x", Capabilities: []string{"toolcall"}}
	direct := modelEntryFor(ModelInfo{ID: "x"}, prov.forcedModelCaps())
	if v, ok := direct["tool_call"]; !ok || v != true {
		t.Errorf("provider-capabilities entry tool_call = %v (present %v), want true", v, ok)
	}
}
