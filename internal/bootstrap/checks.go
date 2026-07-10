// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package bootstrap

import (
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	"github.com/metux/starfleetctl/internal/agents"
	"github.com/metux/starfleetctl/internal/dashboard"
	starfleetctl "github.com/metux/starfleetctl"
)

// Check is one idempotent bootstrap step: Verify reports whether it's
// already satisfied (ok) plus a human-readable detail; Fix repairs it.
// Running a Check whose Verify already returns ok is always safe (Fix is
// simply not called) — this is what makes re-running the whole bootstrap
// against an already-set-up checkout a no-op.
type Check struct {
	Name   string
	Verify func(b *Bootstrap) (ok bool, detail string)
	Fix    func(b *Bootstrap) error
}

// requiredDirs are every directory a starfleetctl subcommand lazily
// os.MkdirAll's on first use — listed here too so `bootstrap` can warm them
// all up in one pass (e.g. right after a fresh clone, before any subcommand
// has run yet) instead of relying on each one's own first invocation.
var requiredDirs = []string{
	filepath.Join("_WORK_", "agent-bus", "status"),
	filepath.Join("_WORK_", "agent-bus", "msgs"),
	filepath.Join("_WORK_", "agent-bus", "acks"),
	filepath.Join("_WORK_", "agent-bus", "ships"),
	filepath.Join("_WORK_", "agent-bus", "monitor-seen"),
	filepath.Join("_WORK_", "agent-bus", "notify", ".popup-once"),
	filepath.Join("_WORK_", "agent-claims"),
}

// requiredAllowEntries are the starfleetctl-specific permission rules
// `bootstrap` verifies/fixes in .claude/settings.json. Deliberately narrow —
// this is NOT a general settings.json linter, just the entries this tool
// itself depends on to run without a confirmation prompt every time.
var requiredAllowEntries = []string{
	"Bash(.starfleet-ai/bin/starfleetctl)",
	"Bash(.starfleet-ai/bin/starfleetctl *)",
}

// requiredPreToolHookMarker is the string we check for to decide whether the
// agent-permission-hook is already wired into .claude/settings.json.
const requiredPreToolHookMarker = "agent-permission-hook"

// permissionHookCommand is the JSON command value written into
// .claude/settings.json's PreToolUse entry. Uses $CLAUDE_PROJECT_DIR so it
// resolves regardless of which Claude-managed project the agent is running in.
const permissionHookCommand = `\"$CLAUDE_PROJECT_DIR\"/.claude/hooks/agent-permission-hook`

// opencodePluginsSubdir is the subdirectory in embedded fragments that holds
// opencode plugin files (not markdown fragments).
const opencodePluginsSubdir = "opencode-plugins"

// opencodeScriptsSubdir is the subdirectory in embedded fragments that holds
// opencode launcher scripts (run-opencode.flagship, run-opencode.ship).
const opencodeScriptsSubdir = "opencode-scripts"

// claudeHooksSubdir is the subdirectory in embedded fragments that holds
// Claude Code hook scripts (e.g. agent-permission-hook).
const claudeHooksSubdir = "claude-hooks"

// Checks returns the full, ordered set of bootstrap checks.
func Checks() []Check {
	return []Check{
		{
			Name:   "_WORK_ directory tree",
			Verify: verifyDirs,
			Fix:    fixDirs,
		},
		{
			Name:   "ship-names.txt present (.starfleet-ai/etc/)",
			Verify: verifyShipNamesFile,
			Fix:    nil, // not auto-fixable: this is source data, not a directory
		},
		{
			Name:   ".claude/settings.json: starfleetctl allowlist entries",
			Verify: verifySettingsAllowlist,
			Fix:    fixSettingsAllowlist,
		},
		{
			Name:   ".claude/settings.json: agent-permission-hook PreToolUse",
			Verify: verifySettingsPermissionHook,
			Fix:    fixSettingsPermissionHook,
		},
		{
			Name:   "AGENTS.md + agents.d/index.md",
			Verify: verifyAgentsMD,
			Fix:    fixAgentsMD,
		},
		{
			Name:   "DASHBOARD.md",
			Verify: verifyDashboardMD,
			Fix:    fixDashboardMD,
		},
		{
			Name:   "starfleetctl self-fragment (agents.d/starfleet/starfleetctl.md)",
			Verify: verifySelfFragment,
			Fix:    fixSelfFragment,
		},
		{
			Name:   "starfleet fragments (agents.d/starfleet/)",
			Verify: verifyStarfleetFragments,
			Fix:    fixStarfleetFragments,
		},
		{
			Name:   ".opencode/plugins/ directory",
			Verify: verifyOpencodePluginsDir,
			Fix:    fixOpencodePluginsDir,
		},
		{
			Name:   "opencode plugins (.opencode/plugins/)",
			Verify: verifyOpencodePlugins,
			Fix:    fixOpencodePlugins,
		},
		{
			Name:   "opencode launcher scripts (.starfleet-ai/bin/)",
			Verify: verifyOpencodeScripts,
			Fix:    fixOpencodeScripts,
		},
		{
			Name:   "claude hooks directory (.claude/hooks/)",
			Verify: verifyClaudeHooksDir,
			Fix:    fixClaudeHooksDir,
		},
		{
			Name:   "claude hooks (.claude/hooks/)",
			Verify: verifyClaudeHooks,
			Fix:    fixClaudeHooks,
		},
		{
			Name:   ".gitignore: claude hooks entry",
			Verify: verifyGitignoreClaudeHooks,
			Fix:    fixGitignoreClaudeHooks,
		},
	}
}

// selfFragmentOrder must match install-self's own CLI default (900) — kept
// as a literal here rather than a shared constant across packages, same as
// every other bootstrap check's specifics are self-contained.
const selfFragmentOrder = 900

// verifySelfFragment/fixSelfFragment: the "starfleetctl carries its own
// instructions" mechanism (praetor directive m0089, 2026-07-06) — this
// fragment is tool-owned and always overwritten on --fix (not just
// created-if-missing like the other checks), so a starfleetctl update run
// through `bootstrap --fix` also refreshes the instructions that came with
// it. Verify distinguishes "missing" from "stale" (present but from an
// older starfleetctl commit) by comparing against what install-self would
// write right now — a byte-different existing file is still reported as
// not-ok, unlike every other check here, precisely because this one is
// meant to always track the current binary.
func verifySelfFragment(b *Bootstrap) (bool, string) {
	a, err := agents.New(b.Root)
	if err != nil {
		return false, err.Error()
	}
	path := filepath.Join(a.FragmentsDir(), agents.SelfSlug+".md")
	data, err := os.ReadFile(path)
	if err != nil {
		return false, "missing"
	}
	current, err := agents.RenderSelfFragment(selfFragmentOrder)
	if err != nil {
		return false, err.Error()
	}
	if string(data) == string(current) {
		return true, "present, up to date"
	}
	return false, "present but stale (starfleetctl was updated since the last install-self)"
}

func fixSelfFragment(b *Bootstrap) error {
	a, err := agents.New(b.Root)
	if err != nil {
		return err
	}
	return a.DoInstallSelf(selfFragmentOrder)
}

// verifyAgentsMD/fixAgentsMD delegate to internal/agents' own
// EnsureBootstrapped — this is the "should be created fully automatically
// when needed" requirement: a truly from-scratch checkout gets a minimal,
// permanently-fixed root AGENTS.md (see internal/agents' doc comment for
// why it's structured as an @agents.d/index.md import) rather than bootstrap
// needing to know anything about that package's internals.
func verifyAgentsMD(b *Bootstrap) (bool, string) {
	if _, err := os.Stat(filepath.Join(b.Root, "AGENTS.md")); err == nil {
		return true, "present"
	}
	return false, "missing (no AGENTS.md at all)"
}

func fixAgentsMD(b *Bootstrap) error {
	a, err := agents.New(b.Root)
	if err != nil {
		return err
	}
	_, err = a.EnsureBootstrapped()
	return err
}

// verifyDashboardMD/fixDashboardMD: same idea as AGENTS.md above, but for
// DASHBOARD.md's dashboard/themes/ + reindex system (internal/dashboard).
// DASHBOARD.md is now a generated artifact under .starfleet-ai/ (not committed).
func verifyDashboardMD(b *Bootstrap) (bool, string) {
	path := filepath.Join(b.Root, ".starfleet-ai", "DASHBOARD.md")
	if _, err := os.Stat(path); err == nil {
		return true, "present"
	}
	return false, "missing (no .starfleet-ai/DASHBOARD.md at all)"
}

func fixDashboardMD(b *Bootstrap) error {
	d, err := dashboard.New(b.Root)
	if err != nil {
		return err
	}
	if _, err := d.EnsureBootstrapped(); err != nil {
		return err
	}
	// Also run reindex to populate tables from theme files (idempotent)
	return d.DoReindex()
}

func verifyDirs(b *Bootstrap) (bool, string) {
	var missing []string
	for _, d := range requiredDirs {
		if fi, err := os.Stat(filepath.Join(b.Root, d)); err != nil || !fi.IsDir() {
			missing = append(missing, d)
		}
	}
	if len(missing) == 0 {
		return true, fmt.Sprintf("%d/%d present", len(requiredDirs), len(requiredDirs))
	}
	return false, fmt.Sprintf("missing: %s", strings.Join(missing, ", "))
}

func fixDirs(b *Bootstrap) error {
	for _, d := range requiredDirs {
		if err := os.MkdirAll(filepath.Join(b.Root, d), 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", d, err)
		}
	}
	return nil
}

func verifyShipNamesFile(b *Bootstrap) (bool, string) {
	path := filepath.Join(b.Root, ".starfleet-ai", "etc", "ship-names.txt")
	if fi, err := os.Stat(path); err == nil && !fi.IsDir() {
		return true, "present"
	}
	return false, fmt.Sprintf("missing %s (not auto-fixable — this is source data, not something bootstrap can invent; run genesis-init or copy from the symlink at scripts/ship-names.txt)", path)
}

func verifySettingsAllowlist(b *Bootstrap) (bool, string) {
	if _, err := os.Stat(b.SettingsFile); err != nil {
		return false, fmt.Sprintf("missing (no %s at all)", b.SettingsFile)
	}
	allow, err := readAllowList(b.SettingsFile)
	if err != nil {
		return false, fmt.Sprintf("could not read/parse %s: %v", b.SettingsFile, err)
	}
	present := map[string]bool{}
	for _, a := range allow {
		present[a] = true
	}
	var missing []string
	for _, want := range requiredAllowEntries {
		if !present[want] {
			missing = append(missing, want)
		}
	}
	if len(missing) == 0 {
		return true, "present"
	}
	return false, fmt.Sprintf("missing: %s", strings.Join(missing, ", "))
}

// fixSettingsAllowlist inserts any missing required entries right after the
// `"allow": [` line, as a targeted text edit rather than a full JSON
// marshal/remarshal — this file is shared, hand-formatted, and actively
// edited by other sessions, so preserving its exact formatting/ordering for
// every OTHER entry matters (a full re-marshal would reformat the whole
// file and produce a huge, noisy diff).
// minimalSettingsSkeleton is written when .claude/settings.json doesn't
// exist at all yet (a truly fresh checkout, e.g. via `genesis-init`) — just
// enough structure for fixSettingsAllowlist's own marker-based insertion
// below to then add the required entries into a real (if empty) array.
const minimalSettingsSkeleton = `{
  "permissions": {
    "allow": []
  }
}
`

// verifySettingsPermissionHook checks that the agent-permission-hook is
// wired into .claude/settings.json's PreToolUse hooks.
func verifySettingsPermissionHook(b *Bootstrap) (bool, string) {
	if _, err := os.Stat(b.SettingsFile); err != nil {
		return false, fmt.Sprintf("missing (no %s at all)", b.SettingsFile)
	}
	data, err := os.ReadFile(b.SettingsFile)
	if err != nil {
		return false, fmt.Sprintf("could not read %s: %v", b.SettingsFile, err)
	}
	content := string(data)
	if strings.Contains(content, requiredPreToolHookMarker) {
		return true, "present"
	}
	return false, "missing agent-permission-hook in PreToolUse hooks"
}

// fixSettingsPermissionHook inserts the agent-permission-hook entry into
// .claude/settings.json's PreToolUse hooks, right after the existing
// confirm-log-hook entry.
func fixSettingsPermissionHook(b *Bootstrap) error {
	if _, err := os.Stat(b.SettingsFile); err != nil {
		return nil
	}
	data, err := os.ReadFile(b.SettingsFile)
	if err != nil {
		return err
	}
	content := string(data)
	if strings.Contains(content, requiredPreToolHookMarker) {
		return nil
	}

	marker := `"statusMessage": "confirm-log: telemetry"`
	idx := strings.Index(content, marker)
	if idx < 0 {
		return fmt.Errorf("could not find %q in %s — cannot auto-wire permission hook", marker, b.SettingsFile)
	}
	closingBrace := strings.Index(content[idx:], "\n")
	if closingBrace < 0 {
		return fmt.Errorf("malformed settings.json near confirm-log-hook marker")
	}
	insertAt := idx + closingBrace
	rest := content[insertAt:]
	objEnd := strings.Index(rest, "          }")
	if objEnd < 0 {
		return fmt.Errorf("could not find hook object boundary near confirm-log-hook")
	}
	insertAt += objEnd + len("          }")

	hookEntry := `,
          {
            "type": "command",
            "timeout": 120,
            "command": "` + permissionHookCommand + `",
            "statusMessage": "agent-permission: 1st officer"
          }`

	newContent := content[:insertAt] + hookEntry + content[insertAt:]

	var probe any
	if err := json.Unmarshal([]byte(newContent), &probe); err != nil {
		return fmt.Errorf("edit would produce invalid JSON, aborting: %w", err)
	}
	return os.WriteFile(b.SettingsFile, []byte(newContent), 0o644)
}

func fixSettingsAllowlist(b *Bootstrap) error {
	if _, err := os.Stat(b.SettingsFile); err != nil {
		if err := os.MkdirAll(filepath.Dir(b.SettingsFile), 0o755); err != nil {
			return err
		}
		if err := os.WriteFile(b.SettingsFile, []byte(minimalSettingsSkeleton), 0o644); err != nil {
			return err
		}
	}
	data, err := os.ReadFile(b.SettingsFile)
	if err != nil {
		return err
	}
	allow, err := readAllowList(b.SettingsFile)
	if err != nil {
		return err
	}
	present := map[string]bool{}
	for _, a := range allow {
		present[a] = true
	}
	var toAdd []string
	for _, want := range requiredAllowEntries {
		if !present[want] {
			toAdd = append(toAdd, want)
		}
	}
	if len(toAdd) == 0 {
		return nil
	}

	content := string(data)
	marker := `"allow": [`
	idx := strings.Index(content, marker)
	if idx < 0 {
		return fmt.Errorf("could not find %q in %s — refusing to guess where to insert", marker, b.SettingsFile)
	}
	insertAt := idx + len(marker)
	// If the array is otherwise empty (next non-whitespace is the closing
	// bracket), the LAST inserted entry must not carry a trailing comma —
	// JSON has no trailing commas. Found this the hard way testing against
	// a from-scratch settings.json with "allow": [] (2026-07-06).
	arrayOtherwiseEmpty := strings.HasPrefix(strings.TrimLeft(content[insertAt:], " \t\r\n"), "]")
	// Match the indentation of the line the marker itself is on, plus one
	// level (2 spaces), matching this file's existing style throughout.
	var lines []string
	for i, e := range toAdd {
		comma := ","
		if arrayOtherwiseEmpty && i == len(toAdd)-1 {
			comma = ""
		}
		lines = append(lines, fmt.Sprintf("\n      %q%s", e, comma))
	}
	newContent := content[:insertAt] + strings.Join(lines, "") + content[insertAt:]

	// Validate before writing: a broken settings.json silently disables
	// every setting sourced from this file — never leave it invalid.
	var probe any
	if err := json.Unmarshal([]byte(newContent), &probe); err != nil {
		return fmt.Errorf("edit would produce invalid JSON, aborting: %w", err)
	}
	return os.WriteFile(b.SettingsFile, []byte(newContent), 0o644)
}

// verifyStarfleetFragments checks that every .md file embedded under
// fragments/starfleet/ in the starfleetctl binary is installed to
// agents.d/<slug>.md and byte-identical to what the current binary would
// write. This is a bulk, always-overwrite check like the self-fragment.
func verifyStarfleetFragments(b *Bootstrap) (bool, string) {
	a, err := agents.New(b.Root)
	if err != nil {
		return false, err.Error()
	}
	entries, err := fs.ReadDir(starfleetctl.Fragments, filepath.Join(starfleetctl.FragmentsRoot, agents.StarfleetSubdir))
	if err != nil {
		return false, err.Error()
	}
	var missing, stale []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		current, err := agents.RenderStarfleetFragment(agents.StarfleetSubdir, e.Name())
		if err != nil {
			return false, err.Error()
		}
		// Derive slug to find the installed path.
		meta, _, err := agents.ParseEmbeddedFragment(starfleetctl.Fragments, agents.StarfleetSubdir, e.Name())
		if err != nil {
			return false, err.Error()
		}
		installedPath := a.FragmentsDir() + string(os.PathSeparator) + meta.Slug + ".md"
		data, err := os.ReadFile(installedPath)
		if err != nil {
			missing = append(missing, meta.Slug)
			continue
		}
		if string(data) != string(current) {
			stale = append(stale, meta.Slug)
		}
	}
	if len(missing) == 0 && len(stale) == 0 {
		return true, fmt.Sprintf("%d/%d present, up to date", len(entries), len(entries))
	}
	var parts []string
	if len(missing) > 0 {
		parts = append(parts, fmt.Sprintf("missing: %s", strings.Join(missing, ", ")))
	}
	if len(stale) > 0 {
		parts = append(parts, fmt.Sprintf("stale: %s", strings.Join(stale, ", ")))
	}
	return false, strings.Join(parts, "; ")
}

func fixStarfleetFragments(b *Bootstrap) error {
	a, err := agents.New(b.Root)
	if err != nil {
		return err
	}
	return a.DoInstallStarfleet(agents.StarfleetSubdir)
}

func readAllowList(path string) ([]string, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var doc struct {
		Permissions struct {
			Allow []string `json:"allow"`
		} `json:"permissions"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return nil, err
	}
	return doc.Permissions.Allow, nil
}

// verifyOpencodePluginsDir checks that the .opencode/plugins directory exists.
func verifyOpencodePluginsDir(b *Bootstrap) (bool, string) {
	path := filepath.Join(b.Root, ".opencode", "plugins")
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		return true, "present"
	}
	return false, "missing .opencode/plugins/"
}

func fixOpencodePluginsDir(b *Bootstrap) error {
	path := filepath.Join(b.Root, ".opencode", "plugins")
	return os.MkdirAll(path, 0o755)
}

// verifyOpencodePlugins checks that every embedded opencode plugin file is
// installed to .opencode/plugins/ and byte-identical to what the current binary would write.
func verifyOpencodePlugins(b *Bootstrap) (bool, string) {
	entries, err := fs.ReadDir(starfleetctl.Fragments, filepath.Join(starfleetctl.FragmentsRoot, opencodePluginsSubdir))
	if err != nil {
		// If the directory doesn't exist in the embedded FS, that's OK — no plugins to install.
		return true, "no embedded opencode plugins"
	}
	var missing, stale []string
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ts") {
			continue
		}
		current, err := fs.ReadFile(starfleetctl.Fragments, filepath.Join(starfleetctl.FragmentsRoot, opencodePluginsSubdir, e.Name()))
		if err != nil {
			return false, err.Error()
		}
		installedPath := filepath.Join(b.Root, ".opencode", "plugins", e.Name())
		data, err := os.ReadFile(installedPath)
		if err != nil {
			missing = append(missing, e.Name())
			continue
		}
		if string(data) != string(current) {
			stale = append(stale, e.Name())
		}
	}
	if len(missing) == 0 && len(stale) == 0 {
		return true, fmt.Sprintf("%d/%d present, up to date", len(entries), len(entries))
	}
	var parts []string
	if len(missing) > 0 {
		parts = append(parts, fmt.Sprintf("missing: %s", strings.Join(missing, ", ")))
	}
	if len(stale) > 0 {
		parts = append(parts, fmt.Sprintf("stale: %s", strings.Join(stale, ", ")))
	}
	return false, strings.Join(parts, "; ")
}

func fixOpencodePlugins(b *Bootstrap) error {
	entries, err := fs.ReadDir(starfleetctl.Fragments, filepath.Join(starfleetctl.FragmentsRoot, opencodePluginsSubdir))
	if err != nil {
		// No embedded plugins directory — nothing to do.
		return nil
	}
	destDir := filepath.Join(b.Root, ".opencode", "plugins")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ts") {
			continue
		}
		data, err := fs.ReadFile(starfleetctl.Fragments, filepath.Join(starfleetctl.FragmentsRoot, opencodePluginsSubdir, e.Name()))
		if err != nil {
			return err
		}
		destPath := filepath.Join(destDir, e.Name())
		if err := os.WriteFile(destPath, data, 0o644); err != nil {
			return err
		}
	}
	return nil
}

// verifyOpencodeScripts checks that every embedded opencode launcher script is
// installed to .starfleet-ai/bin/ and byte-identical.
func verifyOpencodeScripts(b *Bootstrap) (bool, string) {
	entries, err := fs.ReadDir(starfleetctl.Fragments, filepath.Join(starfleetctl.FragmentsRoot, opencodeScriptsSubdir))
	if err != nil {
		return true, "no embedded opencode scripts"
	}
	var missing, stale []string
	destDir := filepath.Join(b.Root, ".starfleet-ai", "bin")
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		current, err := fs.ReadFile(starfleetctl.Fragments, filepath.Join(starfleetctl.FragmentsRoot, opencodeScriptsSubdir, e.Name()))
		if err != nil {
			return false, err.Error()
		}
		installedPath := filepath.Join(destDir, e.Name())
		data, err := os.ReadFile(installedPath)
		if err != nil {
			missing = append(missing, e.Name())
			continue
		}
		if string(data) != string(current) {
			stale = append(stale, e.Name())
		}
	}
	if len(missing) == 0 && len(stale) == 0 {
		return true, fmt.Sprintf("%d/%d present, up to date", len(entries), len(entries))
	}
	var parts []string
	if len(missing) > 0 {
		parts = append(parts, fmt.Sprintf("missing: %s", strings.Join(missing, ", ")))
	}
	if len(stale) > 0 {
		parts = append(parts, fmt.Sprintf("stale: %s", strings.Join(stale, ", ")))
	}
	return false, strings.Join(parts, "; ")
}

func fixOpencodeScripts(b *Bootstrap) error {
	entries, err := fs.ReadDir(starfleetctl.Fragments, filepath.Join(starfleetctl.FragmentsRoot, opencodeScriptsSubdir))
	if err != nil {
		return nil
	}
	destDir := filepath.Join(b.Root, ".starfleet-ai", "bin")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := fs.ReadFile(starfleetctl.Fragments, filepath.Join(starfleetctl.FragmentsRoot, opencodeScriptsSubdir, e.Name()))
		if err != nil {
			return err
		}
		destPath := filepath.Join(destDir, e.Name())
		if err := os.WriteFile(destPath, data, 0o755); err != nil {
			return err
		}
	}
	return nil
}

func verifyClaudeHooksDir(b *Bootstrap) (bool, string) {
	path := filepath.Join(b.Root, ".claude", "hooks")
	if fi, err := os.Stat(path); err == nil && fi.IsDir() {
		return true, "present"
	}
	return false, "missing .claude/hooks/"
}

func fixClaudeHooksDir(b *Bootstrap) error {
	path := filepath.Join(b.Root, ".claude", "hooks")
	return os.MkdirAll(path, 0o755)
}

func verifyClaudeHooks(b *Bootstrap) (bool, string) {
	entries, err := fs.ReadDir(starfleetctl.Fragments, filepath.Join(starfleetctl.FragmentsRoot, claudeHooksSubdir))
	if err != nil {
		return true, "no embedded claude hooks"
	}
	var missing, stale []string
	destDir := filepath.Join(b.Root, ".claude", "hooks")
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		current, err := fs.ReadFile(starfleetctl.Fragments, filepath.Join(starfleetctl.FragmentsRoot, claudeHooksSubdir, e.Name()))
		if err != nil {
			return false, err.Error()
		}
		installedPath := filepath.Join(destDir, e.Name())
		data, err := os.ReadFile(installedPath)
		if err != nil {
			missing = append(missing, e.Name())
			continue
		}
		if string(data) != string(current) {
			stale = append(stale, e.Name())
		}
	}
	if len(missing) == 0 && len(stale) == 0 {
		return true, fmt.Sprintf("%d/%d present, up to date", len(entries), len(entries))
	}
	var parts []string
	if len(missing) > 0 {
		parts = append(parts, fmt.Sprintf("missing: %s", strings.Join(missing, ", ")))
	}
	if len(stale) > 0 {
		parts = append(parts, fmt.Sprintf("stale: %s", strings.Join(stale, ", ")))
	}
	return false, strings.Join(parts, "; ")
}

func fixClaudeHooks(b *Bootstrap) error {
	entries, err := fs.ReadDir(starfleetctl.Fragments, filepath.Join(starfleetctl.FragmentsRoot, claudeHooksSubdir))
	if err != nil {
		return nil
	}
	destDir := filepath.Join(b.Root, ".claude", "hooks")
	if err := os.MkdirAll(destDir, 0o755); err != nil {
		return err
	}
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		data, err := fs.ReadFile(starfleetctl.Fragments, filepath.Join(starfleetctl.FragmentsRoot, claudeHooksSubdir, e.Name()))
		if err != nil {
			return err
		}
		destPath := filepath.Join(destDir, e.Name())
		if err := os.WriteFile(destPath, data, 0o755); err != nil {
			return err
		}
	}
	return nil
}

const gitignoreClaudeHooksEntry = "/.claude/hooks/"

func verifyGitignoreClaudeHooks(b *Bootstrap) (bool, string) {
	path := filepath.Join(b.Root, ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil {
		return false, fmt.Sprintf("missing .gitignore: %v", err)
	}
	if strings.Contains(string(data), gitignoreClaudeHooksEntry) {
		return true, "present"
	}
	return false, "missing /.claude/hooks/ entry in .gitignore"
}

func fixGitignoreClaudeHooks(b *Bootstrap) error {
	path := filepath.Join(b.Root, ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("reading .gitignore: %w", err)
	}
	content := string(data)
	if !strings.HasSuffix(content, "\n") {
		content += "\n"
	}
	content += gitignoreClaudeHooksEntry + "\n"
	return os.WriteFile(path, []byte(content), 0o644)
}
