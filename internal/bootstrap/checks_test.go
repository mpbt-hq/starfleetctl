// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package bootstrap

import (
	"encoding/json"
	"io/fs"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	starfleetctl "github.com/metux/starfleetctl"
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


// TestFixOpencodePluginsInstallsModuleAssets verifies that imported .mjs
// helpers are installed and checked, without being registered as plugins.
func TestFixOpencodePluginsInstallsModuleAssets(t *testing.T) {
	root := t.TempDir()
	b := New(root)

	if err := fixOpencodePlugins(b); err != nil {
		t.Fatalf("fixOpencodePlugins: %v", err)
	}

	helperNames := []string{"session-resolution.mjs", "runtime-identity.mjs", "prompt-preparation.mjs"}
	for _, helperName := range helperNames {
		embeddedPath := filepath.Join(starfleetctl.FragmentsRoot, opencodePluginsSubdir, helperName)
		want, err := fs.ReadFile(starfleetctl.Fragments, embeddedPath)
		if err != nil {
			t.Fatalf("read embedded helper %s: %v", helperName, err)
		}
		installedPath := filepath.Join(root, ".opencode", "plugins", helperName)
		got, err := os.ReadFile(installedPath)
		if err != nil {
			t.Fatalf("read installed helper %s: %v", helperName, err)
		}
		if string(got) != string(want) {
			t.Fatalf("installed helper %s differs from embedded source", helperName)
		}
	}

	if ok, detail := verifyOpencodePlugins(b); !ok {
		t.Fatalf("verify after install = false (%s)", detail)
	}

	// Imported helper modules are runtime assets, not plugin entry points.
	configPath := filepath.Join(root, ".opencode", "opencode.json")
	data, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read opencode config: %v", err)
	}
	var config struct {
		Plugin []string `json:"plugin"`
	}
	if err := json.Unmarshal(data, &config); err != nil {
		t.Fatalf("parse opencode config: %v", err)
	}
	for _, spec := range config.Plugin {
		for _, helperName := range helperNames {
			if strings.Contains(spec, helperName) {
				t.Fatalf("helper module must not be registered as a plugin: %q", spec)
			}
		}
	}

	missingHelper := filepath.Join(root, ".opencode", "plugins", helperNames[1])
	if err := os.Remove(missingHelper); err != nil {
		t.Fatalf("remove installed helper: %v", err)
	}
	if ok, detail := verifyOpencodePlugins(b); ok || !strings.Contains(detail, helperNames[1]) {
		t.Fatalf("verify with missing helper = (%v, %q), want missing %s", ok, detail, helperNames[1])
	}
}


// TestRootGitignoreManagedEntriesAreNarrowAndMerged verifies that bootstrap
// ignores only embedded Starfleetctl hooks and preserves user-owned hook rules.
func TestRootGitignoreManagedEntriesAreNarrowAndMerged(t *testing.T) {
	root := t.TempDir()
	b := New(root)
	path := filepath.Join(root, ".gitignore")
	initial := "build/\n/.claude/hooks/\n/.claude/hooks/my-custom-hook"
	if err := os.WriteFile(path, []byte(initial), 0o644); err != nil {
		t.Fatal(err)
	}

	if ok, _ := verifyRootGitignore(b); ok {
		t.Fatal("broad legacy rule should not satisfy root gitignore check")
	}
	if err := fixRootGitignore(b); err != nil {
		t.Fatalf("fixRootGitignore: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	content := string(data)
	if strings.Contains(content, "/.claude/hooks/\n") {
		t.Fatalf("broad hooks directory rule remains: %q", content)
	}
	if !strings.Contains(content, "/.claude/hooks/agent-permission-hook\n") {
		t.Fatalf("managed hook entry missing: %q", content)
	}
	for _, managed := range []string{
		"/.opencode/plugins/starfleet-dispatch.ts",
		"/.opencode/plugins/session-resolution.mjs",
	} {
		if !strings.Contains(content, managed+"\n") {
			t.Errorf("managed OpenCode asset entry %q missing: %q", managed, content)
		}
	}
	if strings.Contains(content, "/.opencode/plugins/\n") {
		t.Fatalf("broad OpenCode plugins directory rule was added: %q", content)
	}
	if !strings.Contains(content, "/.claude/hooks/my-custom-hook") {
		t.Fatalf("user hook rule was not preserved: %q", content)
	}
	if !strings.Contains(content, "build/\n") {
		t.Fatalf("unrelated user rule was not preserved: %q", content)
	}
	if ok, detail := verifyRootGitignore(b); !ok {
		t.Fatalf("verify after fix = false (%s)", detail)
	}

	before := string(data)
	if err := fixRootGitignore(b); err != nil {
		t.Fatalf("second fixRootGitignore: %v", err)
	}
	after, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(after) != before {
		t.Fatal("second root gitignore fix changed an already-correct file")
	}
}

func TestMergeGitignoreEntriesPreservesLocalRules(t *testing.T) {
	got := mergeGitignoreEntries(
		"custom/\n/obsolete/\n",
		[]string{"/managed/file"},
		[]string{"/obsolete/"},
	)
	want := "custom/\n/managed/file\n"
	if got != want {
		t.Fatalf("mergeGitignoreEntries() = %q, want %q", got, want)
	}
}
