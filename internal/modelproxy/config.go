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
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"

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
	// Direct indicates this provider should be used directly (bypassing the
	// local proxy). When true, the provider's actual BaseURL and APIKey are
	// used in the generated opencode config instead of the proxy's endpoint
	// and the ship-specific key.
	Direct bool
	// ModelFilter controls which models are exposed. Options:
	// - "" or "all" (default): no filtering
	// - "free-only": auto-filter free models (zen-proxy: "-free" suffix;
	//   nim-proxy/nvidia: built-in known-free list)
	// - comma-separated list: explicit allowlist of model IDs
	ModelFilter string
	// Retry tuning (defaults applied).
	MaxRetries    int
	RetryDelayMS  int
	HoldTimeoutMS int // SSE keepalive hold timeout (ms) after retry budget exhausted; 0 = disabled
	// Type is the provider class, driving upstream-specific request
	// handling. Empty = generic OpenAI-compatible. See
	// Provider.applyUpstreamHeaders for the known types.
	Type string
	// UserAgent overrides the upstream request's User-Agent header
	// (type-dependent; mainly for "opencode-zen").
	UserAgent string
	// Capabilities forces capability flags (toolcall, temperature, ...) onto
	// every model this provider serves in the generated opencode config, for
	// upstreams whose catalog does not advertise them. See
	// Config.ModelProxyProvider.Capabilities.
	Capabilities []string
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
			ID:          strings.TrimSpace(p.ID),
			Name:        p.Name,
			BaseURL:     strings.TrimRight(expandEnv(p.BaseURL), "/"),
			APIKey:      expandEnv(p.APIKey),
			Direct:      p.Direct,
			ModelFilter: strings.TrimSpace(p.ModelFilter),
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
		prov.HoldTimeoutMS = p.HoldTimeoutMS
		if prov.HoldTimeoutMS <= 0 {
			prov.HoldTimeoutMS = 15000 // 15s default hold timeout
		}
		prov.Type = strings.TrimSpace(p.Type)
		prov.UserAgent = strings.TrimSpace(p.UserAgent)
		prov.Capabilities = append([]string(nil), p.Capabilities...) // copy, never alias the yaml slice
		out.Providers = append(out.Providers, prov)
		for r := range refs {
			out.EnvRefs = append(out.EnvRefs, r)
		}
	}
	return out, nil
}

// typeOpenCodeZen identifies the OpenCode Zen provider class (and is also
// accepted as a fallback when the provider id starts with "zen-", matching
// the pre-type name-based routing used before the Type field existed).
const typeOpenCodeZen = "opencode-zen"

// typeOpenCodeOllama identifies the Ollama provider class. Ollama serves a
// bare /v1/models catalog without capability metadata, so opencode would
// treat its models as tool-less by default — the type makes the generated
// config force-enable the capabilities that virtually all Ollama models
// support (see forcedModelCaps), especially tool calling.
const typeOpenCodeOllama = "ollama"

// defaultOllamaCapabilities is the capability set forced onto every model of
// an "ollama"-typed provider unless the provider overrides `capabilities:`.
// toolcall + temperature are universal across Ollama's llama-/qwen-/... based
// models; vision (attachment) and reasoning stay per-model opt-in via an
// explicit `capabilities:` list, since not every Ollama model supports them.
var defaultOllamaCapabilities = []string{"toolcall", "temperature"}

// forcedModelCaps returns the capability flags that should be force-enabled
// on every model entry of this provider in the generated opencode config:
// the provider's explicit `capabilities:` list, or — when empty — the type's
// default (Ollama models need tool calling etc. switched on explicitly).
func (p *Provider) forcedModelCaps() []string {
	if len(p.Capabilities) > 0 {
		return p.Capabilities
	}
	if p.Type == typeOpenCodeOllama {
		return defaultOllamaCapabilities
	}
	return nil
}

// defaultOpenCodeZenUserAgent is the request User-Agent OpenCode Zen expects
// of its official client ("opencode/<version>"). It satisfies the free-tier
// gate while allowing a provider-level user_agent override.
const defaultOpenCodeZenUserAgent = "opencode/1.18.30"

// applyUpstreamHeaders sets any provider-class-specific headers on an
// outgoing upstream request. from is the client's original request (may be
// nil, e.g. the health check has no client). Known types:
//
//	opencode-zen — OpenCode Zen. Its free tier only accepts requests that are
//	indistinguishable from the official opencode client: it validates the
//	User-Agent (must be "opencode/<version>") and requires the x-opencode-*
//	identity headers the client always sends (session/client/project).
//	Verified against the live gateway:
//	- only the opencode UA            -> HTTP 400 MissingSessionID
//	- identity headers, no opencode UA -> HTTP 429 FreeUsageLimitError
//	- opencode UA + session/client/project -> HTTP 200
//	The @ai-sdk/openai-compatible client that ships pointed at the proxy does
//	not send x-opencode-* itself, but it DOES send its real, per-conversation
//	session id as X-Session-Id — forward that verbatim as x-opencode-session
//	so Zen's per-session prefix cache actually hits across turns. Everything
//	the client doesn't provide is synthesized (fresh random request id per
//	request; a sticky bad session id would route every retry to a broken
//	upstream replica, see opencode issue #46011).
//
// Add new cases here as further per-provider requirements arise.
func (p *Provider) applyUpstreamHeaders(up *http.Request, from *http.Request) {
	zen := p.Type == typeOpenCodeZen || strings.HasPrefix(p.ID, "zen-")
	if !zen {
		return
	}
	// User-Agent: forward the client's opencode UA verbatim when it already
	// looks like the official client, otherwise use the override/default.
	if ua := headerFrom(from, "User-Agent"); ua != "" && strings.HasPrefix(ua, "opencode/") {
		up.Header.Set("User-Agent", ua)
	} else if p.UserAgent != "" {
		up.Header.Set("User-Agent", p.UserAgent)
	} else {
		up.Header.Set("User-Agent", defaultOpenCodeZenUserAgent)
	}
	// x-opencode-session: prefer the client's own session id (real one, keeps
	// the prefix cache warm), in either spelling it may arrive with.
	if v := headerFrom(from, "x-opencode-session"); v != "" {
		up.Header.Set("x-opencode-session", v)
	} else if v := headerFrom(from, "X-Session-Id"); v != "" {
		up.Header.Set("x-opencode-session", v)
	} else {
		up.Header.Set("x-opencode-session", "ses_"+randHex(16))
	}
	if v := headerFrom(from, "x-opencode-client"); v != "" {
		up.Header.Set("x-opencode-client", v)
	} else {
		up.Header.Set("x-opencode-client", "cli")
	}
	if v := headerFrom(from, "x-opencode-project"); v != "" {
		up.Header.Set("x-opencode-project", v)
	} else {
		up.Header.Set("x-opencode-project", "global")
	}
	if v := headerFrom(from, "x-opencode-request"); v != "" {
		up.Header.Set("x-opencode-request", v)
	} else {
		up.Header.Set("x-opencode-request", "msg_"+randHex(16))
	}
}

// headerFrom reads a header from the client request, tolerating a nil
// request (health-check probes have no client request).
func headerFrom(r *http.Request, key string) string {
	if r == nil {
		return ""
	}
	return r.Header.Get(key)
}

// randHex returns n random bytes encoded as lowercase hex, for synthesizing
// OpenCode Zen x-opencode-session / x-opencode-request ids.
func randHex(n int) string {
	b := make([]byte, n)
	if _, err := rand.Read(b); err != nil {
		// crypto/rand can only fail on a bespoke, non-portable setup; fall
		// back to a timestamp so the request is still well-formed.
		return strconv.FormatInt(time.Now().UnixNano(), 16)
	}
	return hex.EncodeToString(b)
}
