// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package bootstrap

import (
	"encoding/json"
	"reflect"
	"testing"
)

func TestEnsureOpencodePlanAccessFromScratch(t *testing.T) {
	doc := map[string]any{}
	if !ensureOpencodePlanAccess(doc) {
		t.Fatal("expected change on empty doc")
	}

	if got := doc["$schema"]; got != opencodeConfigSchema {
		t.Errorf("$schema = %v, want %s", got, opencodeConfigSchema)
	}

	instr, ok := doc["instructions"].([]any)
	if !ok || len(instr) != 1 || instr[0] != opencodeInstructionsPath {
		t.Fatalf("instructions = %#v, want [%q]", instr, opencodeInstructionsPath)
	}

	bash, ok := nestedBashPerms(doc)
	if !ok {
		t.Fatal("missing agent.plan.permission.bash in generated config")
	}
	for pattern, want := range opencodePlanPermissionRules() {
		if got, ok := bash[pattern]; !ok || got != want {
			t.Errorf("permission %q = %v (present=%v), want %q", pattern, got, ok, want)
		}
	}
	if len(bash) != 2*len(opencodePlanCommands) {
		t.Errorf("permission.bash has %d entries, want %d", len(bash), 2*len(opencodePlanCommands))
	}
}

func TestEnsureOpencodePlanAccessMergesExisting(t *testing.T) {
	// Simulate a hand-maintained config that already carries user content.
	doc := map[string]any{
		"$schema": "https://example.com/custom-schema.json",
		"plugin":  []any{"some-other-npm-plugin"},
		"agent": map[string]any{
			"plan": map[string]any{
				"permission": map[string]any{
					"bash": map[string]any{
						"starfleetctl dashboard*": "ask",
					},
				},
			},
		},
	}
	if !ensureOpencodePlanAccess(doc) {
		t.Fatal("expected change (instructions + permissions missing)")
	}

	// User keys are preserved untouched.
	if got := doc["$schema"]; got != "https://example.com/custom-schema.json" {
		t.Errorf("custom $schema was overwritten: %v", got)
	}
	if plugin := doc["plugin"]; !reflect.DeepEqual(plugin, []any{"some-other-npm-plugin"}) {
		t.Errorf("plugin array changed: %#v", plugin)
	}
	bash, ok := nestedBashPerms(doc)
	if !ok {
		t.Fatal("missing agent.plan.permission.bash after merge")
	}
	// An explicit user rule is honored, not overwritten by bootstrap.
	if got := bash["starfleetctl dashboard*"]; got != "ask" {
		t.Errorf("user rule starfleetctl dashboard* overwritten: %v", got)
	}
	// Every other verb pattern is added as an allow rule.
	for pattern, want := range opencodePlanPermissionRules() {
		if pattern == "starfleetctl dashboard*" || pattern == ".starfleet-ai/bin/starfleetctl dashboard*" {
			continue
		}
		if got, ok := bash[pattern]; !ok || got != want {
			t.Errorf("permission %q = %v (present=%v), want %q", pattern, got, ok, want)
		}
	}
}

func TestEnsureOpencodePlanAccessDropsLegacyInstructions(t *testing.T) {
	doc := map[string]any{
		"instructions": []any{".starfleet-ai/agents.d/index.md", "extra.md"},
	}
	if !ensureOpencodePlanAccess(doc) {
		t.Fatal("expected change (legacy path drop)")
	}
	instr, _ := doc["instructions"].([]any)
	seen := map[string]bool{}
	for _, e := range instr {
		seen[e.(string)] = true
	}
	if !seen[opencodeInstructionsPath] {
		t.Errorf("instructions missing %q: %#v", opencodeInstructionsPath, instr)
	}
	if seen[".starfleet-ai/agents.d/index.md"] {
		t.Errorf("legacy instructions path not dropped: %#v", instr)
	}
	if !seen["extra.md"] {
		t.Errorf("user instruction dropped: %#v", instr)
	}
}

func TestEnsureOpencodePlanAccessIdempotent(t *testing.T) {
	doc := map[string]any{}
	if !ensureOpencodePlanAccess(doc) {
		t.Fatal("first pass should change")
	}
	if ensureOpencodePlanAccess(doc) {
		t.Fatal("second pass on already-complete config must be a no-op")
	}
	// And the config must round-trip through JSON without extra changes.
	data, err := json.Marshal(doc)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var again map[string]any
	if err := json.Unmarshal(data, &again); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if ensureOpencodePlanAccess(again) {
		t.Fatal("JSON round-trip should not introduce a further change")
	}
}

// nestedBashPerms unwraps doc.agent.plan.permission.bash.
func nestedBashPerms(doc map[string]any) (map[string]any, bool) {
	agent, _ := doc["agent"].(map[string]any)
	if agent == nil {
		return nil, false
	}
	plan, _ := agent["plan"].(map[string]any)
	if plan == nil {
		return nil, false
	}
	permission, _ := plan["permission"].(map[string]any)
	if permission == nil {
		return nil, false
	}
	bash, ok := permission["bash"].(map[string]any)
	return bash, ok
}
