// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package modelproxy

import (
	"os"
	"path/filepath"
	"reflect"
	"sort"
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
