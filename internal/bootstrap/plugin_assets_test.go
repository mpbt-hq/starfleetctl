// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package bootstrap

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"reflect"
	"runtime"
	"sort"
	"strings"
	"testing"

	starfleetctl "github.com/metux/starfleetctl"
)

// classifyPluginSourceAsset is deliberately exhaustive: a new file type in
// the canonical plugin source directory must be classified explicitly rather
// than silently escaping the source/embed/install parity check.
func classifyPluginSourceAsset(name string) (bool, error) {
	if isOpencodePluginAsset(name) {
		return true, nil
	}
	switch {
	case name == "tsconfig.json",
		strings.HasSuffix(name, ".d.mts"),
		strings.HasSuffix(name, ".test.ts"),
		strings.HasSuffix(name, ".test.mjs"):
		return false, nil
	default:
		return false, fmt.Errorf("unclassified file in opencode plugin source tree: %s", name)
	}
}

func discoverPluginRuntimeAssets(fsys fs.FS, root string) (map[string]bool, error) {
	assets := make(map[string]bool)
	err := fs.WalkDir(fsys, root, func(file string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(filepath.FromSlash(root), filepath.FromSlash(file))
		if err != nil {
			return err
		}
		rel = filepath.ToSlash(rel)
		runtimeAsset, err := classifyPluginSourceAsset(path.Base(rel))
		if err != nil {
			return err
		}
		if runtimeAsset {
			assets[rel] = true
		}
		return nil
	})
	return assets, err
}


func discoverAllFiles(fsys fs.FS, root string) (map[string]bool, error) {
	files := make(map[string]bool)
	err := fs.WalkDir(fsys, root, func(file string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			return nil
		}
		rel, err := filepath.Rel(filepath.FromSlash(root), filepath.FromSlash(file))
		if err != nil {
			return err
		}
		files[filepath.ToSlash(rel)] = true
		return nil
	})
	return files, err
}

func pluginAssetSetDifference(want, got map[string]bool) (missing, unexpected []string) {
	for name := range want {
		if !got[name] {
			missing = append(missing, name)
		}
	}
	for name := range got {
		if !want[name] {
			unexpected = append(unexpected, name)
		}
	}
	sort.Strings(missing)
	sort.Strings(unexpected)
	return missing, unexpected
}

func assertPluginAssetSetsEqual(t *testing.T, label string, want, got map[string]bool) {
	t.Helper()
	missing, unexpected := pluginAssetSetDifference(want, got)
	if len(missing) != 0 || len(unexpected) != 0 {
		t.Fatalf("%s asset set mismatch: missing=%v unexpected=%v", label, missing, unexpected)
	}
}

func pluginSourceRoot(t *testing.T) string {
	t.Helper()
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller could not locate plugin asset parity test")
	}
	return filepath.Clean(filepath.Join(filepath.Dir(file), "..", "..", "fragments", opencodePluginsSubdir))
}

func TestOpencodePluginAssetsMatchSourceEmbeddedAndBootstrapInstall(t *testing.T) {
	sourceRoot := pluginSourceRoot(t)
	sourceFS := os.DirFS(filepath.Dir(filepath.Dir(sourceRoot)))
	sourceDir := filepath.ToSlash(filepath.Join("fragments", opencodePluginsSubdir))
	sourceAssets, err := discoverPluginRuntimeAssets(sourceFS, sourceDir)
	if err != nil {
		t.Fatalf("discover canonical source assets: %v", err)
	}
	if len(sourceAssets) == 0 {
		t.Fatal("canonical plugin source tree contains no runtime assets")
	}

	embeddedDir := path.Join(starfleetctl.FragmentsRoot, opencodePluginsSubdir)
	embeddedAssets, err := discoverPluginRuntimeAssets(starfleetctl.Fragments, embeddedDir)
	if err != nil {
		t.Fatalf("discover embedded plugin assets: %v", err)
	}
	assertPluginAssetSetsEqual(t, "source vs embedded", sourceAssets, embeddedAssets)

	for name := range sourceAssets {
		sourceBytes, err := fs.ReadFile(sourceFS, path.Join(sourceDir, name))
		if err != nil {
			t.Fatalf("read source asset %s: %v", name, err)
		}
		embeddedBytes, err := fs.ReadFile(starfleetctl.Fragments, path.Join(embeddedDir, name))
		if err != nil {
			t.Fatalf("read embedded asset %s: %v", name, err)
		}
		if !reflect.DeepEqual(sourceBytes, embeddedBytes) {
			t.Errorf("embedded asset %s differs from canonical source", name)
		}
	}

	root := t.TempDir()
	b := New(root)
	if err := fixOpencodePlugins(b); err != nil {
		t.Fatalf("run real plugin bootstrap installer: %v", err)
	}
	installedRoot := filepath.Join(root, ".opencode", "plugins")
	installedFS := os.DirFS(installedRoot)
	installedAssets, err := discoverAllFiles(installedFS, ".")
	if err != nil {
		t.Fatalf("discover installed plugin files: %v", err)
	}
	// The temporary install starts empty, so this exact comparison also
	// proves source-side tests/config/declaration files were not copied into
	// the runtime plugin directory.
	assertPluginAssetSetsEqual(t, "runtime source vs complete installed tree", sourceAssets, installedAssets)

	for name := range sourceAssets {
		sourceBytes, err := fs.ReadFile(sourceFS, path.Join(sourceDir, name))
		if err != nil {
			t.Fatalf("read source asset %s: %v", name, err)
		}
		installedBytes, err := fs.ReadFile(installedFS, name)
		if err != nil {
			t.Fatalf("read installed asset %s: %v", name, err)
		}
		if !reflect.DeepEqual(sourceBytes, installedBytes) {
			t.Errorf("installed asset %s differs from canonical source", name)
		}
	}

	configPath := filepath.Join(root, ".opencode", "opencode.json")
	configBytes, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatalf("read generated OpenCode config: %v", err)
	}
	var config struct {
		Plugin []string `json:"plugin"`
	}
	if err := json.Unmarshal(configBytes, &config); err != nil {
		t.Fatalf("parse generated OpenCode config: %v", err)
	}
	registered := make(map[string]bool, len(config.Plugin))
	for _, spec := range config.Plugin {
		registered[spec] = true
	}
	for name := range sourceAssets {
		spec := pluginConfigSpec(path.Base(name))
		if strings.HasSuffix(name, ".ts") && !registered[spec] {
			t.Errorf("plugin entry point %s is not registered as %q", name, spec)
		}
		if strings.HasSuffix(name, ".mjs") && registered[spec] {
			t.Errorf("helper module %s must not be registered as an entry point", name)
		}
	}

	if ok, detail := verifyOpencodePlugins(b); !ok {
		t.Fatalf("verify installed plugin assets: %s", detail)
	}

	// Negative regression cases: missing installed helper and unregistered
	// entry point must both be caught by the production verifier.
	var helper string
	var entryPoint string
	for name := range sourceAssets {
		switch {
		case helper == "" && strings.HasSuffix(name, ".mjs"):
			helper = name
		case entryPoint == "" && strings.HasSuffix(name, ".ts"):
			entryPoint = name
		}
	}
	if helper == "" || entryPoint == "" {
		t.Fatalf("expected at least one .mjs helper and .ts entry point, got helper=%q entry=%q", helper, entryPoint)
	}
	if err := os.Remove(filepath.Join(installedRoot, filepath.FromSlash(helper))); err != nil {
		t.Fatalf("remove installed helper for negative test: %v", err)
	}
	if ok, detail := verifyOpencodePlugins(b); ok || !strings.Contains(detail, path.Base(helper)) {
		t.Fatalf("verifier with missing helper = (%v, %q), want missing %s", ok, detail, helper)
	}
	if err := fixOpencodePlugins(b); err != nil {
		t.Fatalf("restore installed plugin assets: %v", err)
	}

	// Remove the entry-point registration from the config and ensure the
	// production verifier detects it, even though the file itself is present.
	for i, spec := range config.Plugin {
		if spec == pluginConfigSpec(path.Base(entryPoint)) {
			config.Plugin = append(config.Plugin[:i], config.Plugin[i+1:]...)
			break
		}
	}
	configBytes, err = json.Marshal(config)
	if err != nil {
		t.Fatalf("marshal config without entry-point registration: %v", err)
	}
	if err := os.WriteFile(configPath, configBytes, 0o644); err != nil {
		t.Fatalf("write config without entry-point registration: %v", err)
	}
	if ok, detail := verifyOpencodePlugins(b); ok || !strings.Contains(detail, path.Base(entryPoint)) {
		t.Fatalf("verifier with unregistered entry point = (%v, %q), want unregistered %s", ok, detail, entryPoint)
	}
}

func TestPluginAssetParityDetectsOmittedAndUnexpectedEmbeddedAssets(t *testing.T) {
	source := map[string]bool{"entry.ts": true, "helper.mjs": true}
	embedded := map[string]bool{"entry.ts": true}
	missing, unexpected := pluginAssetSetDifference(source, embedded)
	if !reflect.DeepEqual(missing, []string{"helper.mjs"}) || len(unexpected) != 0 {
		t.Fatalf("omitted embedded asset diff = missing %v unexpected %v", missing, unexpected)
	}
	embedded["extra.mjs"] = true
	missing, unexpected = pluginAssetSetDifference(source, embedded)
	if !reflect.DeepEqual(missing, []string{"helper.mjs"}) || !reflect.DeepEqual(unexpected, []string{"extra.mjs"}) {
		t.Fatalf("unexpected embedded asset diff = missing %v unexpected %v", missing, unexpected)
	}
}

func TestPluginAssetClassifierRejectsUnclassifiedExtensions(t *testing.T) {
	if runtimeAsset, err := classifyPluginSourceAsset("new-helper.js"); err == nil || runtimeAsset {
		t.Fatalf("new runtime extension silently classified: runtime=%v err=%v", runtimeAsset, err)
	}
}


func TestFixOpencodePluginsRemovesLegacyTestAssetsAndRegistration(t *testing.T) {
	root := t.TempDir()
	installedRoot := filepath.Join(root, ".opencode", "plugins")
	if err := os.MkdirAll(installedRoot, 0o755); err != nil {
		t.Fatal(err)
	}

	embeddedDir := path.Join(starfleetctl.FragmentsRoot, opencodePluginsSubdir)
	entries, err := fs.ReadDir(starfleetctl.Fragments, embeddedDir)
	if err != nil {
		t.Fatalf("read embedded plugin source directory: %v", err)
	}
	var staleTests []string
	for _, entry := range entries {
		if entry.IsDir() || !isOpencodePluginTestAsset(entry.Name()) {
			continue
		}
		staleTests = append(staleTests, entry.Name())
		if err := os.WriteFile(filepath.Join(installedRoot, entry.Name()), []byte("legacy test asset"), 0o644); err != nil {
			t.Fatalf("create legacy installed test asset %s: %v", entry.Name(), err)
		}
	}
	if len(staleTests) == 0 {
		t.Fatal("expected source-side test assets to exercise legacy cleanup")
	}

	configPath := filepath.Join(root, ".opencode", "opencode.json")
	configBytes, err := json.Marshal(map[string]any{
		"plugin": []string{
			pluginConfigSpec("starfleet-dispatch.test.ts"),
			"./plugins/user-owned.ts",
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(configPath, configBytes, 0o644); err != nil {
		t.Fatal(err)
	}

	b := New(root)
	if ok, detail := verifyOpencodePlugins(b); ok || !strings.Contains(detail, "stale test assets") {
		t.Fatalf("verifier before migration = (%v, %q), want stale test asset finding", ok, detail)
	}
	if err := fixOpencodePlugins(b); err != nil {
		t.Fatalf("bootstrap plugin migration: %v", err)
	}
	if ok, detail := verifyOpencodePlugins(b); !ok {
		t.Fatalf("verifier after migration = (%v, %q), want clean install", ok, detail)
	}
	for _, name := range staleTests {
		if _, err := os.Stat(filepath.Join(installedRoot, name)); !os.IsNotExist(err) {
			t.Errorf("legacy test asset %s remains installed (stat err=%v)", name, err)
		}
	}

	configBytes, err = os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Plugin []string `json:"plugin"`
	}
	if err := json.Unmarshal(configBytes, &config); err != nil {
		t.Fatal(err)
	}
	for _, spec := range config.Plugin {
		if spec == pluginConfigSpec("starfleet-dispatch.test.ts") {
			t.Errorf("legacy test entry point remains registered: %q", spec)
		}
	}
	if !containsString(config.Plugin, "./plugins/user-owned.ts") {
		t.Errorf("bootstrap removed unrelated user plugin registration: %v", config.Plugin)
	}
}

func containsString(values []string, want string) bool {
	for _, value := range values {
		if value == want {
			return true
		}
	}
	return false
}
