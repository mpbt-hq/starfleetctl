// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package modelproxy

import (
	"encoding/json"
	"os/exec"
	"regexp"
	"strings"
	"sync"
	"time"
)

// catalogModel carries the display metadata opencode knows about a model
// (label, context window, capability flags). The upstream /models endpoints
// only expose id/object/created/owned_by, so the proxy enriches its model
// listing with this catalog data when available.
type catalogModel struct {
	Label   string
	Context int
	Output  int
	Caps    []string
}

// catalogStore holds a parsed opencode model catalog with a short TTL.
type catalogStore struct {
	mu   sync.Mutex
	at   time.Time
	byID map[string]catalogModel
}

var catalog = &catalogStore{}

// load returns the parsed opencode catalog, re-running `opencode models
// --verbose` at most every 5 minutes. Empty on any error (the proxy then
// serves the listing without enrichment).
func (c *catalogStore) load() map[string]catalogModel {
	c.mu.Lock()
	defer c.mu.Unlock()
	if c.byID != nil && time.Since(c.at) < 5*time.Minute {
		return c.byID
	}
	out, err := exec.Command("opencode", "models", "--verbose").Output()
	if err != nil {
		return nil
	}
	byID := parseOpencodeCatalog(string(out))
	c.byID = byID
	c.at = time.Now()
	return byID
}

// lookup returns the catalog metadata for a model id. It tries, in order:
//
//  1. an exact catalog match (same id),
//  2. a basename match — the catalog keys vendor-prefixed ids
//     (e.g. "deepseek-ai/deepseek-v4-flash") while zen-proxy serves the bare
//     basename ("deepseek-v4-flash"); if exactly one catalog entry shares the
//     basename, its metadata is used,
//  3. a derived display label from the id itself, for models the catalog does
//     not know at all.
//
// The fallbacks exist because the embedded models.dev catalog covers only the
// mainstream models; brand-new zen models and the NIM long tail are missing
// there and would otherwise surface as bare ids.
func (c *catalogStore) lookup(id string) catalogModel {
	cat := c.load()
	if m, ok := cat[id]; ok {
		return m
	}
	// Basename match: vendor-prefixed catalog entry for a bare proxy id.
	base := id
	if strings.ContainsRune(base, '/') {
		base = base[strings.LastIndexByte(base, '/')+1:]
	}
	if base != "" {
		var cand []string
		for key := range cat {
			k := key
			if strings.ContainsRune(k, '/') {
				k = k[strings.LastIndexByte(k, '/')+1:]
			}
			if k == base {
				cand = append(cand, key)
			}
		}
		if len(cand) == 1 {
			return cat[cand[0]]
		}
	}
	// Fallback: lexical label derivation for catalog-unknown models.
	return catalogModel{Label: deriveLabel(id)}
}

// parseOpencodeCatalog parses the `opencode models --verbose` listing. The
// output is a sequence of "<providerID>/<id>" lines each followed by a JSON
// block describing that model; the JSON carries the authoritative id, name,
// limit.context and capabilities. We key the map by the JSON id so upstream
// model ids match directly.
func parseOpencodeCatalog(output string) map[string]catalogModel {
	lines := strings.Split(output, "\n")
	type kv struct {
		idx int
		key string
	}
	var heads []kv
	for i, line := range lines {
		s := strings.TrimSpace(line)
		if s == "" || strings.HasPrefix(s, "{") || strings.HasPrefix(s, "}") || strings.HasPrefix(s, "\"") {
			continue
		}
		heads = append(heads, kv{i, s})
	}

	byID := make(map[string]catalogModel)
	for h, hd := range heads {
		next := len(lines)
		if h+1 < len(heads) {
			next = heads[h+1].idx
		}
		var jsonLines []string
		for j := hd.idx + 1; j < next; j++ {
			if strings.HasPrefix(strings.TrimSpace(lines[j]), "{") {
				jsonLines = lines[j:next]
				break
			}
		}
		if len(jsonLines) == 0 {
			continue
		}
		raw := strings.Join(jsonLines, "\n")
		var d struct {
			ID           string `json:"id"`
			Name         string `json:"name"`
			Capabilities struct {
				Reasoning  bool `json:"reasoning"`
				Attachment bool `json:"attachment"`
				Input      struct {
					Image bool `json:"image"`
					Audio bool `json:"audio"`
					Video bool `json:"video"`
					PDF   bool `json:"pdf"`
				} `json:"input"`
				Output struct {
					Image bool `json:"image"`
					Audio bool `json:"audio"`
					Video bool `json:"video"`
					PDF   bool `json:"pdf"`
				} `json:"output"`
			} `json:"capabilities"`
			Limit struct {
				Context int `json:"context"`
				Output  int `json:"output"`
			} `json:"limit"`
		}
		if err := json.Unmarshal([]byte(raw), &d); err != nil {
			fixed := regexp.MustCompile(`,\s*}`).ReplaceAllString(raw, "}")
			fixed = regexp.MustCompile(`,\s*]`).ReplaceAllString(fixed, "]")
			if err := json.Unmarshal([]byte(fixed), &d); err != nil {
				continue
			}
		}
		if d.ID == "" {
			continue
		}
		var caps []string
		if d.Capabilities.Reasoning {
			caps = append(caps, "reasoning")
		}
		if d.Capabilities.Attachment {
			caps = append(caps, "attachment")
		}
		if d.Capabilities.Input.Image {
			caps = append(caps, "image-in")
		}
		if d.Capabilities.Output.Image {
			caps = append(caps, "image-out")
		}
		if d.Capabilities.Input.Audio {
			caps = append(caps, "audio-in")
		}
		if d.Capabilities.Output.Audio {
			caps = append(caps, "audio-out")
		}
		if d.Capabilities.Input.Video {
			caps = append(caps, "video-in")
		}
		if d.Capabilities.Output.Video {
			caps = append(caps, "video-out")
		}
		if d.Capabilities.Input.PDF {
			caps = append(caps, "pdf-in")
		}
		if d.Capabilities.Output.PDF {
			caps = append(caps, "pdf-out")
		}
		byID[d.ID] = catalogModel{Label: d.Name, Context: d.Limit.Context, Output: d.Limit.Output, Caps: caps}
	}
	return byID
}
