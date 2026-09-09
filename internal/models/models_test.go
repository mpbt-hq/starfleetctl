// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package models

import (
	"testing"
)

// TestMergeDeduplicatesActiveIds verifies that overlapping scan sources
// (opencode catalog + model-proxy both list e.g. ollama models) do not
// produce duplicate active entries in the merged YAML.
func TestMergeDeduplicatesActiveIds(t *testing.T) {
	m := &Models{}
	existing := map[string]yamlModel{
		"ollama/llama3.2:latest": {ID: "ollama/llama3.2:latest", Note: "hand-edited"},
	}
	scanMap := map[string]scannedModel{
		"ollama/llama3.2:latest": {id: "ollama/llama3.2:latest", provider: "ollama", label: "Llama3.2:latest"},
	}
	scanOrder := []scannedModel{
		{id: "ollama/llama3.2:latest", provider: "ollama", label: "Llama3.2:latest", context: 131072}, // opencode catalog
		{id: "ollama/llama3.2:latest", provider: "ollama", label: "Llama3.2:latest", context: 131072}, //  from model-proxy
		{id: "ollama/llama-64k:latest", provider: "ollama", label: "Llama 64k:latest", context: 131072},
	}

	out := m.merge(existing, scanMap, scanOrder)

	active := map[string]bool{}
	for _, e := range out.Models {
		if e.Disabled {
			continue
		}
		if active[e.ID] {
			t.Fatalf("duplicate active id %q in merged output", e.ID)
		}
		active[e.ID] = true
	}
	if !active["ollama/llama3.2:latest"] || !active["ollama/llama-64k:latest"] {
		t.Fatalf("expected both ollama models active, got %v", active)
	}
	// Hand-edited fields of the surviving first occurrence are preserved.
	for _, e := range out.Models {
		if e.ID == "ollama/llama3.2:latest" {
			if e.Note != "hand-edited" {
				t.Fatalf("hand-edited note lost: %q", e.Note)
			}
		}
	}
}

// TestMergeMarksAbsentModelsDisabled verifies that entries gone from the
// catalog still land in the file, marked disabled, not deleted.
func TestMergeMarksAbsentModelsDisabled(t *testing.T) {
	m := &Models{}
	existing := map[string]yamlModel{
		"ollama/old-model": {ID: "ollama/old-model", Note: "gone", Disabled: true},
	}
	scanMap := map[string]scannedModel{}
	out := m.merge(existing, scanMap, nil)

	found := false
	for _, e := range out.Models {
		if e.ID != "ollama/old-model" {
			continue
		}
		found = true
		if !e.Disabled {
			t.Fatalf("absent model not marked disabled")
		}
		if e.Note != "gone" {
			t.Fatalf("disabled model note lost: %q", e.Note)
		}
	}
	if !found {
		t.Fatalf("absent model missing from merged output")
	}
}