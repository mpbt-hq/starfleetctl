package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// readShipConfig decodes a generated per-ship opencode config.
func readShipConfig(t *testing.T, path string) map[string]any {
	t.Helper()
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var cfg map[string]any
	if err := json.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("unmarshal %s: %v", path, err)
	}
	return cfg
}

// extDirRule returns the resolved value for the given external_directory pattern.
func extDirRule(t *testing.T, cfg map[string]any, pattern string) string {
	t.Helper()
	return permRule(t, cfg, "external_directory", pattern)
}

// permRule returns the resolved value for the given tool/pattern rule.
func permRule(t *testing.T, cfg map[string]any, tool, pattern string) string {
	t.Helper()
	perm, ok := cfg["permission"].(map[string]any)
	if !ok {
		t.Fatalf("no permission block in ship config")
	}
	rules, ok := perm[tool].(map[string]any)
	if !ok {
		t.Fatalf("no rules for tool %q in permission block", tool)
	}
	val, ok := rules[pattern].(string)
	if !ok {
		t.Fatalf("no rule for pattern %q in tool %q", pattern, tool)
	}
	return val
}

// TestGenerateOpencodeConfigExternalDirectory verifies that ships never hit an
// unanswerable permission ask for paths outside the workspace.
//
// opencode's external_directory defaults to "ask" and fires for any path
// outside the project working directory. On background/auto ships there is no
// human to answer, so an ask hangs the ship and it then aborts after deny
// (regression: Pasteur trying to read "/.starfleet-ai/...").
func TestGenerateOpencodeConfigExternalDirectory(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	t.Setenv("HOME", home)
	workspacePattern := root + "/**"

	// background/auto ships: deny everything outside the workspace (fast
	// failure, agent can recover) while keeping the workspace + ~/.local/bin
	// allowed.
	for _, launchType := range []string{"background", "auto"} {
		cfgPath, err := generateOpencodeConfig(root, "TestShip", launchType, false)
		if err != nil {
			t.Fatalf("generateOpencodeConfig(%q): %v", launchType, err)
		}
		cfg := readShipConfig(t, cfgPath)
		if got := extDirRule(t, cfg, "**"); got != "deny" {
			t.Errorf("%s: external_directory ** = %q, want deny", launchType, got)
		}
		if got := extDirRule(t, cfg, workspacePattern); got != "allow" {
			t.Errorf("%s: external_directory %q = %q, want allow", launchType, workspacePattern, got)
		}
		if got := extDirRule(t, cfg, home+"/.local/bin/**"); got != "allow" {
			t.Errorf("%s: external_directory ~/.local/bin/** = %q, want allow", launchType, got)
		}
	}

	// terminal ships: keep "ask" so the human at the console can decide.
	cfgPath, err := generateOpencodeConfig(root, "TestShip", "terminal", false)
	if err != nil {
		t.Fatalf("generateOpencodeConfig(terminal): %v", err)
	}
	if got := extDirRule(t, readShipConfig(t, cfgPath), "**"); got != "ask" {
		t.Errorf("terminal: external_directory ** = %q, want ask", got)
	}

	// unrestricted ships: allow everything.
	cfgPath, err = generateOpencodeConfig(root, "TestShip", "background", true)
	if err != nil {
		t.Fatalf("generateOpencodeConfig(unrestricted): %v", err)
	}
	if got := extDirRule(t, readShipConfig(t, cfgPath), "**"); got != "allow" {
		t.Errorf("unrestricted: external_directory ** = %q, want allow", got)
	}
}

// TestGenerateOpencodeConfigWorkspaceTools verifies that the workspace file
// tools (read/write/edit/glob/grep/task) are allowed for every launch type.
//
// These tools pass paths relative to the worktree, so the single "**" rule
// matches all in-workspace access; paths outside the workspace are gated by
// external_directory, not by these rules. Applying the launch-type default
// (ask/deny) here would prompt for every banal read on terminal ships and
// lock background ships out of their own tree (regression after the
// absolute-vs-relative pattern fix).
func TestGenerateOpencodeConfigWorkspaceTools(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	t.Setenv("HOME", home)

	for _, launchType := range []string{"terminal", "background", "auto"} {
		cfgPath, err := generateOpencodeConfig(root, "TestShip", launchType, false)
		if err != nil {
			t.Fatalf("generateOpencodeConfig(%q): %v", launchType, err)
		}
		cfg := readShipConfig(t, cfgPath)
		for _, tool := range []string{"read", "write", "edit", "glob", "grep", "task"} {
			if got := permRule(t, cfg, tool, "**"); got != "allow" {
				t.Errorf("%s: %s ** = %q, want allow", launchType, tool, got)
			}
		}
		// bash: allowed for every launch type (mirrors opencode's permissive
		// default — the fleet ran without bash prompts before per-ship
		// configs, and terminal "ask" regressed that).
		if got := permRule(t, cfg, "bash", "**"); got != "allow" {
			t.Errorf("%s: bash ** = %q, want allow", launchType, got)
		}
		if got := permRule(t, cfg, "bash", "starfleetctl **"); got != "allow" {
			t.Errorf("%s: bash starfleetctl ** = %q, want allow", launchType, got)
		}
	}

	// unrestricted: workspace tools allowed, external allowed.
	cfgPath, err := generateOpencodeConfig(root, "TestShip", "background", true)
	if err != nil {
		t.Fatalf("generateOpencodeConfig(unrestricted): %v", err)
	}
	cfg := readShipConfig(t, cfgPath)
	for _, tool := range []string{"read", "write", "edit", "glob", "grep", "task"} {
		if got := permRule(t, cfg, tool, "**"); got != "allow" {
			t.Errorf("unrestricted: %s ** = %q, want allow", tool, got)
		}
	}
}

// TestGenerateOpencodeConfigUsername verifies every generated per-ship config
// carries the ship ID as its username, so opencode sessions/commits are
// attributed to the ship rather than the OS user.
func TestGenerateOpencodeConfigUsername(t *testing.T) {
	root := t.TempDir()
	for _, launchType := range []string{"terminal", "background", "auto"} {
		cfgPath, err := generateOpencodeConfig(root, "TestShip", launchType, false)
		if err != nil {
			t.Fatalf("generateOpencodeConfig(%q): %v", launchType, err)
		}
		cfg := readShipConfig(t, cfgPath)
		if got, _ := cfg["username"].(string); got != "TestShip" {
			t.Errorf("%s: username = %q, want TestShip", launchType, got)
		}
	}
}

// TestGenerateOpencodeConfigEnabledProviders verifies that in model-proxy-only
// mode the generated per-ship config pins an `enabled_providers` allowlist of
// just the model-proxy backends. This is what actually keeps the user's
// globally-configured providers (~/.config/opencode/opencode.json) out of the
// ship's /models picker: opencode MERGES config files instead of replacing
// them, so merely omitting those providers from the ship config does not
// remove them. Default mode ("all") must NOT set the allowlist.
func TestGenerateOpencodeConfigEnabledProviders(t *testing.T) {
	home := t.TempDir()
	root := t.TempDir()
	t.Setenv("HOME", home)

	confDir := filepath.Join(root, ".starfleet-ai", "conf")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Two model-proxy backends. base_url points at a dead port so the catalog
	// probes fail fast instead of hanging.
	mpYaml := `model_proxy:
  listen_addr: "127.0.0.1:1"
  providers:
    - id: nim-proxy
      name: "NIM proxy"
      base_url: "http://127.0.0.1:1/v1"
    - id: zen-proxy
      name: "ZEN proxy"
      base_url: "http://127.0.0.1:1/v1"
`
	if err := os.WriteFile(filepath.Join(confDir, "model-proxy.yaml"), []byte(mpYaml), 0o644); err != nil {
		t.Fatal(err)
	}

	// Default mode ("all"): no allowlist — user providers are allowed.
	cfgPath, err := generateOpencodeConfig(root, "TestShip", "background", false)
	if err != nil {
		t.Fatalf("generateOpencodeConfig(default): %v", err)
	}
	if _, ok := readShipConfig(t, cfgPath)["enabled_providers"]; ok {
		t.Errorf("default mode: enabled_providers present, want absent")
	}

	// model-proxy-only mode: allowlist == the proxy provider ids.
	fleetYaml := `fleet:
  provider_mode: "model-proxy-only"
`
	if err := os.WriteFile(filepath.Join(confDir, "fleet.yaml"), []byte(fleetYaml), 0o644); err != nil {
		t.Fatal(err)
	}
	cfgPath, err = generateOpencodeConfig(root, "TestShip", "background", false)
	if err != nil {
		t.Fatalf("generateOpencodeConfig(model-proxy-only): %v", err)
	}
	got, ok := readShipConfig(t, cfgPath)["enabled_providers"].([]any)
	if !ok {
		t.Fatalf("model-proxy-only: enabled_providers = %v (%T), want []any", readShipConfig(t, cfgPath)["enabled_providers"], readShipConfig(t, cfgPath)["enabled_providers"])
	}
	want := []string{"nim-proxy", "zen-proxy"}
	if len(got) != len(want) {
		t.Fatalf("model-proxy-only: enabled_providers = %v, want %v", got, want)
	}
	for i, id := range want {
		if got[i] != id {
			t.Errorf("model-proxy-only: enabled_providers[%d] = %v, want %q", i, got[i], id)
		}
	}
}
