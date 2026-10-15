// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package modelproxy

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"regexp"
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
	Strategies []config.ModelProxyStrategy
	// Health is the resolved automated health check configuration.
	Health HealthConfig
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
	// Timeout is the per-request HTTP timeout for upstream calls (streaming & non-streaming).
	// If set, overrides the default (30s non-streaming, 15s for opencode-zen streaming).
	Timeout time.Duration
	// ResponseHeaderTimeout is the time allowed for the upstream to send response
	// headers after the connection is established. If set, overrides the default
	// (30s for all providers). This is useful for slow upstream providers.
	ResponseHeaderTimeout time.Duration
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

	// Health check configuration.
	HealthInterval string
	HealthTimeout  string
	HealthEndpoint string
}

// HealthConfig is the resolved automated health check configuration with
// defaults applied.
type HealthConfig struct {
	// Interval is how often the background check runs. Zero disables it.
	Interval time.Duration
	// Timeout is the per-probe HTTP timeout. Default 30s.
	Timeout time.Duration
	// Parallel is the number of concurrent probes. Default 4.
	Parallel int
	// CapabilityProbe enables the agent-capability probe. Default true.
	CapabilityProbe bool
	// Endpoint is the proxy route serving the persisted state.
	Endpoint string
}

// DefaultHealthConfig returns the defaults for the automated health check.
func DefaultHealthConfig() HealthConfig {
	return HealthConfig{
		Interval:        30 * time.Minute,
		Timeout:         30 * time.Second,
		Parallel:        4,
		CapabilityProbe: true,
		Endpoint:        "/v1/model-health",
	}
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

	// Resolve the automated health check configuration (defaults + overrides).
	out.Health = DefaultHealthConfig()
	hc := mp.Health
	if hc.Interval != "" {
		if d, err := time.ParseDuration(hc.Interval); err == nil {
			out.Health.Interval = d
		}
	}
	if hc.Timeout != "" {
		if d, err := time.ParseDuration(hc.Timeout); err == nil {
			out.Health.Timeout = d
		}
	}
	if hc.Parallel > 1 {
		out.Health.Parallel = hc.Parallel
	}
	if hc.Endpoint != "" {
		out.Health.Endpoint = hc.Endpoint
	}
	// Bool: yaml cannot distinguish "false" from unset via the plain bool
	// field, but for an opt-in feature defaulting to true, explicitly setting
	// false means: disable the capability probe. yaml bool field zero-value is
	// false, which would wrongly disable it — so use a *bool to detect unset.
	// The raw YAML node is decoded via the same struct, so we read the
	// pointer field. (ModelProxyHealthConfig uses *bool.)
	{
		cb := mp.Health.CapabilityProbe
		if cb != nil && !*cb {
			out.Health.CapabilityProbe = false
		}
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
		prov.Type = strings.TrimSpace(p.Type)
		// A virtual provider (type "meta-model") has no real upstream — its
		// catalog is synthesized from the configured strategies, so it needs
		// neither a base_url nor an upstream query.
		if prov.BaseURL == "" && !prov.isVirtual() {
			return nil, fmt.Errorf("model-proxy: provider %q has no base_url", prov.ID)
		}
		prov.MaxRetries = p.MaxRetries
		if prov.MaxRetries < 0 {
			prov.MaxRetries = 3
		}
		prov.RetryDelayMS = p.RetryDelayMS
		if prov.RetryDelayMS <= 0 {
			prov.RetryDelayMS = 1000
		}
		prov.HoldTimeoutMS = p.HoldTimeoutMS
		if prov.HoldTimeoutMS < 0 {
			prov.HoldTimeoutMS = 15000 // 15s default hold timeout
		}
		prov.Timeout = p.Timeout
		if prov.Timeout <= 0 {
			if prov.Type == "opencode-zen" {
				prov.Timeout = 15 * time.Second
			} else {
				prov.Timeout = 30 * time.Second
			}
		}
		prov.ResponseHeaderTimeout = p.ResponseHeaderTimeout
		if prov.ResponseHeaderTimeout <= 0 {
			prov.ResponseHeaderTimeout = 30 * time.Second
		}
		prov.UserAgent = strings.TrimSpace(p.UserAgent)
		prov.Capabilities = append([]string(nil), p.Capabilities...) // copy, never alias the yaml slice
		out.Providers = append(out.Providers, prov)
		for r := range refs {
			out.EnvRefs = append(out.EnvRefs, r)
		}
	}

	// Merge direct providers from fleet.yaml (bypass the local proxy)
	for _, p := range cfg.Fleet.DirectProviders {
		refs := map[string]bool{}
		extractEnvRefs(p.APIKey, refs)
		extractEnvRefs(p.BaseURL, refs)
		prov := Provider{
			ID:          strings.TrimSpace(p.ID),
			Name:        p.Name,
			BaseURL:     strings.TrimRight(expandEnv(p.BaseURL), "/"),
			APIKey:      expandEnv(p.APIKey),
			Direct:      true, // Always true for fleet.yaml direct_providers
			ModelFilter: strings.TrimSpace(p.ModelFilter),
		}
		if prov.ID == "" {
			return nil, fmt.Errorf("model-proxy: direct provider without id in fleet.yaml")
		}
		if prov.Name == "" {
			prov.Name = prov.ID
		}
		prov.Type = strings.TrimSpace(p.Type)
		if prov.BaseURL == "" && !prov.isVirtual() {
			return nil, fmt.Errorf("model-proxy: direct provider %q has no base_url", prov.ID)
		}
		prov.MaxRetries = p.MaxRetries
		if prov.MaxRetries < 0 {
			prov.MaxRetries = 3
		}
		prov.RetryDelayMS = p.RetryDelayMS
		if prov.RetryDelayMS <= 0 {
			prov.RetryDelayMS = 1000
		}
		prov.HoldTimeoutMS = p.HoldTimeoutMS
		if prov.HoldTimeoutMS < 0 {
			prov.HoldTimeoutMS = 15000
		}
		prov.Timeout = p.Timeout
		if prov.Timeout <= 0 {
			if prov.Type == "opencode-zen" {
				prov.Timeout = 15 * time.Second
			} else {
				prov.Timeout = 30 * time.Second
			}
		}
		prov.UserAgent = strings.TrimSpace(p.UserAgent)
		prov.Capabilities = append([]string(nil), p.Capabilities...)
		out.Providers = append(out.Providers, prov)
		for r := range refs {
			out.EnvRefs = append(out.EnvRefs, r)
		}
	}

	// Parse strategies
	for _, s := range mp.Strategies {
		strat := config.ModelProxyStrategy{
			ID:           strings.TrimSpace(s.ID),
			Description:  s.Description,
			DefaultModel: strings.TrimSpace(s.DefaultModel),
			Models:       s.Models,
			Strategy:     s.Strategy,
			Triggers:     s.Triggers,
			Heuristics:   s.Heuristics,
		}
		if strat.ID == "" {
			return nil, fmt.Errorf("model-proxy: strategy without id in config")
		}
		// Apply defaults
		if strat.Triggers == (config.ModelProxyStrategyTriggers{}) {
			strat.Triggers = config.DefaultStrategyTriggers()
		}
		if strat.Heuristics == (config.ModelProxyStrategyHeuristics{}) {
			strat.Heuristics = config.DefaultStrategyHeuristics()
		}
		// Compute effective limit
		strat.EffectiveLimit = strat.ComputeEffectiveLimit()
		out.Strategies = append(out.Strategies, strat)
	}

	return out, nil
}

// typeOpenCodeZen identifies the OpenCode Zen provider class (and is also
// accepted as a fallback when the provider id starts with "zen-", matching
// the pre-type name-based routing used before the Type field existed).
const typeOpenCodeZen = "opencode-zen"

// typeMetaModel identifies the virtual "meta-model" provider class. Unlike a
// real upstream backend, a meta-model provider has no base_url: its catalog is
// synthesized from the configured strategies (one virtual model endpoint per
// strategy, named by the strategy ID). Chat requests for such a virtual model
// are routed through the strategy's picking logic onto the real upstream
// models in its models: list. Direct requests for real model IDs are never
// intercepted by a strategy — they always go straight to their upstream.
const typeMetaModel = "meta-model"

// isVirtual reports whether this provider is a virtual meta-model provider
// (no upstream; catalog synthesized from strategies).
func (p *Provider) isVirtual() bool { return p.Type == typeMetaModel }

// typeOpenCodeOllama identifies the Ollama provider class. Ollama serves a
// bare /v1/models catalog without capability metadata, so opencode would
// treat its models as tool-less by default — the type makes the generated
// config force-enable the capabilities that virtually all Ollama models
// support (see forcedModelCaps), especially tool calling.
const typeOpenCodeOllama = "ollama"
const typeOpenCodeGroq = "groq"

// defaultOllamaCapabilities is the capability set forced onto every model of
// an "ollama"-typed provider unless the provider overrides `capabilities:`.
// toolcall + temperature are universal across Ollama's llama-/qwen-/... based
// models; vision (attachment) and reasoning stay per-model opt-in via an
// explicit `capabilities:` list, since not every Ollama model supports them.
var defaultOllamaCapabilities = []string{"toolcall", "temperature"}
var defaultGroqCapabilities = []string{"toolcall", "temperature"}

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
	if p.Type == typeOpenCodeGroq {
		return defaultGroqCapabilities
	}
	return nil
}

// defaultOpenCodeZenUserAgent is the request User-Agent OpenCode Zen expects
// of its official client. The free-tier gate matches it against the official
// pattern "opencode/<version> ... ai-sdk/provider-utils/<version>" - a bare
// "opencode/<version>" (no ai-sdk token) is rejected, so keep the ai-sdk
// token. It satisfies the gate while allowing a provider-level user_agent
// override.
const defaultOpenCodeZenUserAgent = "opencode/1.18.30 ai-sdk/provider-utils/4.0.23 runtime/bun/1.3.14"

// applyUpstreamHeaders sets any provider-class-specific headers on an
// outgoing upstream request. from is the client's original request (may be
// nil, e.g. the health check has no client). Known types:
//
//	opencode-zen — OpenCode Zen. Its free tier only accepts requests that are
//	indistinguishable from the official opencode client: it validates the
//	User-Agent and requires the x-opencode-* identity headers the client
//	sends. Verified against the live gateway:
//	- UA must start with "opencode/" AND contain "ai-sdk/provider-utils/";
//	  a bare "opencode/<version>" (no ai-sdk token) -> HTTP 403
//	  FreeTierError, as does a non-opencode UA (e.g. "curl/8.0") or no UA.
//	- BOTH x-opencode-session and x-opencode-session-id are required and must
//	  carry the SAME value; either one alone -> HTTP 403. Extra identity
//	  headers (client/project/request) are optional.
//	The @ai-sdk/openai-compatible client ships its real, per-conversation
//	session id in x-opencode-session / x-opencode-session-id (and as
//	X-Session-Id) — forward the same value verbatim under BOTH spellings so
//	Zen's per-session prefix cache hits across turns. A session the client
//	did not provide is NOT synthesized here: Zen's free tier rejects
//	fabricated session ids. The request id is likewise NOT synthesized: Zen
//	validates the x-opencode-request format when present, and a fabricated
//	"msg_"+hex value is rejected with HTTP 403 FreeTierError. The official
//	client omits the header entirely, so forwarding the client's value
//	verbatim (and sending nothing when absent) is both correct and faithful.
//	Measured against the live gateway 2026-10-15.
//
// Add new cases here as further per-provider requirements arise.
func (p *Provider) applyUpstreamHeaders(up *http.Request, from *http.Request) {
	zen := p.Type == typeOpenCodeZen || strings.HasPrefix(p.ID, "zen-")
	if !zen {
		return
	}
	// User-Agent: forward the client's opencode UA verbatim when it is the
	// full official-client UA (opencode/<version> plus the ai-sdk token the
	// free-tier gate also matches on), otherwise use the override/default.
	ua := headerFrom(from, "User-Agent")
	switch {
	case ua != "" && strings.HasPrefix(ua, "opencode/") && strings.Contains(ua, "ai-sdk/"):
		up.Header.Set("User-Agent", ua)
	case p.UserAgent != "":
		up.Header.Set("User-Agent", p.UserAgent)
	default:
		up.Header.Set("User-Agent", defaultOpenCodeZenUserAgent)
	}
	// Session: the gate requires x-opencode-session AND x-opencode-session-id
	// to be present and equal. Take whichever spelling the client supplied
	// (they carry the same value) and set both. Never fabricate one.
	sess := headerFrom(from, "x-opencode-session-id")
	if sess == "" {
		sess = headerFrom(from, "x-opencode-session")
	}
	if sess == "" {
		sess = headerFrom(from, "X-Session-Id")
	}
	if sess != "" {
		up.Header.Set("x-opencode-session", sess)
		up.Header.Set("x-opencode-session-id", sess)
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
	// Request id: forward the client's value verbatim when present, otherwise
	// send nothing. Never synthesize one (see the format note above).
	if v := headerFrom(from, "x-opencode-request"); v != "" {
		up.Header.Set("x-opencode-request", v)
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

// zenToolsRequired is the list of real opencode tool names that satisfy Zen's body gate.
// Zen's free tier requires ≥4 real opencode tools in the request body.
var zenToolsRequired = []map[string]any{
	{"type": "function", "function": map[string]any{"name": "bash", "description": "Run bash commands", "parameters": map[string]any{"type": "object", "properties": map[string]any{"command": map[string]any{"type": "string"}, "description": map[string]any{"type": "string"}}, "required": []string{"command"}}}},
	{"type": "function", "function": map[string]any{"name": "read", "description": "Read file", "parameters": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}}, "required": []string{"path"}}}},
	{"type": "function", "function": map[string]any{"name": "write", "description": "Write file", "parameters": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "content": map[string]any{"type": "string"}}, "required": []string{"path", "content"}}}},
	{"type": "function", "function": map[string]any{"name": "edit", "description": "Edit file", "parameters": map[string]any{"type": "object", "properties": map[string]any{"path": map[string]any{"type": "string"}, "old_string": map[string]any{"type": "string"}, "new_string": map[string]any{"type": "string"}}, "required": []string{"path", "old_string", "new_string"}}}},
	{"type": "function", "function": map[string]any{"name": "glob", "description": "Glob files", "parameters": map[string]any{"type": "object", "properties": map[string]any{"pattern": map[string]any{"type": "string"}}, "required": []string{"pattern"}}}},
}

// ensureZenTools injects the required tools array for Zen's body gate if missing.
// Zen's free tier requires ≥4 real opencode tools in the request body.
func ensureZenTools(payload []byte) ([]byte, error) {
	var m map[string]any
	if err := json.Unmarshal(payload, &m); err != nil {
		return payload, err
	}
	// Only inject if tools array is missing or has fewer than 4 tools
	if tools, ok := m["tools"]; ok {
		if arr, ok := tools.([]any); ok && len(arr) >= 4 {
			return payload, nil // Already has enough tools
		}
	}
	m["tools"] = zenToolsRequired
	m["tool_choice"] = "auto"
	return json.Marshal(m)
}
