// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package bootstrap

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"

	starfleetctl "github.com/metux/starfleetctl"
	"github.com/metux/starfleetctl/internal/agents"
	"github.com/metux/starfleetctl/internal/dashboard"
	"github.com/metux/starfleetctl/internal/templates"
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
	filepath.Join(".starfleet-ai", "conf"),
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

// claudeScriptsSubdir is the subdirectory in embedded fragments that holds
// Claude Code launcher scripts (run-claude.flagship, run-claude.ship).
const claudeScriptsSubdir = "claude-scripts"

// Checks returns the full, ordered set of bootstrap checks.
func Checks() []Check {
	return []Check{
		{
			Name:   "_WORK_ directory tree",
			Verify: verifyDirs,
			Fix:    fixDirs,
		},
		{
			Name:   "ship-names.txt present & non-empty (.starfleet-ai/etc/)",
			Verify: verifyShipNamesFile,
			Fix:    fixShipNamesFile,
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
			Name:   ".starfleet-ai/agents.d/index.md",
			Verify: verifyAgentsIndex,
			Fix:    fixAgentsIndex,
		},
		{
			Name:   "DASHBOARD.md",
			Verify: verifyDashboardMD,
			Fix:    fixDashboardMD,
		},

		{
			Name:   "starfleet fragments (agents.d/starfleet/)",
			Verify: verifyStarfleetFragments,
			Fix:    fixStarfleetFragments,
		},
		{
			Name:   "starfleet skills (.claude/skills/)",
			Verify: verifyStarfleetSkills,
			Fix:    fixStarfleetSkills,
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
			Name:   "claude launcher scripts (.starfleet-ai/bin/)",
			Verify: verifyClaudeScripts,
			Fix:    fixClaudeScripts,
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
			Name:   ".starfleet-ai/.gitignore (ephemeral dirs only)",
			Verify: verifyStarfleetAIGitignore,
			Fix:    fixStarfleetAIGitignore,
		},
		{
			Name:   ".starfleet-ai/conf/web.yaml (web server config)",
			Verify: verifyWebConf,
			Fix:    fixWebConf,
		},
		{
			Name:   ".gitignore: claude hooks entry",
			Verify: verifyGitignoreClaudeHooks,
			Fix:    fixGitignoreClaudeHooks,
		},
	}
}

// verifyAgentsIndex/fixAgentsIndex ensure .starfleet-ai/agents.d/index.md exists.
func verifyAgentsIndex(b *Bootstrap) (bool, string) {
	path := filepath.Join(b.Root, ".starfleet-ai", "agents.d", "index.md")
	if _, err := os.Stat(path); err == nil {
		return true, "present"
	}
	return false, "missing (no .starfleet-ai/agents.d/index.md)"
}

func fixAgentsIndex(b *Bootstrap) error {
	a, err := agents.New(b.Root)
	if err != nil {
		return err
	}
	_, err = a.EnsureBootstrapped()
	return err
}

// verifyDashboardMD/fixDashboardMD: same idea as CLAUDE.md above, but for
// DASHBOARD.md's dashboard/topics/ + reindex system (internal/dashboard).
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
	// Also run reindex to populate tables from topic files (idempotent)
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
	path := filepath.Join(b.Root, templates.ShipNamesRel)
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() {
		return false, fmt.Sprintf("missing %s", path)
	}
	if fi.Size() == 0 {
		return false, fmt.Sprintf("empty %s (bootstrap --fix / genesis-init refills it from the embedded template)", path)
	}
	return true, "present, non-empty"
}

// fixShipNamesFile (re)creates the ship-name pool from the embedded template
// when it's missing or empty. A non-empty pool is left untouched, so any
// names a user has added locally are preserved.
func fixShipNamesFile(b *Bootstrap) error {
	dest := filepath.Join(b.Root, templates.ShipNamesRel)
	if fi, err := os.Stat(dest); err == nil && fi.Size() > 0 {
		return nil // present and non-empty — leave any local edits alone
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return err
	}
	return os.WriteFile(dest, templates.ShipNames, 0o644)
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
// .starfleet-ai/agents.d/starfleet/<slug>.md and byte-identical to what the
// current binary would write. This is a bulk, always-overwrite check like the
// self-fragment.
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
		// slug is "starfleet/<name>" — installed under StarfleetFragmentsDir()
		fname := meta.Slug
		if i := strings.LastIndex(fname, "/"); i >= 0 {
			fname = fname[i+1:]
		}
		installedPath := filepath.Join(a.StarfleetFragmentsDir(), fname+".md")
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

// verifyStarfleetSkills checks that every skill directory embedded under
// fragments/starfleet-skills/ is installed to .claude/skills/<name>/ and
// byte-identical to what the current binary would write.
func verifyStarfleetSkills(b *Bootstrap) (bool, string) {
	a, err := agents.New(b.Root)
	if err != nil {
		return false, err.Error()
	}
	skillsDir := filepath.Join(starfleetctl.FragmentsRoot, agents.StarfleetSkillsSubdir)
	entries, err := fs.ReadDir(starfleetctl.Fragments, skillsDir)
	if err != nil {
		return true, "no embedded starfleet skills"
	}
	var missing, stale []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		skillName := e.Name()
		skillDir := filepath.Join(skillsDir, skillName)
		skillEntries, err := fs.ReadDir(starfleetctl.Fragments, skillDir)
		if err != nil {
			return false, err.Error()
		}
		for _, f := range skillEntries {
			if f.IsDir() || !strings.HasSuffix(f.Name(), ".md") {
				continue
			}
			current, err := fs.ReadFile(starfleetctl.Fragments, filepath.Join(skillDir, f.Name()))
			if err != nil {
				return false, err.Error()
			}
			installedPath := filepath.Join(a.SkillsDir(), skillName, f.Name())
			data, err := os.ReadFile(installedPath)
			if err != nil {
				missing = append(missing, skillName+"/"+f.Name())
				continue
			}
			if string(data) != string(current) {
				stale = append(stale, skillName+"/"+f.Name())
			}
		}
	}
	if len(missing) == 0 && len(stale) == 0 {
		return true, fmt.Sprintf("%d skill dirs present, up to date", len(entries))
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

func fixStarfleetSkills(b *Bootstrap) error {
	a, err := agents.New(b.Root)
	if err != nil {
		return err
	}
	return a.DoInstallStarfleetSkills()
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
// installed to .opencode/plugins/, byte-identical to what the current binary
// would write, AND registered in .opencode/opencode.json's "plugin" array
// (openblocks only auto-loads plugins from the array, not by file presence
// alone — without registration the agent-bus polling/injection never runs).
func verifyOpencodePlugins(b *Bootstrap) (bool, string) {
	entries, err := fs.ReadDir(starfleetctl.Fragments, filepath.Join(starfleetctl.FragmentsRoot, opencodePluginsSubdir))
	if err != nil {
		// If the directory doesn't exist in the embedded FS, that's OK — no plugins to install.
		return true, "no embedded opencode plugins"
	}
	var missing, stale, unregistered []string
	registered := loadOpencodePluginConfig(b)
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
		// Plugin must be registered in .opencode/opencode.json so opencode
		// actually loads it. Path is relative to .opencode/ (see resolvePluginSpec
		// path handling) to avoid the doubled-.opencode path bug.
		if !registered[pluginConfigSpec(e.Name())] {
			unregistered = append(unregistered, e.Name())
		}
	}
	if len(missing) == 0 && len(stale) == 0 && len(unregistered) == 0 {
		return true, fmt.Sprintf("%d/%d present, up to date, registered", len(entries), len(entries))
	}
	var parts []string
	if len(missing) > 0 {
		parts = append(parts, fmt.Sprintf("missing: %s", strings.Join(missing, ", ")))
	}
	if len(stale) > 0 {
		parts = append(parts, fmt.Sprintf("stale: %s", strings.Join(stale, ", ")))
	}
	if len(unregistered) > 0 {
		parts = append(parts, fmt.Sprintf("unregistered: %s", strings.Join(unregistered, ", ")))
	}
	return false, strings.Join(parts, "; ")
}

// pluginConfigSpec returns the opencode config "plugin" array entry for a
// plugin filename. opencode resolves plugin specs relative to the config
// directory (.opencode/), so we use "./plugins/<name>.ts" rather than a
// project-root-relative or absolute path (the latter triggers a doubled
// ".opencode/.opencode/plugins" resolution bug).
func pluginConfigSpec(name string) string {
	return "./plugins/" + name
}

// loadOpencodePluginConfig reads .opencode/opencode.json and returns the set
// of plugin specs already present in its "plugin" array. A missing or
// unparsable file yields an empty set (so fix will register everything).
func loadOpencodePluginConfig(b *Bootstrap) map[string]bool {
	out := map[string]bool{}
	path := filepath.Join(b.Root, ".opencode", "opencode.json")
	data, err := os.ReadFile(path)
	if err != nil {
		return out
	}
	var doc struct {
		Plugin []string `json:"plugin"`
	}
	if err := json.Unmarshal(data, &doc); err != nil {
		return out
	}
	for _, spec := range doc.Plugin {
		out[spec] = true
	}
	return out
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

	// Remove a previously-installed plugin under its OLD name, so a rename
	// doesn't leave two copies loaded at once (duplicate inbox injection).
	// Keep this list in sync with any fragment renames in
	// fragments/opencode-plugins/.
	for _, stale := range []string{"agent-bus-poller.ts"} {
		if err := os.Remove(filepath.Join(destDir, stale)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
	}

	// Register the embedded plugin(s) in .opencode/package.json, but only if
	// the manifest is absent — don't clobber a workspace-local manifest.
	pkgPath := filepath.Join(b.Root, ".opencode", "package.json")
	if _, err := os.Stat(pkgPath); errors.Is(err, os.ErrNotExist) {
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".ts") {
				continue
			}
			name := strings.TrimSuffix(e.Name(), ".ts")
			manifest := map[string]any{
				"name":         name,
				"version":      "1.0.0",
				"type":         "module",
				"main":         e.Name(),
				"dependencies": map[string]string{"@opencode-ai/plugin": "*"},
			}
			data, err := json.MarshalIndent(manifest, "", "  ")
			if err != nil {
				return err
			}
			data = append(data, '\n')
			if err := os.WriteFile(pkgPath, data, 0o644); err != nil {
				return err
			}
			break
		}
	}

	// Register every embedded plugin in .opencode/opencode.json's "plugin"
	// array. opencode only loads plugins that are listed there (file presence
	// in .opencode/plugins/ is not enough), so without this the agent-bus
	// polling/injection never starts. The write is idempotent: an already
	// registered spec is left untouched, and any pre-existing config keys are
	// preserved.
	cfgPath := filepath.Join(b.Root, ".opencode", "opencode.json")
	specs := []string{}
	registered := map[string]bool{}
	if data, err := os.ReadFile(cfgPath); err == nil {
		var doc struct {
			Plugin []string `json:"plugin"`
		}
		if err := json.Unmarshal(data, &doc); err == nil {
			specs = doc.Plugin
			for _, s := range specs {
				registered[s] = true
			}
		}
	}
	changed := false
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".ts") {
			continue
		}
		spec := pluginConfigSpec(e.Name())
		if registered[spec] {
			continue
		}
		specs = append(specs, spec)
		registered[spec] = true
		changed = true
	}
	if changed {
		doc := map[string]any{}
		if data, err := os.ReadFile(cfgPath); err == nil {
			// Best-effort: preserve other keys (e.g. future additions).
			_ = json.Unmarshal(data, &doc)
		}
		doc["plugin"] = specs
		out, err := json.MarshalIndent(doc, "", "  ")
		if err != nil {
			return err
		}
		out = append(out, '\n')
		if err := os.WriteFile(cfgPath, out, 0o644); err != nil {
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

// verifyClaudeScripts checks that every embedded claude launcher script is
// installed to .starfleet-ai/bin/ and byte-identical.
func verifyClaudeScripts(b *Bootstrap) (bool, string) {
	entries, err := fs.ReadDir(starfleetctl.Fragments, filepath.Join(starfleetctl.FragmentsRoot, claudeScriptsSubdir))
	if err != nil {
		return true, "no embedded claude scripts"
	}
	var missing, stale []string
	destDir := filepath.Join(b.Root, ".starfleet-ai", "bin")
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		current, err := fs.ReadFile(starfleetctl.Fragments, filepath.Join(starfleetctl.FragmentsRoot, claudeScriptsSubdir, e.Name()))
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

func fixClaudeScripts(b *Bootstrap) error {
	entries, err := fs.ReadDir(starfleetctl.Fragments, filepath.Join(starfleetctl.FragmentsRoot, claudeScriptsSubdir))
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
		data, err := fs.ReadFile(starfleetctl.Fragments, filepath.Join(starfleetctl.FragmentsRoot, claudeScriptsSubdir, e.Name()))
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

const starfleetAIGitignoreContent = `# Ephemeral runtime directories (not persisted to git)
/agent-bus/
/logs/
/var/
/term-pipes/

# Runtime state files
/session-registry.txt
/DASHBOARD.md

# Built binary (built from src/starfleetctl/)
/bin/starfleetctl

# Starfleetctl source is a separate repo - not tracked here
# (clone starfleetctl repo separately to build)
/src/starfleetctl/
`

func verifyStarfleetAIGitignore(b *Bootstrap) (bool, string) {
	path := filepath.Join(b.Root, ".starfleet-ai", ".gitignore")
	data, err := os.ReadFile(path)
	if err != nil {
		return false, "missing .starfleet-ai/.gitignore"
	}
	if string(data) == starfleetAIGitignoreContent {
		return true, "present, up to date"
	}
	return false, "content differs from template"
}

func fixStarfleetAIGitignore(b *Bootstrap) error {
	path := filepath.Join(b.Root, ".starfleet-ai", ".gitignore")
	return os.WriteFile(path, []byte(starfleetAIGitignoreContent), 0o644)
}

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

const webYamlTemplate = `# starfleetctl web server configuration
# This file is managed by starfleetctl bootstrap --fix
# Only created once on first bootstrap --fix; user edits are preserved.

# Web server listen address (host:port)
# Default: 0.0.0.0:8080
# Alternative: 0.0.0.0:8090
listen_addr: "0.0.0.0:8080"

# Web server autostart (cron/systemd integration)
# When enabled, starfleetctl web autostart will ensure the server is running
# Default: false
autostart_enabled: false

# PID file location for daemon management
# Default: .starfleet-ai/var/web.pid
pid_file: ".starfleet-ai/var/web.pid"

# Log file for web server daemon
# Default: .starfleet-ai/logs/web.log
log_file: ".starfleet-ai/logs/web.log"
`

func verifyWebConf(b *Bootstrap) (bool, string) {
	path := filepath.Join(b.Root, ".starfleet-ai", "conf", "web.yaml")
	_, err := os.Stat(path)
	if err != nil {
		if os.IsNotExist(err) {
			return false, "missing .starfleet-ai/conf/web.yaml"
		}
		return false, fmt.Sprintf("stat error: %v", err)
	}
	return true, "present"
}

func fixWebConf(b *Bootstrap) error {
	path := filepath.Join(b.Root, ".starfleet-ai", "conf", "web.yaml")
	// Only create if missing - don't overwrite persistent user config
	if _, err := os.Stat(path); err == nil {
		return nil // already exists, leave user config alone
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(webYamlTemplate), 0o644)
}
