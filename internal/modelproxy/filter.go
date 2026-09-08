// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package modelproxy

import "strings"

// knownFreeModels holds built-in allowlists of models usable on free-tier /
// no-cost plans for providers that carry both free and paid models on the
// same endpoint but do NOT expose the distinction via their /models listing.
// Used when a provider sets `model_filter: free-only`.
//
// NVIDIA NIM: the upstream /models endpoint only returns id/object/created/
// owned_by — no pricing info. This list is maintained by hand; extend it in
// model-proxy.yaml via an explicit comma-separated allowlist when a model is
// missing.
var knownFreeModels = map[string][]string{
	"nim-proxy": {
		"nvidia/nemotron-3-ultra-550b-a55b",
		"nvidia/nemotron-nano-3-30b-a3b",
		"nvidia/nemotron-3-nano-30b-a3b",
		"nvidia/nemotron-3-super-120b-a12b",
		"nvidia/llama-3.1-nemotron-51b-instruct",
		"nvidia/llama-3.1-nemotron-70b-instruct",
	},
	"nvidia-direct": {
		"nvidia/nemotron-3-ultra-550b-a55b",
		"nvidia/nemotron-nano-3-30b-a3b",
		"nvidia/nemotron-3-nano-30b-a3b",
		"nvidia/nemotron-3-super-120b-a12b",
		"nvidia/llama-3.1-nemotron-51b-instruct",
		"nvidia/llama-3.1-nemotron-70b-instruct",
	},
}

// isFreeModel reports whether a model is considered free-tier for the given
// provider under `model_filter: free-only`.
//
// Per-provider heuristics:
//   - zen-proxy (OpenCode Zen): free models carry a "-free" suffix
//     (e.g. "deepseek-v4-flash-free", "nemotron-3-ultra-free",
//     "muse-spark-1.3-contributor-free").
//   - nim-proxy / nvidia-direct: NIM's /models listing exposes no pricing
//     information, so a built-in allowlist (knownFreeModels) is used.
func isFreeModel(prov Provider, id string) bool {
	switch prov.ID {
	case "zen-proxy":
		l := strings.ToLower(id)
		return strings.HasSuffix(l, "-free") || strings.HasSuffix(l, "free")
	default:
		for _, m := range knownFreeModels[prov.ID] {
			if m == id {
				return true
			}
		}
		return false
	}
}

// explicitAllowlist parses `model_filter` when it is a comma-separated allow-
// list of explicit model IDs. Returns nil for the special values ("" / "all" /
// "free-only"). Entries are trimmed; empty entries dropped.
func explicitAllowlist(filter string) map[string]bool {
	if filter == "" || filter == "all" || filter == "free-only" {
		return nil
	}
	out := map[string]bool{}
	for _, id := range strings.Split(filter, ",") {
		id = strings.TrimSpace(id)
		if id != "" {
			out[id] = true
		}
	}
	return out
}

// applyModelFilter filters the fetched model listing according to the
// provider's ModelFilter setting. "" / "all" pass everything through;
// "free-only" keeps only free-tier models (see isFreeModel); anything else is
// treated as a comma-separated explicit allowlist of model IDs.
func applyModelFilter(prov Provider, infos []ModelInfo) []ModelInfo {
	if prov.ModelFilter == "" || prov.ModelFilter == "all" {
		return infos
	}
	if allow := explicitAllowlist(prov.ModelFilter); allow != nil {
		var out []ModelInfo
		for _, m := range infos {
			if allow[m.ID] {
				out = append(out, m)
			}
		}
		return out
	}
	// free-only
	var out []ModelInfo
	for _, m := range infos {
		if isFreeModel(prov, m.ID) {
			out = append(out, m)
		}
	}
	return out
}
