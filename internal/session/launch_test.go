package session

import (
	"encoding/json"
	"os"
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
	perm, ok := cfg["permission"].(map[string]any)
	if !ok {
		t.Fatalf("no permission block in ship config")
	}
	ext, ok := perm["external_directory"].(map[string]any)
	if !ok {
		t.Fatalf("no external_directory in permission block")
	}
	val, ok := ext[pattern].(string)
	if !ok {
		t.Fatalf("no rule for pattern %q in external_directory", pattern)
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
