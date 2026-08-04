// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package modelproxy implements a local OpenAI-compatible proxy in front of
// the real model API backends (NVIDIA NIM, OpenCode Zen, ...). Ships talk to
// this single local endpoint instead of the flaky upstreams: the proxy retries
// transient errors (429/5xx/conn-reset, and gRPC-style saturation errors such
// as ResourceExhausted) and catches streaming failures so a model-API hiccup
// never leaks raw into the agent session.
package modelproxy

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"

	"github.com/metux/starfleetctl/internal/config"
)

var envRefRe = regexp.MustCompile(`\$\{([A-Za-z_][A-Za-z0-9_]*)\}|\$([A-Za-z_][A-Za-z0-9_]*)`)

// DefaultListenAddr is used when the config file sets no listen_addr.
const DefaultListenAddr = "127.0.0.1:8443"

// expandEnv replaces env references in a config value. Two syntaxes are
// supported so it works both with opencode-style references ({env:VAR}) and
// classic shell-style references (${VAR} or $VAR). Missing variables expand
// to the empty string; the caller decides whether that is an error.
func expandEnv(s string) string {
	s = os.Expand(s, func(k string) string { return os.Getenv(k) })
	for {
		start := strings.Index(s, "{env:")
		if start < 0 {
			break
		}
		end := strings.Index(s[start:], "}")
		if end < 0 {
			break
		}
		key := s[start+5 : start+end]
		val := os.Getenv(key)
		s = s[:start] + val + s[start+end+1:]
	}
	return s
}

// Config is the fully resolved model-proxy configuration (env expanded).
type Config struct {
	ListenAddr string
	PIDFile    string
	LogFile    string
	Providers  []Provider
	// EnvRefs lists the env var names referenced via {env:VAR}/${VAR}/$VAR in
	// the raw config (as they appeared before expansion). Used by the daemon
	// to pick up missing keys from the user's opencode config. Not serialized.
	EnvRefs []string
}

// extractEnvRefs collects the env var names referenced in a raw config value.
// Both passes run independently so neither consumes input the other needs.
func extractEnvRefs(s string, out map[string]bool) {
	// {env:NAME}
	rest := s
	for {
		idx := strings.Index(rest, "{env:")
		if idx < 0 {
			break
		}
		start := idx + 5
		end := strings.IndexByte(rest[start:], '}')
		if end < 0 {
			break
		}
		out[rest[start:start+end]] = true
		rest = rest[start+end+1:]
	}
	// $VAR / ${VAR} — over the original string.
	for _, m := range envRefRe.FindAllStringSubmatch(s, -1) {
		if m[1] != "" {
			out[m[1]] = true
		} else if m[2] != "" {
			out[m[2]] = true
		}
	}
}

// Provider is one resolved upstream backend.
type Provider struct {
	ID      string
	Name    string
	BaseURL string
	APIKey  string
	// Retry tuning (defaults applied).
	MaxRetries   int
	RetryDelayMS int
}

// Load reads and resolves the model-proxy configuration for the workspace
// root. A missing config file is not an error: it returns the default
// listen address with no providers (the proxy then serves nothing).
func Load(root string) (*Config, error) {
	cfg, err := config.Load(root)
	if err != nil {
		return nil, err
	}
	mp := cfg.ModelProxy
	out := &Config{
		ListenAddr: mp.ListenAddr,
		PIDFile:    mp.PIDFile,
		LogFile:    mp.LogFile,
	}
	if out.ListenAddr == "" {
		out.ListenAddr = DefaultListenAddr
	}
	if out.PIDFile == "" {
		out.PIDFile = ".starfleet-ai/var/model-proxy.pid"
	}
	if out.LogFile == "" {
		out.LogFile = ".starfleet-ai/var/log/model-proxy.log"
	}
	// Normalize PID/log paths relative to the workspace root.
	if !filepath.IsAbs(out.PIDFile) {
		out.PIDFile = filepath.Join(root, out.PIDFile)
	}
	if !filepath.IsAbs(out.LogFile) {
		out.LogFile = filepath.Join(root, out.LogFile)
	}

	for _, p := range mp.Providers {
		refs := map[string]bool{}
		extractEnvRefs(p.APIKey, refs)
		extractEnvRefs(p.BaseURL, refs)
		prov := Provider{
			ID:      strings.TrimSpace(p.ID),
			Name:    p.Name,
			BaseURL: strings.TrimRight(expandEnv(p.BaseURL), "/"),
			APIKey:  expandEnv(p.APIKey),
		}
		if prov.ID == "" {
			return nil, fmt.Errorf("model-proxy: provider without id in config")
		}
		if prov.Name == "" {
			prov.Name = prov.ID
		}
		if prov.BaseURL == "" {
			return nil, fmt.Errorf("model-proxy: provider %q has no base_url", prov.ID)
		}
		prov.MaxRetries = p.MaxRetries
		if prov.MaxRetries <= 0 {
			prov.MaxRetries = 3
		}
		prov.RetryDelayMS = p.RetryDelayMS
		if prov.RetryDelayMS <= 0 {
			prov.RetryDelayMS = 1000
		}
		out.Providers = append(out.Providers, prov)
		for r := range refs {
			out.EnvRefs = append(out.EnvRefs, r)
		}
	}
	return out, nil
}
