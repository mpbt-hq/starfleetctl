// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package modelproxy

import (
	"testing"
	"time"
)

func TestDeriveLabel(t *testing.T) {
	cases := []struct {
		id   string
		want string
	}{
		// Zen models (bare ids, no vendor prefix).
		{"claude-fable-5", "Claude Fable 5"},
		{"claude-opus-4-6", "Claude Opus 4 6"},
		{"gpt-5.6-sol", "GPT 5.6 Sol"},
		{"gpt-5.1-codex-max", "GPT 5.1 Codex Max"},
		{"gemini-3.6-flash", "Gemini 3.6 Flash"},
		{"gemini-3.5-flash-lite", "Gemini 3.5 Flash Lite"},
		{"deepseek-v4-pro", "DeepSeek V4 Pro"},
		{"glm-5.2", "GLM 5.2"},
		{"minimax-m3", "MiniMax M3"},
		{"kimi-k2.6", "Kimi K2.6"},
		{"qwen3.6-plus", "Qwen3.6 Plus"},
		{"grok-build-0.1", "Grok Build 0.1"},
		// NIM models (vendor/... prefix; vendor dropped).
		{"01-ai/yi-large", "Yi Large"},
		{"ai21labs/jamba-1.5-large-instruct", "Jamba 1.5 Large Instruct"},
		{"bigcode/starcoder2-15b", "StarCoder2 15B"},
		{"databricks/dbrx-instruct", "DBRX Instruct"},
		{"deepseek-ai/deepseek-coder-6.7b-instruct", "DeepSeek Coder 6.7B Instruct"},
		{"google/codegemma-7b", "CodeGemma 7B"},
		{"google/diffusiongemma-26b-a4b-it", "DiffusionGemma 26B A4B It"},
		{"google/gemma-2b", "Gemma 2B"},
		{"ibm/granite-34b-code-instruct", "Granite 34B Code Instruct"},
		{"meta/codellama-70b", "Code Llama 70B"},
		{"meta/llama2-70b", "Llama2 70B"},
		{"microsoft/phi-3.5-moe-instruct", "Phi 3.5 MoE Instruct"},
		{"mistralai/codestral-22b-instruct-v0.1", "Codestral 22B Instruct v0.1"},
		{"mistralai/mixtral-8x22b-v0.1", "Mixtral 8x22B v0.1"},
		{"moonshotai/kimi-k2.6", "Kimi K2.6"},
		{"nv-mistralai/mistral-nemo-12b-instruct", "Mistral Nemo 12B Instruct"},
		{"nvidia/llama-3.1-nemotron-51b-instruct", "Llama 3.1 Nemotron 51B Instruct"},
		{"nvidia/nemotron-4-340b-instruct", "Nemotron 4 340B Instruct"},
		{"nvidia/nv-embedqa-e5-v5", "NV EmbedQA E5 V5"},
		{"nvidia/nvclip", "NVClip"},
		{"nvidia/riva-translate-4b-instruct-v2", "Riva Translate 4B Instruct V2"},
		{"snowflake/arctic-embed-l", "Arctic Embed L"},
		{"writer/palmyra-med-70b-32k", "Palmyra Med 70B 32K"},
	}
	for _, c := range cases {
		if got := deriveLabel(c.id); got != c.want {
			t.Errorf("deriveLabel(%q) = %q, want %q", c.id, got, c.want)
		}
	}
}

// catalogTestInput mirrors the shape of `opencode models --verbose`: a
// "provider/id" header line followed by a JSON block. Only the fields the
// catalog parser reads matter.
const catalogTestInput = `nvidia/deepseek-ai/deepseek-v4-flash
{
  "id": "deepseek-ai/deepseek-v4-flash",
  "name": "DeepSeek V4 Flash",
  "limit": {"context": 1048576, "output": 65536},
  "capabilities": {"reasoning": true, "input": {"text": true}, "output": {"text": true}}
}
nvidia/meta/llama-3.1-8b-instruct
{
  "id": "meta/llama-3.1-8b-instruct",
  "name": "Llama 3.1 8B Instruct",
  "limit": {"context": 131072, "output": 32768},
  "capabilities": {"input": {"text": true}, "output": {"text": true}}
}
nvidia/nvidia/nemotron-4-340b-instruct
{
  "id": "nvidia/nemotron-4-340b-instruct",
  "name": "Nemotron 4 340B Instruct",
  "limit": {"context": 131072, "output": 32768},
  "capabilities": {"input": {"text": true}, "output": {"text": true}}
}
`

// TestLookupFallbacks verifies the catalog lookup chain: exact match, then
// basename match (bare proxy id vs vendor-prefixed catalog entry), then the
// lexical label fallback for catalog-unknown models.
func TestLookupFallbacks(t *testing.T) {
	cat := &catalogStore{}
	cat.byID = parseOpencodeCatalog(catalogTestInput)
	cat.at = time.Now()

	cases := []struct {
		id      string
		wantLbl string
		wantCtx int
	}{
		// Exact catalog match keeps label + context.
		{"meta/llama-3.1-8b-instruct", "Llama 3.1 8B Instruct", 131072},
		{"deepseek-ai/deepseek-v4-flash", "DeepSeek V4 Flash", 1048576},
		// Basename match: bare zen-proxy id resolves to the vendor-prefixed entry.
		{"deepseek-v4-flash", "DeepSeek V4 Flash", 1048576},
		// Lexical fallback for catalog-unknown models.
		{"claude-fable-5", "Claude Fable 5", 0},
		{"nvidia/llama-3.1-nemotron-51b-instruct", "Llama 3.1 Nemotron 51B Instruct", 0},
		{"gpt-5.6-sol", "GPT 5.6 Sol", 0},
	}
	for _, c := range cases {
		m := cat.lookup(c.id)
		if m.Label != c.wantLbl {
			t.Errorf("lookup(%q).Label = %q, want %q", c.id, m.Label, c.wantLbl)
		}
		if m.Context != c.wantCtx {
			t.Errorf("lookup(%q).Context = %d, want %d", c.id, m.Context, c.wantCtx)
		}
	}
}
