// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package modelproxy

import (
	"regexp"
	"strings"
)

// deriveLabel converts a model id into a human-readable display label. It is
// the last-resort enrichment for models the embedded opencode catalog
// (models.dev) does not know: the catalog covers the mainstream models, but
// brand-new zen-proxy models and the long tail of NIM models are missing
// there, so without a fallback their entries would stay bare ids in the
// generated opencode configs and the web console.
//
// The derivation is purely lexical: take the last path segment, split it on
// '-' and '_', then title-case each word, keeping size/capability suffixes
// (b/k/m) and a set of known brand tokens uppercase. It is best-effort and
// only ever used as display metadata — never for routing or matching.
func deriveLabel(id string) string {
	base := id
	if i := strings.LastIndexByte(base, '/'); i >= 0 {
		base = base[i+1:]
	}
	words := strings.FieldsFunc(base, func(r rune) bool { return r == '-' || r == '_' })
	if len(words) == 0 {
		return id
	}
	out := make([]string, 0, len(words))
	for _, w := range words {
		out = append(out, labelWord(w))
	}
	return strings.Join(out, " ")
}

var (
	// sizeRe matches size/capability suffixes like 340b, 8x22b, 6.7b, 32k.
	sizeRe = regexp.MustCompile(`^[0-9][0-9x.]*[bkm]$`)
	// letterNumRe matches a single-letter prefix + size suffix like a4b, a800m.
	letterNumRe = regexp.MustCompile(`^[a-z][0-9]+[bkm]$`)
	// versionRe matches dotted version tokens like v0.1, v1.5 (kept lowercase,
	// matching the upstream naming convention for e.g. Codestral v0.1).
	versionRe = regexp.MustCompile(`^v[0-9]+\.[0-9]`)
	// alphaNumRe matches a mixed alpha+number token like starcoder2 or llama2.
	alphaNumRe = regexp.MustCompile(`^([A-Za-z]+)([0-9][0-9.]*)$`)
)

// brandWords maps known brand/capability tokens to their canonical spelling
// where a naive title-case would be wrong (Deepseek → DeepSeek, dbrx → DBRX).
var brandWords = map[string]string{
	"ai":             "AI",
	"gpt":            "GPT",
	"glm":            "GLM",
	"nv":             "NV",
	"qa":             "QA",
	"moe":            "MoE",
	"vlm":            "VLM",
	"dbrx":           "DBRX",
	"deepseek":       "DeepSeek",
	"starcoder":      "StarCoder",
	"codellama":      "Code Llama",
	"embedqa":        "EmbedQA",
	"minimax":        "MiniMax",
	"codegemma":      "CodeGemma",
	"nemoguard":      "NeMo Guard",
	"nemoretriever":  "NeMo Retriever",
	"diffusiongemma": "DiffusionGemma",
	"recurrentgemma": "RecurrentGemma",
	"nvclip":         "NVClip",
}

func labelWord(w string) string {
	// Size/capability suffix: "340b" → "340B", "8x22b" → "8x22B".
	if sizeRe.MatchString(w) {
		return w[:len(w)-1] + strings.ToUpper(w[len(w)-1:])
	}
	// Single-letter prefix + size: "a4b" → "A4B".
	if letterNumRe.MatchString(w) {
		return strings.ToUpper(w[:1]) + w[1:len(w)-1] + strings.ToUpper(w[len(w)-1:])
	}
	// Dotted version tokens stay lowercase: "v0.1".
	if versionRe.MatchString(w) {
		return w
	}
	// Whole-word brand first ("nvclip"), then mixed alpha+number tokens with a
	// brand prefix ("starcoder2" → "StarCoder2").
	if brand, ok := brandWords[w]; ok {
		return brand
	}
	if m := alphaNumRe.FindStringSubmatch(w); m != nil {
		if brand, ok := brandWords[m[1]]; ok {
			return brand + m[2]
		}
		return strings.ToUpper(m[1][:1]) + m[1][1:] + m[2]
	}
	return strings.ToUpper(w[:1]) + w[1:]
}
