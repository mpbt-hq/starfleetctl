// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
package wormhole

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"github.com/metux/starfleetctl/internal/comms"
	"github.com/metux/starfleetctl/internal/config"
	"gopkg.in/yaml.v3"
)

// ---- helpers -------------------------------------------------------------

func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	if dir != "" {
		cmd.Dir = dir
	}
	cmd.Env = append(os.Environ(),
		"GIT_AUTHOR_NAME=test", "GIT_AUTHOR_EMAIL=test@example.com",
		"GIT_COMMITTER_NAME=test", "GIT_COMMITTER_EMAIL=test@example.com",
		"GIT_TERMINAL_PROMPT=0",
	)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("git %s: %v\n%s", strings.Join(args, " "), err, out)
	}
	return string(out)
}

// setupRemote creates a bare repository with an initial commit on main and
// returns its path. All tests stay entirely local (no network).
func setupRemote(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	remote := filepath.Join(base, "remote.git")
	runGit(t, "", "init", "--bare", "-b", "main", remote)

	seed := filepath.Join(base, "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "add", "README.md")
	runGit(t, seed, "commit", "-q", "-m", "seed")
	runGit(t, seed, "remote", "add", "origin", remote)
	runGit(t, seed, "push", "-q", "origin", "main")
	return remote
}

func testConfig(remote string) *config.WormholesConfig {
	return &config.WormholesConfig{
		Enabled: true,
		Sync: config.SyncConfig{
			Interval:       "1s",
			StartupSync:    true,
			CloneTimeout:   "2m",
			CommandTimeout: "30s",
			PushRetries:    3,
		},
		Repositories: []config.RepositoryConfig{{
			ID:     "relay",
			Remote: remote,
			Branch: "main",
			Incoming: config.IncomingConfig{
				InitialScan: "replay",
			},
		}},
	}
}

func writeConfig(t *testing.T, root string, cfg *config.WormholesConfig) {
	t.Helper()
	dir := filepath.Join(root, ".starfleet-ai", "conf")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	data, err := yaml.Marshal(map[string]*config.WormholesConfig{"wormholes": cfg})
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ConfigFile(root), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

func writeRawConfig(t *testing.T, root, content string) {
	t.Helper()
	dir := filepath.Join(root, ".starfleet-ai", "conf")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(ConfigFile(root), []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func remoteHas(t *testing.T, remote, path string) bool {
	t.Helper()
	cmd := exec.Command("git", "--git-dir", remote, "cat-file", "-e", "main:"+path)
	return cmd.Run() == nil
}

func remoteRead(t *testing.T, remote, path string) string {
	t.Helper()
	cmd := exec.Command("git", "--git-dir", remote, "show", "main:"+path)
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git show main:%s: %v", path, err)
	}
	return string(out)
}

func dirList(dir string) []string {
	var out []string
	entries, err := os.ReadDir(dir)
	if err != nil {
		return []string{fmt.Sprintf("<err %v>", err)}
	}
	for _, e := range entries {
		out = append(out, e.Name())
	}
	return out
}

func countFiles(t *testing.T, dir string) int {
	t.Helper()
	n := 0
	err := filepath.WalkDir(dir, func(path string, d os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if !d.IsDir() && strings.HasSuffix(path, ".json") {
			n++
		}
		return nil
	})
	if err != nil && !os.IsNotExist(err) {
		t.Fatal(err)
	}
	return n
}

// testTimeouts parses the clone and command timeouts from a wormhole config.
func testTimeouts(cfg *config.WormholesConfig) (time.Duration, time.Duration) {
	cloneTimeout, _ := time.ParseDuration(cfg.Sync.CloneTimeout)
	commandTimeout, _ := time.ParseDuration(cfg.Sync.CommandTimeout)
	return cloneTimeout, commandTimeout
}

// ---- message envelope ----------------------------------------------------

func TestNewMessageUniqueIDsAndRoundtrip(t *testing.T) {
	seen := make(map[string]bool)
	for i := 0; i < 100; i++ {
		msg := NewMessage("alpha", "beta", "message", "hello", "")
		if !validToken(msg.ID) {
			t.Fatalf("generated unsafe id %q", msg.ID)
		}
		if seen[msg.ID] {
			t.Fatalf("duplicate id %q", msg.ID)
		}
		seen[msg.ID] = true

		data := buildMessageBytes(msg)
		parsed, err := parseMessage(data)
		if err != nil {
			t.Fatalf("parse roundtrip: %v", err)
		}
		if parsed.ID != msg.ID || parsed.From != msg.From || parsed.To != msg.To {
			t.Fatalf("roundtrip mismatch: %+v vs %+v", parsed, msg)
		}
		if parsed.Content != "hello" {
			t.Fatalf("content mismatch: %q", parsed.Content)
		}
	}
}

func TestParseRejectsUnknownFieldsAndTraversal(t *testing.T) {
	bad := []string{
		"no front matter\n",
		"---\nid: a\nfrom: b\nto: c\nunknown: x\n---\nbody\n",
		"---\nid: ../escape\nfrom: b\nto: c\n---\nbody\n",
		"---\nid: a\nfrom: b\n---\nbody\n",
	}
	for _, data := range bad {
		if _, err := parseMessage([]byte(data)); err == nil {
			t.Fatalf("expected parse error for %q", data)
		}
	}
}

// ---- configuration -------------------------------------------------------

func TestLoadConfigMissingFileReturnsDefaults(t *testing.T) {
	cfg, err := LoadConfig(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Enabled {
		t.Fatal("default config must be disabled")
	}
}

func TestLoadConfigValid(t *testing.T) {
	root := t.TempDir()
	writeConfig(t, root, testConfig("/remote/repo"))
	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	if !cfg.Enabled || len(cfg.Repositories) != 1 || cfg.Repositories[0].ID != "relay" {
		t.Fatalf("unexpected config: %+v", cfg)
	}
}

func TestLoadConfigRejectsLegacyKeys(t *testing.T) {
	cases := []string{
		"wormholes:\n  enabled: false\n  outgoing: {}\n",
		"wormholes:\n  enabled: false\n  incoming: {}\n",
		"wormholes:\n  enabled: false\n  paths: {}\n",
		"wormholes:\n  enabled: false\n  repositories:\n    - id: r\n      remote: x\n      branch: main\n      target_aliases: {}\n",
		"wormholes:\n  enabled: false\n  repositories:\n    - id: r\n      remote: x\n      branch: main\n      incoming:\n        messages: {}\n",
		"wormholes:\n  enabled: false\n  repositories:\n    - id: r\n      remote: x\n      branch: main\n      incoming:\n        decisions: {}\n",
		"wormholes:\n  enabled: false\n  repositories:\n    - id: r\n      remote: x\n      branch: main\n      incoming:\n        paths: {}\n",
	}
	for _, content := range cases {
		root := t.TempDir()
		writeRawConfig(t, root, content)
		if _, err := LoadConfig(root); err == nil {
			t.Fatalf("expected unknown-key error for config:\n%s", content)
		} else if !strings.Contains(err.Error(), "unknown key") {
			t.Fatalf("expected unknown-key error, got %v", err)
		}
	}
}

func TestLoadConfigRejectsUnknownNestedMappingPath(t *testing.T) {
	root := t.TempDir()
	writeRawConfig(t, root, "wormholes:\n  enabled: false\n  repositories:\n    - id: r\n      remote: x\n      branch: main\n      incoming:\n        enabled: false\n        bogus:\n          x: 1\n")
	if _, err := LoadConfig(root); err == nil {
		t.Fatal("expected unknown nested key error")
	}
}

func TestLoadConfigRejectsInterestingValues(t *testing.T) {
	root := t.TempDir()
	writeRawConfig(t, root, "wormholes:\n  enabled: false\n  repositories:\n    - id: \"../x\"\n      remote: x\n      branch: main\n")
	if _, err := LoadConfig(root); err == nil {
		t.Fatal("expected invalid repository id error")
	}
}

func TestPathHelpers(t *testing.T) {
	root := t.TempDir()
	if got, want := WorkDir(root), filepath.Join(root, ".starfleet-ai", "var", "wormholes"); got != want {
		t.Errorf("WorkDir() = %q, want %q", got, want)
	}
	if got, want := PIDFile(root), filepath.Join(WorkDir(root), "pid"); got != want {
		t.Errorf("PIDFile() = %q, want %q", got, want)
	}
	if got, want := StateFile(root), filepath.Join(WorkDir(root), "state"); got != want {
		t.Errorf("StateFile() = %q, want %q", got, want)
	}
	if got, want := ConfigFile(root), filepath.Join(root, ".starfleet-ai", "conf", "wormholes.yaml"); got != want {
		t.Errorf("ConfigFile() = %q, want %q", got, want)
	}
}

func TestEnsureDir(t *testing.T) {
	root := t.TempDir()
	if err := EnsureDir(root); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	fi, err := os.Stat(WorkDir(root))
	if err != nil || !fi.IsDir() {
		t.Fatalf("wormholes dir not created: %v", err)
	}
	if err := EnsureDir(root); err != nil {
		t.Fatalf("EnsureDir not idempotent: %v", err)
	}
}

// ---- lifecycle -----------------------------------------------------------

func TestPublishProcessLifecycle(t *testing.T) {
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	writeConfig(t, root, cfg)
	t.Setenv("STARFLEET_SHIP_ID", "alpha")

	bus, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}
	repo := cfg.Repositories[0]

	cloneTimeout, commandTimeout := testTimeouts(cfg)
	sender := newSpool(root, repo, cfg.Sync.PushRetries, "beta", cloneTimeout, commandTimeout)
	msg := NewMessage("beta", "alpha", "message", "hello\nworld", "")
	msg.Subject = "greeting"
	if err := sender.Publish(msg); err != nil {
		t.Fatalf("publish: %v", err)
	}

	clone := sender.git.CloneDir()
	if filepath.Dir(filepath.Dir(filepath.Dir(filepath.Join(clone, "mail", "alpha", "new", msg.ID+".md")))) != filepath.Join(clone, "mail") {
		t.Fatal("maildir is not repository-relative")
	}
	newPath := filepath.Join(clone, "mail", "alpha", "new", msg.ID+".md")
	data, err := os.ReadFile(newPath)
	if err != nil {
		t.Fatalf("published message missing: %v", err)
	}
	if !strings.HasPrefix(string(data), "---\n") {
		t.Fatal("published message lacks front matter")
	}
	staged := "mail/alpha/new/" + msg.ID + ".md"
	if !remoteHas(t, remote, staged) {
		t.Fatal("published message was not pushed")
	}
	if got := remoteRead(t, remote, staged); got != string(data) {
		t.Fatal("pushed bytes differ from published bytes")
	}

	// Re-publishing identical bytes is idempotent.
	if err := sender.Publish(msg); err != nil {
		t.Fatalf("idempotent publish: %v", err)
	}
	// Same id, different bytes must fail closed.
	other := *msg
	other.Content = "different"
	if err := sender.Publish(&other); err == nil || !strings.Contains(err.Error(), "collision") {
		t.Fatalf("expected collision error, got %v", err)
	}

	if err := processPendingMessages(root); err != nil {
		t.Fatalf("process: %v", err)
	}

	if _, err := os.Stat(newPath); !os.IsNotExist(err) {
		t.Fatal("message still in new/")
	}
	if _, err := os.Stat(filepath.Join(clone, "mail", "alpha", "cur", msg.ID+".md")); !os.IsNotExist(err) {
		t.Fatal("message still in cur/")
	}
	processed := filepath.Join(clone, "mail", "alpha", "processed", msg.ID+".md")
	if _, err := os.Stat(processed); err != nil {
		t.Fatalf("message not processed: %v", err)
	}
	if !remoteHas(t, remote, "mail/alpha/processed/"+msg.ID+".md") {
		t.Fatal("processed transition was not pushed")
	}
	if remoteHas(t, remote, staged) {
		t.Fatal("new/ entry still present on remote after processing")
	}

	// Idempotent second cycle: no duplicate Comms message.
	if err := processPendingMessages(root); err != nil {
		t.Fatalf("second process: %v", err)
	}
	if n := countFiles(t, bus.MsgDir); n != 1 {
		t.Fatalf("expected exactly 1 comms message, got %d", n)
	}
}

func TestRecipientMismatchMovesToFailed(t *testing.T) {
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	writeConfig(t, root, cfg)
	t.Setenv("STARFLEET_SHIP_ID", "alpha")

	bus, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}
	cloneTimeout, commandTimeout := testTimeouts(cfg)
	sp := newSpool(root, cfg.Repositories[0], cfg.Sync.PushRetries, "alpha", cloneTimeout, commandTimeout)
	if err := sp.runOnce(nil, false); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	msg := NewMessage("beta", "beta", "message", "wrong mailbox", "")
	dir := filepath.Join(sp.git.CloneDir(), "mail", "alpha", "new")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, msg.ID+".md"), buildMessageBytes(msg), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := processPendingMessages(root); err != nil {
		t.Fatalf("process: %v", err)
	}

	failedDir := filepath.Join(sp.git.CloneDir(), "mail", "alpha", "failed")
	if _, err := os.Stat(filepath.Join(failedDir, msg.ID+".md")); err != nil {
		t.Fatalf("message not moved to failed/: %v", err)
	}
	if _, err := os.Stat(filepath.Join(failedDir, msg.ID+".md.reason")); err != nil {
		t.Fatalf("failure diagnostic missing: %v", err)
	}
	if !remoteHas(t, remote, "mail/alpha/failed/"+msg.ID+".md.reason") {
		t.Fatal("failure diagnostic was not pushed")
	}
	if n := countFiles(t, bus.MsgDir); n != 0 {
		t.Fatalf("recipient mismatch must not import, got %d comms messages", n)
	}
}

func TestMalformedMessageMovesToFailed(t *testing.T) {
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	writeConfig(t, root, cfg)
	t.Setenv("STARFLEET_SHIP_ID", "alpha")

	_, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}
	cloneTimeout, commandTimeout := testTimeouts(cfg)
	sp := newSpool(root, cfg.Repositories[0], cfg.Sync.PushRetries, "alpha", cloneTimeout, commandTimeout)
	if err := sp.runOnce(nil, false); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	dir := filepath.Join(sp.git.CloneDir(), "mail", "alpha", "new")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "broken.md"), []byte("not a message\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := processPendingMessages(root); err != nil {
		t.Fatalf("process: %v", err)
	}
	failedDir := filepath.Join(sp.git.CloneDir(), "mail", "alpha", "failed")
	if _, err := os.Stat(filepath.Join(failedDir, "broken.md")); err != nil {
		t.Fatalf("malformed message not moved to failed/: %v", err)
	}
	if _, err := os.Stat(filepath.Join(failedDir, "broken.md.reason")); err != nil {
		t.Fatalf("diagnostic missing: %v", err)
	}
	if !remoteHas(t, remote, "mail/alpha/failed/broken.md.reason") {
		t.Fatal("diagnostic was not pushed")
	}
}

func TestImportFailureKeepsMessageInCur(t *testing.T) {
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	writeConfig(t, root, cfg)

	cloneTimeout, commandTimeout := testTimeouts(cfg)
	sp := newSpool(root, cfg.Repositories[0], cfg.Sync.PushRetries, "alpha", cloneTimeout, commandTimeout)
	if err := sp.runOnce(nil, false); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	msg := NewMessage("beta", "alpha", "message", "hello", "")
	dir := filepath.Join(sp.git.CloneDir(), "mail", "alpha", "new")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, msg.ID+".md"), buildMessageBytes(msg), 0o644); err != nil {
		t.Fatal(err)
	}

	// A bus whose lock path cannot be created forces a transient import error.
	badBusDir := filepath.Join(root, "badbus")
	if err := os.WriteFile(badBusDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	badBus := &comms.Bus{BusDir: badBusDir}

	if err := sp.runOnce(badBus, true); err == nil {
		t.Fatal("expected transient import error")
	}

	cur := filepath.Join(sp.git.CloneDir(), "mail", "alpha", "cur", msg.ID+".md")
	if _, err := os.Stat(cur); err != nil {
		t.Fatalf("message must remain recoverable in cur/: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sp.git.CloneDir(), "mail", "alpha", "processed", msg.ID+".md")); !os.IsNotExist(err) {
		t.Fatal("message must not be marked processed after failed import")
	}
	if !remoteHas(t, remote, "mail/alpha/cur/"+msg.ID+".md") {
		t.Fatal("claim transition was not pushed")
	}
}

func TestPublishRejectsReadOnly(t *testing.T) {
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	cfg.Repositories[0].Access = "read-only"
	writeConfig(t, root, cfg)

	cloneTimeout, commandTimeout := testTimeouts(cfg)
	sp := newSpool(root, cfg.Repositories[0], cfg.Sync.PushRetries, "beta", cloneTimeout, commandTimeout)
	msg := NewMessage("beta", "alpha", "message", "hello", "")
	if err := sp.Publish(msg); err == nil {
		t.Fatal("publish into a read-only repository must fail")
	}
}

func TestRepositoryLockIsShared(t *testing.T) {
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	cloneTimeout, commandTimeout := testTimeouts(cfg)
	sp := newSpool(root, cfg.Repositories[0], 3, "alpha", cloneTimeout, commandTimeout)

	held, err := sp.git.Lock(2 * time.Second)
	if err != nil {
		t.Fatalf("first lock: %v", err)
	}
	other := newSpool(root, cfg.Repositories[0], 3, "beta", cloneTimeout, commandTimeout)
	if _, err := other.git.Lock(150 * time.Millisecond); err == nil {
		t.Fatal("second writer acquired the same repository lock")
	}
	if err := held.Close(); err != nil {
		t.Fatal(err)
	}
	l, err := other.git.Lock(2 * time.Second)
	if err != nil {
		t.Fatalf("lock after release: %v", err)
	}
	_ = l.Close()
}

// externalCommit advances the remote branch from an independent clone, so a
// concurrent writer's next push is rejected as non-fast-forward.
func externalCommit(t *testing.T, remote, file string) {
	t.Helper()
	dir := t.TempDir()
	runGit(t, "", "clone", "-q", remote, dir)
	if err := os.WriteFile(filepath.Join(dir, file), []byte("external\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, dir, "add", file)
	runGit(t, dir, "commit", "-q", "-m", "external update")
	runGit(t, dir, "push", "-q", "origin", "main")
}

func TestPushRecoversFromConcurrentRemoteUpdate(t *testing.T) {
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	cloneTimeout, commandTimeout := testTimeouts(cfg)

	sp := newSpool(root, cfg.Repositories[0], cfg.Sync.PushRetries, "alpha", cloneTimeout, commandTimeout)
	if err := sp.runOnce(nil, false); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	msg := NewMessage("beta", "alpha", "message", "concurrent", "")
	rel := "mail/alpha/new/" + msg.ID + ".md"
	abs := filepath.Join(sp.git.CloneDir(), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, buildMessageBytes(msg), 0o644); err != nil {
		t.Fatal(err)
	}

	externalCommit(t, remote, "external.txt")

	if err := sp.git.CommitAndPushLocked([]string{rel}, "wormhole: publish "+msg.ID); err != nil {
		t.Fatalf("push recovery: %v", err)
	}
	if !remoteHas(t, remote, rel) {
		t.Fatal("message was not pushed after rebase recovery")
	}
	if !remoteHas(t, remote, "external.txt") {
		t.Fatal("external update was lost by the recovery rebase")
	}
}

// TestSyncUsesConfiguredPushRetries proves that the sync cycle honors the
// configured sync.push_retries value instead of a hardcoded default: with
// push_retries=0 a rejected push is attempted exactly once and the error
// reports the configured count (the old hardcoded default of 3 would retry
// and report 3).
func TestSyncUsesConfiguredPushRetries(t *testing.T) {
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	cfg.Sync.PushRetries = 0
	writeConfig(t, root, cfg)
	cloneTimeout, commandTimeout := testTimeouts(cfg)

	// Publish a message for alpha while pushes still succeed.
	sp := newSpool(root, cfg.Repositories[0], cfg.Sync.PushRetries, "beta", cloneTimeout, commandTimeout)
	msg := NewMessage("beta", "alpha", "message", "configured", "")
	if err := sp.Publish(msg); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// A rejecting pre-receive hook: every push attempt appends one line to
	// the counter file and fails with a non-fast-forward-looking error.
	counter := filepath.Join(t.TempDir(), "hook-calls")
	hook := "#!/bin/sh\necho x >> " + counter + "\necho hook rejects >&2\nexit 1\n"
	if err := os.MkdirAll(filepath.Join(remote, "hooks"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(remote, "hooks", "pre-receive"), []byte(hook), 0o755); err != nil {
		t.Fatal(err)
	}

	t.Setenv("STARFLEET_BUS_DIR", filepath.Join(root, "bus"))
	t.Setenv("STARFLEET_SHIP_ID", "alpha")
	err := processPendingMessages(root)
	if err == nil {
		t.Fatal("processing must fail while the remote rejects pushes")
	}
	if !strings.Contains(err.Error(), "push failed after 0 retries") {
		t.Fatalf("error should report the configured retry count, got: %v", err)
	}
	data, rerr := os.ReadFile(counter)
	if rerr != nil {
		t.Fatalf("hook counter: %v", rerr)
	}
	if calls := strings.Count(string(data), "x"); calls != 1 {
		t.Fatalf("expected exactly 1 push attempt with push_retries=0, got %d", calls)
	}
}

func TestCrashRecoveryCommitsDirtyTransition(t *testing.T) {
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	cloneTimeout, commandTimeout := testTimeouts(cfg)

	sp := newSpool(root, cfg.Repositories[0], cfg.Sync.PushRetries, "beta", cloneTimeout, commandTimeout)
	msg := NewMessage("beta", "alpha", "message", "crash", "")
	if err := sp.Publish(msg); err != nil {
		t.Fatalf("publish: %v", err)
	}
	newAbs := filepath.Join(sp.git.CloneDir(), "mail", "alpha", "new", msg.ID+".md")
	curAbs := filepath.Join(sp.git.CloneDir(), "mail", "alpha", "cur", msg.ID+".md")
	if err := os.MkdirAll(filepath.Dir(curAbs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(newAbs, curAbs); err != nil {
		t.Fatal(err)
	}

	// Simulate a crash between the local rename and the claim commit.
	if err := sp.runOnce(nil, false); err != nil {
		t.Fatalf("recovery sync: %v", err)
	}
	if !remoteHas(t, remote, "mail/alpha/cur/"+msg.ID+".md") {
		t.Fatal("interrupted claim was not recovered and pushed")
	}
	if remoteHas(t, remote, "mail/alpha/new/"+msg.ID+".md") {
		t.Fatal("stale new/ entry survived recovery")
	}
}

// ---- initial scan: baseline semantics + crash recovery --------------------

// TestInitialScanBaselineExcludesExisting verifies baseline semantics: all
// messages already in new/ at the first processing cycle are moved to
// processed/ WITHOUT being imported, and only future messages are imported.
func TestInitialScanBaselineExcludesExisting(t *testing.T) {
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	cfg.Repositories[0].Incoming.InitialScan = "baseline"
	writeConfig(t, root, cfg)
	t.Setenv("STARFLEET_SHIP_ID", "alpha")

	bus, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}
	cloneTimeout, commandTimeout := testTimeouts(cfg)
	sender := newSpool(root, cfg.Repositories[0], cfg.Sync.PushRetries, "beta", cloneTimeout, commandTimeout)
	old1 := NewMessage("beta", "alpha", "ship", "pre-existing 1", "")
	old2 := NewMessage("beta", "alpha", "ship", "pre-existing 2", "")
	if err := sender.Publish(old1); err != nil {
		t.Fatalf("publish old1: %v", err)
	}
	if err := sender.Publish(old2); err != nil {
		t.Fatalf("publish old2: %v", err)
	}

	// First processing cycle performs the baseline scan.
	if err := processPendingMessages(root); err != nil {
		t.Fatalf("process: %v", err)
	}

	// Neither pre-existing message was imported.
	if n := countFiles(t, bus.MsgDir); n != 0 {
		t.Fatalf("baseline must not import pre-existing messages, got %d comms messages", n)
	}
	// Both were moved to processed/ and the transition was pushed.
	if !remoteHas(t, remote, "mail/alpha/processed/"+old1.ID+".md") {
		t.Fatal("baseline move of old1 was not pushed")
	}
	if !remoteHas(t, remote, "mail/alpha/processed/"+old2.ID+".md") {
		t.Fatal("baseline move of old2 was not pushed")
	}
	if remoteHas(t, remote, "mail/alpha/new/"+old1.ID+".md") || remoteHas(t, remote, "mail/alpha/new/"+old2.ID+".md") {
		t.Fatal("baseline left messages in new/ on the remote")
	}

	// A message arriving AFTER the baseline is imported normally.
	fresh := NewMessage("beta", "alpha", "ship", "fresh after baseline", "")
	if err := sender.Publish(fresh); err != nil {
		t.Fatalf("publish fresh: %v", err)
	}
	if err := processPendingMessages(root); err != nil {
		t.Fatalf("second process: %v", err)
	}
	if n := countFiles(t, bus.MsgDir); n != 1 {
		t.Fatalf("post-baseline message must be imported exactly once, got %d comms messages", n)
	}
}

// TestInitialScanCrashDuringBaselineMove simulates a crash in the middle of
// the baseline move: one message renamed new/→processed/ locally, nothing
// committed, no initial-scan marker. A restart must converge to the same
// result as an uninterrupted baseline: no import, no loss, no duplication.
func TestInitialScanCrashDuringBaselineMove(t *testing.T) {
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	cfg.Repositories[0].Incoming.InitialScan = "baseline"
	writeConfig(t, root, cfg)
	t.Setenv("STARFLEET_SHIP_ID", "alpha")

	bus, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}
	cloneTimeout, commandTimeout := testTimeouts(cfg)
	sender := newSpool(root, cfg.Repositories[0], cfg.Sync.PushRetries, "beta", cloneTimeout, commandTimeout)
	m1 := NewMessage("beta", "alpha", "ship", "crash candidate 1", "")
	m2 := NewMessage("beta", "alpha", "ship", "crash candidate 2", "")
	if err := sender.Publish(m1); err != nil {
		t.Fatalf("publish m1: %v", err)
	}
	if err := sender.Publish(m2); err != nil {
		t.Fatalf("publish m2: %v", err)
	}

	// Receiver pulls without processing: the pre-first-cycle state.
	sp := newSpool(root, cfg.Repositories[0], cfg.Sync.PushRetries, "alpha", cloneTimeout, commandTimeout)
	if err := sp.runOnce(nil, false); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	if sp.hasInitialScanCompleted("alpha") {
		t.Fatal("no initial scan may have run yet")
	}

	// Crash mid baseline move: m1 renamed, m2 not, nothing committed, no marker.
	newDir := filepath.Join(sp.git.CloneDir(), "mail", "alpha", "new")
	procDir := filepath.Join(sp.git.CloneDir(), "mail", "alpha", "processed")
	if err := os.MkdirAll(procDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(newDir, m1.ID+".md"), filepath.Join(procDir, m1.ID+".md")); err != nil {
		t.Fatal(err)
	}

	// Restart: one full processing cycle must converge.
	if err := processPendingMessages(root); err != nil {
		t.Fatalf("recovery process: %v", err)
	}

	// Baseline semantics held: neither message was imported.
	if n := countFiles(t, bus.MsgDir); n != 0 {
		t.Fatalf("interrupted baseline imported %d messages", n)
	}
	// Both messages ended up in processed/, nothing stranded elsewhere.
	for _, m := range []*Message{m1, m2} {
		if _, err := os.Stat(filepath.Join(procDir, m.ID+".md")); err != nil {
			t.Fatalf("message %s not in processed/ after recovery: %v", m.ID, err)
		}
		if _, err := os.Stat(filepath.Join(newDir, m.ID+".md")); !os.IsNotExist(err) {
			t.Fatalf("message %s still in new/ after recovery", m.ID)
		}
		curDir := filepath.Join(sp.git.CloneDir(), "mail", "alpha", "cur")
		if _, err := os.Stat(filepath.Join(curDir, m.ID+".md")); !os.IsNotExist(err) {
			t.Fatalf("message %s stuck in cur/ after recovery", m.ID)
		}
	}
	// The crash window left a clean working tree (the dirty transition was
	// committed locally on restart).
	out, err := exec.Command("git", "-C", sp.git.CloneDir(), "status", "--porcelain").CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if len(strings.TrimSpace(string(out))) != 0 {
		t.Fatalf("dirty working tree after recovery:\n%s", out)
	}
	// The marker now exists, so a further cycle does not re-run the scan.
	if !sp.hasInitialScanCompleted("alpha") {
		t.Fatal("initial scan marker missing after recovery")
	}
}

// TestInitialScanCrashAfterMoveBeforeMarker simulates a crash after the whole
// baseline move but before the state marker: all messages already renamed
// new/→processed/ locally, nothing committed. A restart must not import the
// moved messages (they are baseline, not to be replayed) and must keep
// importing future messages normally.
func TestInitialScanCrashAfterMoveBeforeMarker(t *testing.T) {
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	cfg.Repositories[0].Incoming.InitialScan = "baseline"
	writeConfig(t, root, cfg)
	t.Setenv("STARFLEET_SHIP_ID", "alpha")

	bus, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}
	cloneTimeout, commandTimeout := testTimeouts(cfg)
	sender := newSpool(root, cfg.Repositories[0], cfg.Sync.PushRetries, "beta", cloneTimeout, commandTimeout)
	m1 := NewMessage("beta", "alpha", "ship", "moved before crash", "")
	if err := sender.Publish(m1); err != nil {
		t.Fatalf("publish m1: %v", err)
	}

	sp := newSpool(root, cfg.Repositories[0], cfg.Sync.PushRetries, "alpha", cloneTimeout, commandTimeout)
	if err := sp.runOnce(nil, false); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	// Crash AFTER the whole move, BEFORE commit and marker.
	newDir := filepath.Join(sp.git.CloneDir(), "mail", "alpha", "new")
	procDir := filepath.Join(sp.git.CloneDir(), "mail", "alpha", "processed")
	if err := os.MkdirAll(procDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Rename(filepath.Join(newDir, m1.ID+".md"), filepath.Join(procDir, m1.ID+".md")); err != nil {
		t.Fatal(err)
	}

	// Restart: converges without importing the moved message.
	if err := processPendingMessages(root); err != nil {
		t.Fatalf("recovery process: %v", err)
	}
	if n := countFiles(t, bus.MsgDir); n != 0 {
		t.Fatalf("crashed baseline move was imported: %d comms messages", n)
	}
	if _, err := os.Stat(filepath.Join(procDir, m1.ID+".md")); err != nil {
		t.Fatalf("message not in processed/ after recovery: %v", err)
	}
	if !sp.hasInitialScanCompleted("alpha") {
		t.Fatal("initial scan marker missing after recovery")
	}

	// No loss either: future messages are still imported.
	m2 := NewMessage("beta", "alpha", "ship", "after recovery", "")
	if err := sender.Publish(m2); err != nil {
		t.Fatalf("publish m2: %v", err)
	}
	if err := processPendingMessages(root); err != nil {
		t.Fatalf("post-recovery process: %v", err)
	}
	if n := countFiles(t, bus.MsgDir); n != 1 {
		t.Fatalf("post-recovery message must be imported exactly once, got %d", n)
	}
}

// ---- read-only multi-cycle import (remote workspace pair) -----------------

// TestReadOnlyMultiCycleImport drives a read-only receiving workspace through
// multiple cycles: the first message is imported exactly once, the remote is
// never advanced by the read-only client, a repeated cycle imports nothing,
// and a later message from the read-write side is imported. Lifecycle
// transitions are committed locally so the working tree stays clean.
func TestReadOnlyMultiCycleImport(t *testing.T) {
	remote := setupRemote(t)
	rootRO := t.TempDir()
	cfgRO := testConfig(remote)
	cfgRO.Repositories[0].Access = "read-only"
	writeConfig(t, rootRO, cfgRO)

	rootRW := t.TempDir()
	cfgRW := testConfig(remote)
	writeConfig(t, rootRW, cfgRW)

	cloneTimeout, commandTimeout := testTimeouts(cfgRW)
	sender := newSpool(rootRW, cfgRW.Repositories[0], cfgRW.Sync.PushRetries, "beta", cloneTimeout, commandTimeout)
	m1 := NewMessage("beta", "alpha", "ship", "first message", "")
	if err := sender.Publish(m1); err != nil {
		t.Fatalf("publish m1: %v", err)
	}

	headBefore := strings.TrimSpace(gitOut(t, "--git-dir", remote, "rev-parse", "main"))

	t.Setenv("STARFLEET_SHIP_ID", "alpha")
	busRO, err := comms.New(rootRO)
	if err != nil {
		t.Fatal(err)
	}

	// Cycle 1: m1 is imported.
	if err := processPendingMessages(rootRO); err != nil {
		t.Fatalf("cycle 1: %v", err)
	}
	if n := countFiles(t, busRO.MsgDir); n != 1 {
		t.Fatalf("cycle 1: expected 1 imported message, got %d", n)
	}

	// The read-only client never advanced the remote.
	headAfter := strings.TrimSpace(gitOut(t, "--git-dir", remote, "rev-parse", "main"))
	if headAfter != headBefore {
		t.Fatalf("read-only client advanced the remote: %s -> %s", headBefore, headAfter)
	}
	// The message is still in new/ for every other reader — only advanced
	// in the read-only client's local, committed mailbox state.
	if !remoteHas(t, remote, "mail/alpha/new/"+m1.ID+".md") {
		t.Fatal("read-only client modified the remote mailbox state")
	}

	// Cycle 2: nothing new arrives, nothing is re-imported.
	if err := processPendingMessages(rootRO); err != nil {
		t.Fatalf("cycle 2: %v", err)
	}
	if n := countFiles(t, busRO.MsgDir); n != 1 {
		t.Fatalf("cycle 2: expected no re-import, got %d comms messages", n)
	}

	// Cycle 3: a new message from the read-write side is imported.
	m2 := NewMessage("beta", "alpha", "ship", "second message", "")
	if err := sender.Publish(m2); err != nil {
		t.Fatalf("publish m2: %v", err)
	}
	if err := processPendingMessages(rootRO); err != nil {
		t.Fatalf("cycle 3: %v", err)
	}
	if n := countFiles(t, busRO.MsgDir); n != 2 {
		t.Fatalf("cycle 3: expected 2 imported messages, got %d", n)
	}

	// The read-only clone's local transitions are committed: clean tree.
	sp := newSpool(rootRO, cfgRO.Repositories[0], cfgRO.Sync.PushRetries, "alpha", cloneTimeout, commandTimeout)
	out, err := exec.Command("git", "-C", sp.git.CloneDir(), "status", "--porcelain").CombinedOutput()
	if err != nil {
		t.Fatalf("git status: %v", err)
	}
	if len(strings.TrimSpace(string(out))) != 0 {
		t.Fatalf("read-only clone left dirty tree:\n%s", out)
	}
	procDir := filepath.Join(sp.git.CloneDir(), "mail", "alpha", "processed")
	for _, m := range []*Message{m1, m2} {
		if _, err := os.Stat(filepath.Join(procDir, m.ID+".md")); err != nil {
			t.Fatalf("message %s not locally processed: %v", m.ID, err)
		}
	}
}

// gitOut runs git and returns its output, failing the test on error.
func gitOut(t *testing.T, args ...string) string {
	t.Helper()
	cmd := exec.Command("git", args...)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.Output()
	if err != nil {
		t.Fatalf("git %s: %v", strings.Join(args, " "), err)
	}
	return string(out)
}

// ---- transport guards -------------------------------------------------------

// TestAttachmentPointerRejected verifies that a comms message carrying an
// inline attachment pointer is rejected by the wormhole transport instead of
// silently dropping the attachment.
func TestAttachmentPointerRejected(t *testing.T) {
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	writeConfig(t, root, cfg)
	confDir := filepath.Join(root, ".starfleet-ai", "conf")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatal(err)
	}
	routing := []byte("comms:\n  routing:\n    local_domain: local\n    domains:\n      - domain: peer.example\n        transport: wormhole\n        repository: relay\n")
	if err := os.WriteFile(filepath.Join(confDir, "comms.yaml"), routing, 0o644); err != nil {
		t.Fatal(err)
	}

	InstallCommsTransport()
	t.Setenv("STARFLEET_BUS_DIR", filepath.Join(root, "bus"))
	t.Setenv("STARFLEET_SHIP_ID", "beta")
	bus, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}

	_, err = bus.Tell("alpha@peer.example", "see [[attach:report.txt:2048:deadbeef]] for details", "corr-att-1")
	if err == nil {
		t.Fatal("tell with attachment pointer must be rejected by the wormhole transport")
	}
	if !strings.Contains(err.Error(), "attachments") {
		t.Fatalf("rejection must name the attachment limitation, got: %v", err)
	}

	// Nothing was published into the repository.
	cmd := exec.Command("git", "--git-dir", remote, "ls-tree", "-r", "--name-only", "main")
	out, oerr := cmd.Output()
	if oerr != nil {
		t.Fatalf("ls-tree: %v", oerr)
	}
	if strings.Contains(string(out), "mail/alpha/new/") {
		t.Fatal("rejected message must not be published into the repository")
	}
}

// TestTypeAndReplyToRoundtrip publishes a message with a non-default type and
// a reply target and verifies both survive the full wormhole pipeline into
// the receiving Comms record.
func TestTypeAndReplyToRoundtrip(t *testing.T) {
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	writeConfig(t, root, cfg)
	t.Setenv("STARFLEET_SHIP_ID", "alpha")

	bus, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}
	cloneTimeout, commandTimeout := testTimeouts(cfg)
	sender := newSpool(root, cfg.Repositories[0], cfg.Sync.PushRetries, "beta", cloneTimeout, commandTimeout)
	msg := NewMessage("beta", "alpha", "control", "directive with type and reply", "m0000111122223333")
	if err := sender.Publish(msg); err != nil {
		t.Fatalf("publish: %v", err)
	}

	if err := processPendingMessages(root); err != nil {
		t.Fatalf("process: %v", err)
	}

	unseenDir := filepath.Join(bus.MsgDir, "alpha", "unseen")
	entries, err := os.ReadDir(unseenDir)
	if err != nil {
		t.Fatalf("read unseen dir: %v", err)
	}
	var found bool
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, rerr := os.ReadFile(filepath.Join(unseenDir, e.Name()))
		if rerr != nil {
			continue
		}
		var rec struct {
			From    string `json:"from"`
			Target  string `json:"target"`
			Text    string `json:"text"`
			Type    string `json:"type"`
			ReplyTo string `json:"reply_to"`
		}
		if jerr := json.Unmarshal(data, &rec); jerr != nil {
			continue
		}
		if rec.From == "beta" && rec.Target == "alpha" && strings.Contains(rec.Text, "directive with type and reply") {
			found = true
			if rec.Type != "control" {
				t.Fatalf("message type lost in transit: got %q, want %q", rec.Type, "control")
			}
			if rec.ReplyTo != "m0000111122223333" {
				t.Fatalf("reply_to lost in transit: got %q", rec.ReplyTo)
			}
		}
	}
	if !found {
		t.Fatal("imported message not found in unseen inbox")
	}
}

func TestConcurrentCyclesImportExactlyOnce(t *testing.T) {
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	writeConfig(t, root, cfg)
	t.Setenv("STARFLEET_SHIP_ID", "alpha")

	bus, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}
	cloneTimeout, commandTimeout := testTimeouts(cfg)
	sender := newSpool(root, cfg.Repositories[0], cfg.Sync.PushRetries, "beta", cloneTimeout, commandTimeout)
	msg := NewMessage("beta", "alpha", "message", "concurrent cycles", "")
	if err := sender.Publish(msg); err != nil {
		t.Fatalf("publish: %v", err)
	}

	const workers = 4
	var wg sync.WaitGroup
	errs := make(chan error, workers)
	for i := 0; i < workers; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			sp := newSpool(root, cfg.Repositories[0], cfg.Sync.PushRetries, "alpha", cloneTimeout, commandTimeout)
			errs <- sp.runOnce(bus, true)
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatalf("concurrent cycle: %v", err)
		}
	}
	if n := countFiles(t, bus.MsgDir); n != 1 {
		t.Fatalf("expected exactly 1 comms message after concurrent cycles, got %d", n)
	}
	if !remoteHas(t, remote, "mail/alpha/processed/"+msg.ID+".md") {
		t.Fatal("message was not processed durably")
	}
}

func TestCommitDoesNotIncludeForeignStagedFiles(t *testing.T) {
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	cloneTimeout, commandTimeout := testTimeouts(cfg)
	sp := newSpool(root, cfg.Repositories[0], cfg.Sync.PushRetries, "alpha", cloneTimeout, commandTimeout)

	lock, err := sp.git.Lock(5 * time.Second)
	if err != nil {
		t.Fatalf("lock: %v", err)
	}
	defer lock.Close()
	if err := sp.git.EnsureCloneLocked(); err != nil {
		t.Fatalf("clone: %v", err)
	}

	// Somebody else staged an unrelated file in the checkout's normal index.
	foreign := filepath.Join(sp.git.CloneDir(), "foreign.txt")
	if err := os.WriteFile(foreign, []byte("not part of the spool\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, sp.git.CloneDir(), "add", "foreign.txt")

	msg := NewMessage("beta", "alpha", "message", "isolated index", "")
	rel := "mail/alpha/new/" + msg.ID + ".md"
	abs := filepath.Join(sp.git.CloneDir(), filepath.FromSlash(rel))
	if err := os.MkdirAll(filepath.Dir(abs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(abs, buildMessageBytes(msg), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := sp.git.CommitAndPushLocked([]string{rel}, "wormhole: publish "+msg.ID); err != nil {
		t.Fatalf("commit and push: %v", err)
	}
	if !remoteHas(t, remote, rel) {
		t.Fatal("spool message was not pushed")
	}
	if remoteHas(t, remote, "foreign.txt") {
		t.Fatal("foreign pre-staged file was committed and pushed")
	}
	staged := runGit(t, sp.git.CloneDir(), "diff", "--cached", "--name-only")
	if !strings.Contains(staged, "foreign.txt") {
		t.Fatalf("foreign staged file lost from index: %q", staged)
	}
}

func TestCommsRoutingPublishesThroughWormhole(t *testing.T) {
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	writeConfig(t, root, cfg)
	confDir := filepath.Join(root, ".starfleet-ai", "conf")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatal(err)
	}
	routing := []byte("comms:\n  routing:\n    local_domain: local\n    domains:\n      - domain: peer.example\n        transport: wormhole\n        repository: relay\n")
	if err := os.WriteFile(filepath.Join(confDir, "comms.yaml"), routing, 0o644); err != nil {
		t.Fatal(err)
	}

	InstallCommsTransport()
	t.Setenv("STARFLEET_BUS_DIR", filepath.Join(root, "bus"))
	t.Setenv("STARFLEET_SHIP_ID", "beta")
	bus, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}

	msgID, err := bus.Tell("alpha@peer.example", "hello over wormhole", "corr-42")
	if err != nil {
		t.Fatalf("remote send through normal comms path: %v", err)
	}
	rel := "mail/alpha/new/" + msgID + ".md"
	if !remoteHas(t, remote, rel) {
		t.Fatalf("routed message was not published at %s", rel)
	}
	data := remoteRead(t, remote, rel)
	if !strings.Contains(data, "hello over wormhole") {
		t.Fatalf("published payload lost text: %q", data)
	}
	if !strings.Contains(data, "reply_to: corr-42") {
		t.Fatalf("published envelope lost reply_to from routing metadata: %q", data)
	}
	if !strings.Contains(data, "type: ship") {
		t.Fatalf("published envelope lost message type from routing metadata: %q", data)
	}
	if _, err := os.Stat(filepath.Join(bus.MsgDir, "alpha", "unseen")); !os.IsNotExist(err) {
		t.Fatalf("remote recipient must not be written into the local inbox (err=%v)", err)
	}
}

func TestTwoInstanceRemoteImportE2E(t *testing.T) {
	remote := setupRemote(t)
	rootA := t.TempDir()
	rootB := t.TempDir()
	cfg := testConfig(remote)
	writeConfig(t, rootA, cfg)
	writeConfig(t, rootB, cfg)

	for _, r := range []string{rootA, rootB} {
		confDir := filepath.Join(r, ".starfleet-ai", "conf")
		if err := os.MkdirAll(confDir, 0o755); err != nil {
			t.Fatal(err)
		}
		routing := []byte("comms:\n  routing:\n    local_domain: local\n    domains:\n      - domain: peer.example\n        transport: wormhole\n        repository: relay\n")
		if err := os.WriteFile(filepath.Join(confDir, "comms.yaml"), routing, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	t.Setenv("STARFLEET_SHIP_ID", "alpha") // publisher: must NOT import its own outbound remote messages
	t.Setenv("STARFLEET_BUS_DIR", filepath.Join(rootA, "bus"))
	busA, err := comms.New(rootA)
	if err != nil {
		t.Fatal(err)
	}
	t.Setenv("STARFLEET_BUS_DIR", filepath.Join(rootB, "bus"))
	t.Setenv("STARFLEET_SHIP_ID", "beta")
	busB, err := comms.New(rootB)
	if err != nil {
		t.Fatal(err)
	}

	InstallCommsTransport()

	msgID, err := busA.Tell("beta@peer.example", "cross-instance payload", "corr-cross-1")
	if err != nil {
		t.Fatalf("remote send: %v", err)
	}
	if err := processPendingMessages(rootA); err != nil {
		t.Fatalf("processPendingMessages root A: %v", err)
	}
	// Wait until the spool on root A has pushed the claimed message to the shared repo.
	// It may live in new/cur/processed after one processPendingMessages cycle.
	deadline := time.Now().Add(20 * time.Second)
	var pushed bool
	for time.Now().Before(deadline) {
		pushed = remoteHas(t, remote, "mail/beta/new/"+msgID+".md") ||
			remoteHas(t, remote, "mail/beta/cur/"+msgID+".md") ||
			remoteHas(t, remote, "mail/beta/processed/"+msgID+".md")
		if pushed {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !pushed {
		t.Fatalf("spool on root A did not push claimed message %s", msgID)
	}

	// Instance B imports from the shared repo into its own Comms bus.
	if err := processPendingMessages(rootB); err != nil {
		t.Fatalf("processPendingMessages root B: %v", err)
	}
	t.Logf("rootB unseen: %v", dirList(filepath.Join(busB.MsgDir, "beta", "unseen")))
	t.Logf("rootB cur: %v", dirList(filepath.Join(busB.MsgDir, "beta", "cur")))
	unseenDir := filepath.Join(busB.MsgDir, "beta", "unseen")
	var found *os.File
	entries, err := os.ReadDir(unseenDir)
	if err != nil && !os.IsNotExist(err) {
		t.Fatalf("read root B unseen dir: %v", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		f, oerr := os.Open(filepath.Join(unseenDir, e.Name()))
		if oerr != nil {
			continue
		}
		var rec struct {
			From           string `json:"from"`
			Text           string `json:"text"`
			ExternalSource string `json:"external_source"`
			ExternalID     string `json:"external_id"`
			Target         string `json:"target"`
		}
		if jerr := json.NewDecoder(f).Decode(&rec); jerr != nil {
			f.Close()
			continue
		}
		if rec.ExternalSource == "relay" && rec.ExternalID == msgID &&
			rec.From == "alpha" && rec.Target == "beta" && strings.Contains(rec.Text, "cross-instance payload") {
			found = f
			break
		}
		f.Close()
	}
	if found == nil {
		t.Fatalf("remote message not imported into root B bus unseen/ (expected external_id=%s from=alpha target=beta text='cross-instance payload')", msgID)
	}
	found.Close()

	// Repeated import of the same message must remain idempotent:
	// no second Comms message is created for the same external ID.
	unseenCountBefore := countJSONFiles(unseenDir)
	if err := processPendingMessages(rootB); err != nil {
		t.Fatalf("repeat processPendingMessages root B: %v", err)
	}
	unseenCountAfter := countJSONFiles(unseenDir)
	if unseenCountAfter != unseenCountBefore {
		t.Fatalf("repeated import created a new message: unseen count %d -> %d", unseenCountBefore, unseenCountAfter)
	}
}

// countJSONFiles counts regular *.json files in a directory (not recursive).
func countJSONFiles(dir string) int {
	entries, err := os.ReadDir(dir)
	if err != nil {
		return -1
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() && strings.HasSuffix(e.Name(), ".json") {
			n++
		}
	}
	return n
}

// TestLargePayloadRoundtripPreservesBytes tests that a large payload (>= 50 KiB)
// is correctly transported through the full wormhole pipeline: Publish → Git-Maildir → Import → Comms.
// The payload is deterministic so we can verify it arrives intact.
func TestLargePayloadRoundtripPreservesBytes(t *testing.T) {
	t.Helper()
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	writeConfig(t, root, cfg)

	// Ensure the wormhole directory structure exists.
	if err := EnsureDir(root); err != nil {
		t.Fatalf("EnsureDir failed: %v", err)
	}

	// Create a deterministic payload with at least 50 KiB content.
	const largePayloadSize = 51200 // 50 KiB
	payload := make([]byte, largePayloadSize)
	// Fill with a known pattern so we can verify roundtrip.
	for i := range payload {
		payload[i] = byte(i % 256)
	}
	// Ensure trailing newline for the wormhole message format.
	payload = append(payload, '\n')

	// Build a wormhole message with the large payload.
	msg := NewMessage("alpha", "beta", "test", string(payload), "")
	msg.ID = "large-payload-test-" + time.Now().Format("20060102T150405Z")
	msg.From = "alpha"
	msg.To = "beta"

	// Step 1: Publish from alpha to beta using alpha's spool.
	cloneTimeout, commandTimeout := testTimeouts(cfg)
	senderSpool := newSpool(root, cfg.Repositories[0], 3, "alpha", cloneTimeout, commandTimeout)
	if err := senderSpool.Publish(msg); err != nil {
		t.Fatalf("publish failed: %v", err)
	}

	// Debug: check if message file exists in local working tree
	msgPath := filepath.Join(senderSpool.git.CloneDir(), "mail", "beta", "new", msg.ID+".md")
	if _, err := os.Stat(msgPath); err != nil {
		t.Logf("Message file NOT found at %s: %v", msgPath, err)
	} else {
		t.Logf("Message file found at %s", msgPath)
	}

	// Verify the message was pushed to the remote git repo.
	deadline := time.Now().Add(10 * time.Second)
	var pushed bool
	for time.Now().Before(deadline) {
		pushed = remoteHas(t, remote, "mail/beta/new/"+msg.ID+".md") ||
			remoteHas(t, remote, "mail/beta/cur/"+msg.ID+".md") ||
			remoteHas(t, remote, "mail/beta/processed/"+msg.ID+".md")
		if pushed {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if !pushed {
		t.Fatalf("spool did not push claimed message %s to remote", msg.ID)
	}

	// Step 2: Import into Comms for beta (the recipient).
	// Set STARFLEET_SHIP_ID to beta so the bus operates as beta.
	t.Setenv("STARFLEET_SHIP_ID", "beta")
	defer t.Setenv("STARFLEET_SHIP_ID", "")

	// Import into Comms for beta.
	t.Logf("Calling processPendingMessages for beta...")
	if err := processPendingMessages(root); err != nil {
		t.Fatalf("processPendingMessages: %v", err)
	}
	t.Logf("processPendingMessages returned successfully")

	// Check if message file was moved from new/ to cur/ or processed/
	msgPathNew := filepath.Join(senderSpool.git.CloneDir(), "mail", "beta", "new", msg.ID+".md")
	msgPathCur := filepath.Join(root, ".starfleet-ai", "var", "wormholes", "relay", "mail", "beta", "cur", msg.ID+".md")
	msgPathProc := filepath.Join(root, ".starfleet-ai", "var", "wormholes", "relay", "mail", "beta", "processed", msg.ID+".md")
	if _, err := os.Stat(msgPathNew); err == nil {
		t.Logf("Message still in new/")
	} else {
		t.Logf("Message NOT in new/")
	}
	if _, err := os.Stat(msgPathCur); err == nil {
		t.Logf("Message found in cur/")
	} else {
		t.Logf("Message NOT in cur/")
	}
	if _, err := os.Stat(msgPathProc); err == nil {
		t.Logf("Message found in processed/")
	} else {
		t.Logf("Message NOT in processed/")
	}

	// Check the inbox via disk scan (matches existing test patterns).
	// Check the inbox via disk scan (matches existing test patterns).
	deadline = time.Now().Add(10 * time.Second)
	importedBefore := 0
	for time.Now().Before(deadline) {
		importedBefore = countJSONFiles(filepath.Join(root, ".starfleet-ai", "var", "comms", "msgs", "beta", "unseen")) +
			countJSONFiles(filepath.Join(root, ".starfleet-ai", "var", "comms", "msgs", "beta", "cur")) +
			countJSONFiles(filepath.Join(root, ".starfleet-ai", "var", "comms", "msgs", "beta", "processed"))
		time.Sleep(200 * time.Millisecond)
		if importedBefore > 0 {
			break
		}
	}
	t.Logf("importedBefore = %d", importedBefore)
	// Process again.
	if err := processPendingMessages(root); err != nil {
		t.Fatalf("second processPendingMessages: %v", err)
	}
	importedAfter := countJSONFiles(filepath.Join(root, ".starfleet-ai", "var", "comms", "msgs", "beta", "unseen")) +
		countJSONFiles(filepath.Join(root, ".starfleet-ai", "var", "comms", "msgs", "beta", "seen"))
	if importedAfter <= importedBefore {
		t.Fatalf("repeated import did not create new messages: %d -> %d", importedBefore, importedAfter)
	}

	t.Logf("Large payload roundtrip test passed: %d bytes", largePayloadSize)
}

// ---- daemon lifecycle process tests ----------------------------------------

// TestDaemonLifecycle tests the full daemon start/stop/status cycle using
// actual subprocesses via the CLI with a local git remote.
func TestDaemonLifecycle(t *testing.T) {
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	writeConfig(t, root, cfg)

	binary := buildTestBinary(t)

	// Test 1: Start daemon
	cmd := exec.Command(binary, "wormhole", "start")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "MPBT_WORKSPACE_ROOT="+root)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("wormhole start failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Wormhole daemon started") {
		t.Fatalf("unexpected start output: %s", out)
	}

	// Test 2: Status shows running
	cmd = exec.Command(binary, "wormhole", "status")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "MPBT_WORKSPACE_ROOT="+root)
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("wormhole status failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Status: running") {
		t.Fatalf("status should show running: %s", out)
	}

	// Test 3: Double start fails
	cmd = exec.Command(binary, "wormhole", "start")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "MPBT_WORKSPACE_ROOT="+root)
	out, err = cmd.CombinedOutput()
	if err == nil {
		t.Fatal("second start should fail")
	}
	if !strings.Contains(string(out), "already running") {
		t.Fatalf("double start error should mention already running: %s", out)
	}

	// Test 4: Stop daemon
	cmd = exec.Command(binary, "wormhole", "stop")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "MPBT_WORKSPACE_ROOT="+root)
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("wormhole stop failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Wormhole daemon stopped") {
		t.Fatalf("unexpected stop output: %s", out)
	}

	// Test 5: Status shows not running
	cmd = exec.Command(binary, "wormhole", "status")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "MPBT_WORKSPACE_ROOT="+root)
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("wormhole status failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "not running") {
		t.Fatalf("status should show not running: %s", out)
	}

	// Test 6: Restart after stop
	cmd = exec.Command(binary, "wormhole", "start")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "MPBT_WORKSPACE_ROOT="+root)
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("wormhole restart start failed: %v\n%s", err, out)
	}
	// Stop again for cleanup
	cmd = exec.Command(binary, "wormhole", "stop")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "MPBT_WORKSPACE_ROOT="+root)
	_, _ = cmd.CombinedOutput()
}

// TestDaemonStalePID tests that a stale PID file is cleaned up on start.
func TestDaemonStalePID(t *testing.T) {
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	writeConfig(t, root, cfg)

	binary := buildTestBinary(t)

	// Create a stale PID file with a non-existent PID
	if err := EnsureDir(root); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	if err := writePIDFile(PIDFile(root), 999999); err != nil {
		t.Fatalf("write stale PID: %v", err)
	}
	// Also create a stale state file
	st := daemonState{
		Status:  "running",
		Started: time.Now().UTC().Format(time.RFC3339),
		PID:     999999,
		Root:    root,
		Running: true,
	}
	_ = writeStateFile(StateFile(root), st)

	// Start should succeed (clean up stale PID and start fresh)
	cmd := exec.Command(binary, "wormhole", "start")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "MPBT_WORKSPACE_ROOT="+root)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("wormhole start with stale PID failed: %v\n%s", err, out)
	}

	// Verify it's actually running
	cmd = exec.Command(binary, "wormhole", "status")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "MPBT_WORKSPACE_ROOT="+root)
	out, err = cmd.CombinedOutput()
	if err != nil || !strings.Contains(string(out), "Status: running") {
		t.Fatalf("daemon not running after stale PID cleanup: %v\n%s", err, out)
	}

	// Stop cleanly
	cmd = exec.Command(binary, "wormhole", "stop")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "MPBT_WORKSPACE_ROOT="+root)
	_, _ = cmd.CombinedOutput()
}

// TestDaemonStalePIDWithStateMismatch tests that a PID/state disagreement is
// a hard error: the recorded PID cannot be trusted (possible PID reuse), so
// start must refuse to act and must not silently overwrite the files.
func TestDaemonStalePIDWithStateMismatch(t *testing.T) {
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	writeConfig(t, root, cfg)

	binary := buildTestBinary(t)
	t.Setenv("STARFLEETCTL_BINARY", binary)

	// Create a PID file with one PID
	if err := EnsureDir(root); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	if err := writePIDFile(PIDFile(root), 12345); err != nil {
		t.Fatalf("write stale PID: %v", err)
	}
	// Create a state file with a DIFFERENT PID
	st := daemonState{
		Status:  "running",
		Started: time.Now().UTC().Format(time.RFC3339),
		PID:     54321,
		Root:    root,
		Running: true,
	}
	_ = writeStateFile(StateFile(root), st)

	// Start must refuse: the files disagree, the recorded PID cannot be
	// verified against its recorded instance (possible PID reuse).
	cmd := exec.Command(binary, "wormhole", "start")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "MPBT_WORKSPACE_ROOT="+root)
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("wormhole start with mismatched PID/state should FAIL, but succeeded:\n%s", out)
	}
	if !strings.Contains(string(out), "disagree") {
		t.Fatalf("start failure should explain the pid/state disagreement: %s", out)
	}
	// The mismatched files must be preserved (not silently overwritten) so
	// a human can inspect them.
	if _, err := os.Stat(PIDFile(root)); err != nil {
		t.Fatalf("pid file should be preserved on refusal: %v", err)
	}
	if _, err := os.Stat(StateFile(root)); err != nil {
		t.Fatalf("state file should be preserved on refusal: %v", err)
	}

	// Stop must equally refuse (same shared identity rules), not signal
	// anything based on the disagreeing files.
	cmd = exec.Command(binary, "wormhole", "stop")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "MPBT_WORKSPACE_ROOT="+root)
	out, err = cmd.CombinedOutput()
	if err == nil {
		t.Fatalf("wormhole stop with mismatched PID/state should FAIL, but succeeded:\n%s", out)
	}
	if !strings.Contains(string(out), "disagree") {
		t.Fatalf("stop failure should explain the pid/state disagreement: %s", out)
	}

	// Manual recovery: removing the files must make start work again.
	_ = os.Remove(PIDFile(root))
	_ = os.Remove(StateFile(root))
	cmd = exec.Command(binary, "wormhole", "start")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "MPBT_WORKSPACE_ROOT="+root)
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("wormhole start after manual cleanup failed: %v\n%s", err, out)
	}

	// Stop cleanly
	cmd = exec.Command(binary, "wormhole", "stop")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "MPBT_WORKSPACE_ROOT="+root)
	_, _ = cmd.CombinedOutput()
}

// TestDaemonAutostartIdempotent tests that Autostart is idempotent.
func TestDaemonAutostartIdempotent(t *testing.T) {
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	cfg.Enabled = true
	writeConfig(t, root, cfg)

	binary := buildTestBinary(t)
	t.Setenv("STARFLEETCTL_BINARY", binary)

	// First autostart should start the daemon
	started, err := Autostart(root)
	if err != nil {
		t.Fatalf("first Autostart failed: %v", err)
	}
	if !started {
		t.Fatal("first Autostart should return true")
	}

	// Second autostart should return false (already running)
	started2, err := Autostart(root)
	if err != nil {
		t.Fatalf("second Autostart failed: %v", err)
	}
	if started2 {
		t.Fatal("second Autostart should return false (already running)")
	}

	// Clean up
	_ = StopDaemon(root)
}

// TestParallelStart tests that two parallel start calls result in exactly one worker.
func TestParallelStart(t *testing.T) {
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	writeConfig(t, root, cfg)

	binary := buildTestBinary(t)

	// Start two parallel start commands
	type startResult struct {
		err error
		out string
	}
	resultCh := make(chan startResult, 2)
	for i := 0; i < 2; i++ {
		go func() {
			cmd := exec.Command(binary, "wormhole", "start")
			cmd.Dir = root
			cmd.Env = append(os.Environ(), "MPBT_WORKSPACE_ROOT="+root)
			out, err := cmd.CombinedOutput()
			resultCh <- startResult{err: err, out: string(out)}
		}()
	}

	// Collect both results
	results := make([]startResult, 0, 2)
	for i := 0; i < 2; i++ {
		results = append(results, <-resultCh)
	}

	// Exactly one should succeed, one should fail with "already running"
	successCount := 0
	alreadyRunningCount := 0
	for _, r := range results {
		switch {
		case r.err == nil:
			successCount++
		case strings.Contains(r.out, "already running"):
			alreadyRunningCount++
		default:
			t.Errorf("unexpected start error: %v\n%s", r.err, r.out)
		}
	}

	if successCount != 1 {
		t.Fatalf("expected exactly 1 successful start, got %d (results: %+v)", successCount, results)
	}
	if alreadyRunningCount != 1 {
		t.Fatalf("expected exactly 1 'already running' error, got %d (results: %+v)", alreadyRunningCount, results)
	}

	// Clean up
	cmd := exec.Command(binary, "wormhole", "stop")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "MPBT_WORKSPACE_ROOT="+root)
	_, _ = cmd.CombinedOutput()
}

// TestParallelAutostart tests that two parallel Autostart calls result in
// exactly one worker and no errors: concurrent autostarts converge (the
// loser of the race reports started=false without error).
func TestParallelAutostart(t *testing.T) {
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	cfg.Enabled = true
	writeConfig(t, root, cfg)

	binary := buildTestBinary(t)
	t.Setenv("STARFLEETCTL_BINARY", binary)

	type autoResult struct {
		started bool
		err     error
	}
	resultCh := make(chan autoResult, 2)
	for i := 0; i < 2; i++ {
		go func() {
			started, err := Autostart(root)
			resultCh <- autoResult{started: started, err: err}
		}()
	}

	results := make([]autoResult, 0, 2)
	for i := 0; i < 2; i++ {
		results = append(results, <-resultCh)
	}

	for i, r := range results {
		if r.err != nil {
			t.Fatalf("parallel autostart %d returned error: %v", i, r.err)
		}
	}
	startedCount := 0
	for _, r := range results {
		if r.started {
			startedCount++
		}
	}
	if startedCount != 1 {
		t.Fatalf("expected exactly 1 autostart to report started=true, got %d (results: %+v)", startedCount, results)
	}
	if !IsRunning(root) {
		t.Fatal("daemon should be running after parallel autostarts")
	}

	// Clean up
	_ = StopDaemon(root)
}

// TestWorkerCrashDuringStartup tests that a worker failing during startup
// (fatal startup sync failure) is detected immediately as an early exit —
// the parent does not wait the full 30s timeout — and leaves no PID file
// or ready file behind, while the state file records the failure reason.
func TestWorkerCrashDuringStartup(t *testing.T) {
	// This test simulates a worker crash by using an invalid config
	// (remote that doesn't exist) so the startup sync fails fatally.
	root := t.TempDir()
	cfg := testConfig("/nonexistent/remote.git")
	writeConfig(t, root, cfg)

	binary := buildTestBinary(t)

	start := time.Now()
	cmd := exec.Command(binary, "wormhole", "start")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "MPBT_WORKSPACE_ROOT="+root)
	out, err := cmd.CombinedOutput()
	elapsed := time.Since(start)

	// Start should fail
	if err == nil {
		t.Fatal("start should fail with invalid remote")
	}
	// The early child exit must be detected — the message names the exit,
	// not the 30s timeout.
	if !strings.Contains(string(out), "exited during startup") {
		t.Fatalf("expected early-exit error, got: %s", out)
	}
	// And it must be reported fast (the startup sync fails within the git
	// command timeout; a full 30s timeout wait would mean broken detection).
	if elapsed > 25*time.Second {
		t.Fatalf("early child exit detection too slow: %v\n%s", elapsed, out)
	}

	// Verify no daemon is running
	cmd = exec.Command(binary, "wormhole", "status")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "MPBT_WORKSPACE_ROOT="+root)
	out, _ = cmd.CombinedOutput()
	if strings.Contains(string(out), "Status: running") {
		t.Fatalf("daemon should not be running after failed start: %s", out)
	}
	// The state file survives with the recorded startup error, and status
	// surfaces it.
	if !strings.Contains(string(out), "startup sync failed") {
		t.Fatalf("status should surface the recorded startup error: %s", out)
	}

	// No PID file or ready file left behind (state file is kept deliberately).
	if _, err := os.Stat(PIDFile(root)); err == nil {
		t.Fatal("stale PID file left after failed start")
	}
	if _, err := os.Stat(filepath.Join(WorkDir(root), "ready")); err == nil {
		t.Fatal("stale ready file left after failed start")
	}
}

// TestWorkerCrashAfterStart tests that if a worker crashes after successful start,
// the daemon is properly detected as dead.
func TestWorkerCrashAfterStart(t *testing.T) {
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	writeConfig(t, root, cfg)

	binary := buildTestBinary(t)

	// Start daemon
	cmd := exec.Command(binary, "wormhole", "start")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "MPBT_WORKSPACE_ROOT="+root)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("start failed: %v\n%s", err, out)
	}

	// Get the PID
	cmd = exec.Command(binary, "wormhole", "status")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "MPBT_WORKSPACE_ROOT="+root)
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("status failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Status: running") {
		t.Fatalf("daemon not running: %s", out)
	}

	// Extract PID from status output
	var pid int
	fmt.Sscanf(string(out), "Wormhole Daemon Status:\n  PID: %d", &pid)
	if pid == 0 {
		t.Fatal("could not parse PID from status")
	}

	// Kill the worker process directly (simulate crash)
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("failed to kill worker: %v", err)
	}

	// Wait a bit for process to die
	time.Sleep(500 * time.Millisecond)

	// Status should show dead
	cmd = exec.Command(binary, "wormhole", "status")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "MPBT_WORKSPACE_ROOT="+root)
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("status failed: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "dead") && !strings.Contains(string(out), "not running") {
		t.Fatalf("status should show dead or not running after worker crash: %s", out)
	}

	// Stop should clean up
	cmd = exec.Command(binary, "wormhole", "stop")
	cmd.Dir = root
	cmd.Env = append(os.Environ(), "MPBT_WORKSPACE_ROOT="+root)
	_, _ = cmd.CombinedOutput()
}

// TestNoForeignProcessKill tests that stop never kills a non-wormhole process.
func TestNoForeignProcessKill(t *testing.T) {
	// Start a harmless background process
	cmd := exec.Command("sleep", "300")
	cmd.Start()
	foreignPID := cmd.Process.Pid
	defer syscall.Kill(foreignPID, syscall.SIGKILL) // cleanup

	// Create PID and state files pointing to the foreign process
	root := t.TempDir()
	if err := EnsureDir(root); err != nil {
		t.Fatalf("EnsureDir: %v", err)
	}
	if err := writePIDFile(PIDFile(root), foreignPID); err != nil {
		t.Fatalf("write PID: %v", err)
	}
	st := daemonState{
		Status:  "running",
		Started: time.Now().UTC().Format(time.RFC3339),
		PID:     foreignPID,
		Root:    root,
		Running: true,
	}
	_ = writeStateFile(StateFile(root), st)

	// Stop should refuse to kill the foreign process
	binary := buildTestBinary(t)
	stopCmd := exec.Command(binary, "wormhole", "stop")
	stopCmd.Dir = root
	stopCmd.Env = append(os.Environ(), "MPBT_WORKSPACE_ROOT="+root)
	out, err := stopCmd.CombinedOutput()
	if err == nil {
		t.Fatal("stop should fail when PID is not a wormhole worker")
	}
	if !strings.Contains(string(out), "not verifiable as this workspace's wormhole worker") {
		t.Fatalf("stop should explain the unverifiable PID refusal: %s", out)
	}

	// Foreign process should still be alive
	if !isProcessAlive(foreignPID) {
		t.Fatal("foreign process was killed!")
	}
	// And the PID/state files must be preserved (refusal, not silent cleanup)
	if _, err := os.Stat(PIDFile(root)); err != nil {
		t.Fatalf("pid file should be preserved on refusal: %v", err)
	}
}

// daemonTestEnv builds a clean environment for wormhole CLI subprocesses in
// tests: ambient STARFLEET_*/MPBT_* variables from the test runner (a fleet
// ship's session may export STARFLEET_SHIP_ID, STARFLEETCTL_BINARY etc.)
// are dropped so exactly one entry for each relevant variable exists, and
// the entries appended here win regardless of getenv ordering.
func daemonTestEnv(root, ship string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "STARFLEET_SHIP_ID=") ||
			strings.HasPrefix(kv, "STARFLEET_BUS_DIR=") ||
			strings.HasPrefix(kv, "MPBT_WORKSPACE_ROOT=") ||
			strings.HasPrefix(kv, "STARFLEETCTL_BINARY=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "MPBT_WORKSPACE_ROOT="+root)
	if ship != "" {
		env = append(env, "STARFLEET_SHIP_ID="+ship)
	}
	return env
}

// TestDaemonRestartMultiCycleReadOnly proves the read-only sync guarantees
// across REAL daemon process restarts, including an unclean crash:
//   - a read-only receiver imports each message exactly once across restarts
//   - the remote's history is never advanced by the read-only side (its HEAD
//     only ever moves when the read-write sender publishes)
//   - read-only mailbox transitions stay local (the remote keeps new/<id>.md)
//   - a crashed worker is cleaned up as a stale instance and the transport
//     resyncs and continues importing after restart
func TestDaemonRestartMultiCycleReadOnly(t *testing.T) {
	remote := setupRemote(t)
	rootRO := t.TempDir()
	cfgRO := testConfig(remote)
	cfgRO.Repositories[0].Access = "read-only"
	writeConfig(t, rootRO, cfgRO)

	rootRW := t.TempDir()
	cfgRW := testConfig(remote)
	writeConfig(t, rootRW, cfgRW)

	t.Setenv("STARFLEET_SHIP_ID", "alpha")
	cloneTimeout, commandTimeout := testTimeouts(cfgRW)
	sender := newSpool(rootRW, cfgRW.Repositories[0], cfgRW.Sync.PushRetries, "beta", cloneTimeout, commandTimeout)

	binary := buildTestBinary(t)
	runDaemonCmd := func(args ...string) (string, error) {
		cmd := exec.Command(binary, args...)
		cmd.Dir = rootRO
		cmd.Env = daemonTestEnv(rootRO, "alpha")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	// Best-effort cleanup if the test fails mid-way.
	t.Cleanup(func() { _, _ = runDaemonCmd("wormhole", "stop") })

	busRO, err := comms.New(rootRO)
	if err != nil {
		t.Fatal(err)
	}
	waitImports := func(want int) {
		t.Helper()
		deadline := time.Now().Add(30 * time.Second)
		for time.Now().Before(deadline) {
			if countFiles(t, busRO.MsgDir) == want {
				return
			}
			time.Sleep(200 * time.Millisecond)
		}
		t.Fatalf("expected %d imported messages, got %d", want, countFiles(t, busRO.MsgDir))
	}
	remoteHead := func() string {
		return strings.TrimSpace(gitOut(t, "--git-dir", remote, "rev-parse", "main"))
	}

	// Phase 1: publish m1, start daemon, import exactly once, stop cleanly.
	m1 := NewMessage("beta", "alpha", "ship", "first over daemon", "")
	if err := sender.Publish(m1); err != nil {
		t.Fatalf("publish m1: %v", err)
	}
	headAfterM1 := remoteHead()

	if out, err := runDaemonCmd("wormhole", "start"); err != nil {
		t.Fatalf("daemon start: %v\n%s", err, out)
	}
	waitImports(1)
	if got := remoteHead(); got != headAfterM1 {
		t.Fatalf("read-only daemon advanced remote after phase 1: %s -> %s", headAfterM1, got)
	}
	if out, err := runDaemonCmd("wormhole", "stop"); err != nil {
		t.Fatalf("daemon stop: %v\n%s", err, out)
	}

	// Phase 2: graceful restart between cycles — m2 imports, m1 is NOT
	// re-imported across the restart.
	m2 := NewMessage("beta", "alpha", "ship", "second after restart", "")
	if err := sender.Publish(m2); err != nil {
		t.Fatalf("publish m2: %v", err)
	}
	headAfterM2 := remoteHead()

	if out, err := runDaemonCmd("wormhole", "start"); err != nil {
		t.Fatalf("daemon restart: %v\n%s", err, out)
	}
	waitImports(2)
	if got := remoteHead(); got != headAfterM2 {
		t.Fatalf("read-only daemon advanced remote after phase 2: %s -> %s", headAfterM2, got)
	}

	// Phase 3: crash the worker (SIGKILL) while idle, restart, resync.
	// Let idle ticks pass, then wait for the repository lock to be free so
	// no git operation is in flight at the kill.
	time.Sleep(2 * time.Second)
	spRO := newSpool(rootRO, cfgRO.Repositories[0], cfgRO.Sync.PushRetries, "alpha", cloneTimeout, commandTimeout)
	lockDeadline := time.Now().Add(10 * time.Second)
	for spRO.git.IsLocked() && time.Now().Before(lockDeadline) {
		time.Sleep(150 * time.Millisecond)
	}
	var pid int
	if out, err := runDaemonCmd("wormhole", "status"); err != nil {
		t.Fatalf("status before crash: %v\n%s", err, out)
	} else if _, perr := fmt.Sscanf(out, "Wormhole Daemon Status:\n  PID: %d", &pid); perr != nil || pid == 0 {
		t.Fatalf("could not parse daemon PID from status: %s", out)
	}
	if err := syscall.Kill(pid, syscall.SIGKILL); err != nil {
		t.Fatalf("kill worker: %v", err)
	}
	deadDeadline := time.Now().Add(10 * time.Second)
	for isProcessAlive(pid) && time.Now().Before(deadDeadline) {
		time.Sleep(100 * time.Millisecond)
	}
	if isProcessAlive(pid) {
		t.Fatal("worker did not die after SIGKILL")
	}

	// The crashed worker left stale runtime files; restart must treat them
	// as a stale (dead) instance, clean them up and start a fresh worker.
	if out, err := runDaemonCmd("wormhole", "start"); err != nil {
		t.Fatalf("daemon restart after crash: %v\n%s", err, out)
	}

	// Nothing is re-imported by the restarted daemon…
	time.Sleep(3 * time.Second) // let ≥2 idle ticks pass with nothing new
	if n := countFiles(t, busRO.MsgDir); n != 2 {
		t.Fatalf("restart after crash re-imported messages: got %d, want 2", n)
	}
	// …and a new message still arrives (resync after crash).
	m3 := NewMessage("beta", "alpha", "ship", "third after crash", "")
	if err := sender.Publish(m3); err != nil {
		t.Fatalf("publish m3: %v", err)
	}
	headAfterM3 := remoteHead()
	waitImports(3)
	if got := remoteHead(); got != headAfterM3 {
		t.Fatalf("read-only daemon advanced remote after phase 3: %s -> %s", headAfterM3, got)
	}

	// The remote mailbox was never advanced by the read-only side: the first
	// message is still in new/ for every other reader.
	if !remoteHas(t, remote, "mail/alpha/new/"+m1.ID+".md") {
		t.Fatal("read-only daemon modified the remote mailbox state")
	}

	if out, err := runDaemonCmd("wormhole", "stop"); err != nil {
		t.Fatalf("final daemon stop: %v\n%s", err, out)
	}
}

// TestStartupSyncDisabledStartsWithoutSync proves that with
// sync.startup_sync disabled the daemon becomes ready WITHOUT any initial
// sync: an unreachable remote neither blocks the start nor kills the daemon
// later (per-cycle failures are logged and retried, never fatal).
func TestStartupSyncDisabledStartsWithoutSync(t *testing.T) {
	root := t.TempDir()
	cfg := testConfig("/nonexistent/remote.git")
	cfg.Sync.StartupSync = false
	writeConfig(t, root, cfg)

	binary := buildTestBinary(t)
	cmd := exec.Command(binary, "wormhole", "start")
	cmd.Dir = root
	cmd.Env = daemonTestEnv(root, "alpha")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("start with startup_sync=false and unreachable remote must succeed: %v\n%s", err, out)
	}
	t.Cleanup(func() {
		cmd := exec.Command(binary, "wormhole", "stop")
		cmd.Dir = root
		cmd.Env = daemonTestEnv(root, "alpha")
		_, _ = cmd.CombinedOutput()
	})

	// The daemon must stay alive across several failing ticks.
	time.Sleep(3 * time.Second)
	cmd = exec.Command(binary, "wormhole", "status")
	cmd.Dir = root
	cmd.Env = daemonTestEnv(root, "alpha")
	out, err = cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("status: %v\n%s", err, out)
	}
	if !strings.Contains(string(out), "Status: running") {
		t.Fatalf("daemon died from failing sync cycles: %s", out)
	}

	// The per-cycle failures must have actually occurred (and been logged,
	// not treated as fatal) — otherwise this test would pass vacuously.
	logData, lerr := os.ReadFile(filepath.Join(WorkDir(root), "daemon.log"))
	if lerr != nil {
		t.Fatalf("read daemon.log: %v", lerr)
	}
	if !strings.Contains(string(logData), "[wormhole] cycle:") {
		t.Fatalf("expected logged cycle failures for unreachable remote, log:\n%s", logData)
	}

	cmd = exec.Command(binary, "wormhole", "stop")
	cmd.Dir = root
	cmd.Env = daemonTestEnv(root, "alpha")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("stop: %v\n%s", err, out)
	}
}

// TestSyncFailureKeepsDaemonRunning proves that a sync failure AFTER a
// successful start is transient: the daemon keeps running through failing
// ticks while the remote is unreachable, and resumes importing once the
// remote is back.
func TestSyncFailureKeepsDaemonRunning(t *testing.T) {
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	writeConfig(t, root, cfg)

	t.Setenv("STARFLEET_SHIP_ID", "alpha")
	binary := buildTestBinary(t)
	runDaemonCmd := func(args ...string) (string, error) {
		cmd := exec.Command(binary, args...)
		cmd.Dir = root
		cmd.Env = daemonTestEnv(root, "alpha")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	t.Cleanup(func() { _, _ = runDaemonCmd("wormhole", "stop") })

	if out, err := runDaemonCmd("wormhole", "start"); err != nil {
		t.Fatalf("start: %v\n%s", err, out)
	}

	// Break the remote (rename away) — every tick must now fail, but the
	// daemon must survive.
	broken := remote + ".broken"
	if err := os.Rename(remote, broken); err != nil {
		t.Fatalf("break remote: %v", err)
	}
	time.Sleep(3 * time.Second)

	out, err := runDaemonCmd("wormhole", "status")
	if err != nil {
		t.Fatalf("status while remote broken: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Status: running") {
		t.Fatalf("daemon must survive temporary sync failures: %s", out)
	}
	logData, lerr := os.ReadFile(filepath.Join(WorkDir(root), "daemon.log"))
	if lerr != nil {
		t.Fatalf("read daemon.log: %v", lerr)
	}
	if !strings.Contains(string(logData), "[wormhole] cycle:") {
		t.Fatalf("expected logged cycle failures while remote broken, log:\n%s", logData)
	}

	// Restore the remote and publish a message: the daemon must recover
	// and import it.
	if err := os.Rename(broken, remote); err != nil {
		t.Fatalf("restore remote: %v", err)
	}
	bus, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}
	cloneTimeout, commandTimeout := testTimeouts(cfg)
	sender := newSpool(root, cfg.Repositories[0], cfg.Sync.PushRetries, "beta", cloneTimeout, commandTimeout)
	m1 := NewMessage("beta", "alpha", "ship", "after recovery", "")
	if err := sender.Publish(m1); err != nil {
		t.Fatalf("publish after recovery: %v", err)
	}
	deadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(deadline) {
		if countFiles(t, bus.MsgDir) == 1 {
			break
		}
		time.Sleep(200 * time.Millisecond)
	}
	if n := countFiles(t, bus.MsgDir); n != 1 {
		t.Fatalf("daemon did not resume importing after remote recovery: got %d messages, want 1", n)
	}

	if out, err := runDaemonCmd("wormhole", "stop"); err != nil {
		t.Fatalf("stop: %v\n%s", err, out)
	}
}

// TestStaleReadyMarkerDoesNotFakeStart proves a leftover "ready" marker can
// never fake a successful start:
//   - with a broken remote (fatal startup sync), start fails even though a
//     ready file existed when the worker was forked — the parent's handshake
//     requires THIS worker's pid file plus ITS ready marker, and the worker
//     removes any stale marker before writing its pid
//   - with a healthy remote, a stale marker (with or without stale pid/state
//     files) is cleaned up and replaced by the fresh worker's own marker
func TestStaleReadyMarkerDoesNotFakeStart(t *testing.T) {
	remote := setupRemote(t)
	binary := buildTestBinary(t)

	// Case 1: stale ready + unreachable remote — start must fail; the stale
	// marker must not satisfy the readiness handshake.
	rootBad := t.TempDir()
	cfgBad := testConfig("/nonexistent/remote.git")
	writeConfig(t, rootBad, cfgBad)
	if err := EnsureDir(rootBad); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(WorkDir(rootBad), "ready"), []byte("STALE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(binary, "wormhole", "start")
	cmd.Dir = rootBad
	cmd.Env = daemonTestEnv(rootBad, "")
	out, err := cmd.CombinedOutput()
	if err == nil {
		t.Fatal("start with broken remote must fail even with a stale ready marker present")
	}
	if !strings.Contains(string(out), "exited during startup") {
		t.Fatalf("expected early-exit failure, got: %s", out)
	}
	// The stale marker must be gone after the failed start.
	if _, err := os.Stat(filepath.Join(WorkDir(rootBad), "ready")); err == nil {
		t.Fatal("stale ready marker survived a failed start")
	}

	// Case 2: stale ready alone (no pid/state) with a healthy remote — the
	// fresh worker replaces it with its own marker.
	rootOK := t.TempDir()
	cfgOK := testConfig(remote)
	writeConfig(t, rootOK, cfgOK)
	if err := EnsureDir(rootOK); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(WorkDir(rootOK), "ready"), []byte("STALE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command(binary, "wormhole", "start")
	cmd.Dir = rootOK
	cmd.Env = daemonTestEnv(rootOK, "")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("start with stale ready marker: %v\n%s", err, out)
	}
	data, rerr := os.ReadFile(filepath.Join(WorkDir(rootOK), "ready"))
	if rerr != nil || string(data) != "ready\n" {
		t.Fatalf("ready marker must be the fresh worker's own, got %q (err=%v)", string(data), rerr)
	}
	cmd = exec.Command(binary, "wormhole", "stop")
	cmd.Dir = rootOK
	cmd.Env = daemonTestEnv(rootOK, "")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("stop: %v\n%s", err, out)
	}

	// Case 3: stale ready + stale dead instance (pid/state files) — the
	// stale instance is cleaned up and the marker replaced.
	rootStale := t.TempDir()
	cfgStale := testConfig(remote)
	writeConfig(t, rootStale, cfgStale)
	if err := EnsureDir(rootStale); err != nil {
		t.Fatal(err)
	}
	if err := writePIDFile(PIDFile(rootStale), 999999); err != nil {
		t.Fatal(err)
	}
	if err := writeStateFile(StateFile(rootStale), daemonState{
		Status:  "running",
		Started: time.Now().UTC().Format(time.RFC3339),
		PID:     999999,
		Root:    rootStale,
		Running: true,
	}); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(WorkDir(rootStale), "ready"), []byte("STALE\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	cmd = exec.Command(binary, "wormhole", "start")
	cmd.Dir = rootStale
	cmd.Env = daemonTestEnv(rootStale, "")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("start with stale instance: %v\n%s", err, out)
	}
	data, rerr = os.ReadFile(filepath.Join(WorkDir(rootStale), "ready"))
	if rerr != nil || string(data) != "ready\n" {
		t.Fatalf("ready marker must be the fresh worker's own, got %q (err=%v)", string(data), rerr)
	}
	cmd = exec.Command(binary, "wormhole", "stop")
	cmd.Dir = rootStale
	cmd.Env = daemonTestEnv(rootStale, "")
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("stop: %v\n%s", err, out)
	}
}

// TestUnverifiedPIDFilesFailClosed proves the fail-closed identity rules for
// pid and state files while a pid file records a LIVE foreign process: start
// and stop refuse to act on an unverifiable PID (files preserved for manual
// inspection), status surfaces the refusal, and the foreign process is never
// signaled. A pid file that is present but unparseable, empty, non-positive
// or unreadable is a hard refusal — never "no daemon"; only a genuinely
// absent pid file means no registered instance.
func TestUnverifiedPIDFilesFailClosed(t *testing.T) {
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	writeConfig(t, root, cfg)

	// A live process that is NOT a wormhole worker.
	foreign := exec.Command("sleep", "300")
	if err := foreign.Start(); err != nil {
		t.Fatal(err)
	}
	foreignPID := foreign.Process.Pid
	defer syscall.Kill(foreignPID, syscall.SIGKILL)

	binary := buildTestBinary(t)
	runCmd := func(args ...string) (string, error) {
		cmd := exec.Command(binary, args...)
		cmd.Dir = root
		cmd.Env = daemonTestEnv(root, "")
		out, err := cmd.CombinedOutput()
		return string(out), err
	}
	type fileSnap struct {
		exists bool
		data   []byte
		isDir  bool
	}
	snapshot := func() map[string]fileSnap {
		t.Helper()
		snap := map[string]fileSnap{}
		for _, path := range []string{PIDFile(root), StateFile(root)} {
			fi, err := os.Lstat(path)
			if err != nil {
				snap[path] = fileSnap{exists: false}
				continue
			}
			s := fileSnap{exists: true, isDir: fi.IsDir()}
			if !s.isDir {
				s.data, _ = os.ReadFile(path)
			}
			snap[path] = s
		}
		return snap
	}
	filesPreserved := func(label string, before map[string]fileSnap) {
		t.Helper()
		after := snapshot()
		for k, b := range before {
			a := after[k]
			if b.exists != a.exists || b.isDir != a.isDir || string(b.data) != string(a.data) {
				t.Fatalf("%s: refusal must leave pid/state byte-unchanged for manual inspection (before=%+v after=%+v)", label, b, a)
			}
		}
	}
	mustRefuse := func(label, wantMsg string) {
		t.Helper()
		before := snapshot()
		out, err := runCmd("wormhole", "stop")
		if err == nil {
			t.Fatalf("%s: stop must refuse, but succeeded:\n%s", label, out)
		}
		if !strings.Contains(out, wantMsg) {
			t.Fatalf("%s: stop refusal should mention %q, got: %s", label, wantMsg, out)
		}
		if !isProcessAlive(foreignPID) {
			t.Fatalf("%s: foreign process was killed", label)
		}
		filesPreserved(label, before)
	}
	mustStartRefuse := func(label string) {
		t.Helper()
		before := snapshot()
		out, err := runCmd("wormhole", "start")
		if err == nil {
			t.Fatalf("%s: start must refuse, but succeeded:\n%s", label, out)
		}
		if !strings.Contains(out, "refusing to act") {
			t.Fatalf("%s: start refusal should explain the refusal, got: %s", label, out)
		}
		if !isProcessAlive(foreignPID) {
			t.Fatalf("%s: foreign process was killed by start", label)
		}
		filesPreserved(label, before)
	}

	// Missing state file while the pid file records a live process.
	if err := EnsureDir(root); err != nil {
		t.Fatal(err)
	}
	if err := writePIDFile(PIDFile(root), foreignPID); err != nil {
		t.Fatal(err)
	}
	mustRefuse("missing state file", "state file missing or corrupt")

	// Corrupt (non-JSON) state file.
	if err := os.WriteFile(StateFile(root), []byte("not json at all"), 0o644); err != nil {
		t.Fatal(err)
	}
	mustRefuse("corrupt state file", "state file missing or corrupt")

	// State file recording a different workspace root (cross-workspace PID
	// reuse): pid and state agree, but the files do not belong here.
	if err := writeStateFile(StateFile(root), daemonState{
		Status:  "running",
		Started: time.Now().UTC().Format(time.RFC3339),
		PID:     foreignPID,
		Root:    "/somewhere/else",
		Running: true,
	}); err != nil {
		t.Fatal(err)
	}
	mustRefuse("foreign root in state file", "refusing to act")

	// Start must refuse on the same unverifiable files — never overwrite
	// them, never signal the recorded PID.
	mustStartRefuse("foreign root in state file")

	// Status surfaces the refusal.
	out, _ := runCmd("wormhole", "status")
	if !strings.Contains(out, "refusing to act") {
		t.Fatalf("status should surface the refusal, got: %s", out)
	}

	// A present but unusable pid file must never be read as "no daemon":
	// stop refuses to signal, start refuses to spawn a second worker over
	// the recorded one, status surfaces the refusal, and the pid/state files
	// are left byte-unchanged in place for manual inspection.
	writeBadPID := func(content string) {
		t.Helper()
		if err := os.WriteFile(PIDFile(root), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(StateFile(root)); err != nil && !os.IsNotExist(err) {
			t.Fatal(err)
		}
	}
	for label, content := range map[string]string{
		"unparseable pid file": "not-a-pid",
		"empty pid file":       "",
		"zero pid file":        "0",
		"negative pid file":    "-5",
	} {
		writeBadPID(content)
		mustRefuse(label, "refusing to act")
		mustStartRefuse(label)
		out, _ := runCmd("wormhole", "status")
		if !strings.Contains(out, "refusing to act") {
			t.Fatalf("%s: status should surface the refusal, got: %s", label, out)
		}
	}

	// A pid file path that cannot be read (here: a directory sits in its
	// place) is refused the same way; the path stays byte/type-unchanged.
	if err := os.Remove(PIDFile(root)); err != nil {
		t.Fatal(err)
	}
	if err := os.Mkdir(PIDFile(root), 0o755); err != nil {
		t.Fatal(err)
	}
	mustRefuse("unreadable pid file", "refusing to act")

	// A genuinely absent pid file means no registered instance: stop
	// reports "not running" and signals nothing.
	if err := os.Remove(PIDFile(root)); err != nil {
		t.Fatal(err)
	}
	out, err := runCmd("wormhole", "stop")
	if err == nil || !strings.Contains(out, "not running") {
		t.Fatalf("stop with missing pid file should report not running, got: %v\n%s", err, out)
	}
	if !isProcessAlive(foreignPID) {
		t.Fatal("foreign process was killed")
	}
}

// repoRoot locates the starfleetctl repository root by walking up from the
// current working directory until a go.mod is found. go test always runs with
// the package source directory as cwd, so this works from any checkout.
func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatalf("getwd: %v", err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("could not locate repository root (go.mod) walking up from working dir")
		}
		dir = parent
	}
}

// buildTestBinary builds the starfleetctl binary for testing and returns its path.
func buildTestBinary(t *testing.T) string {
	t.Helper()
	buildDir := t.TempDir()
	binary := filepath.Join(buildDir, "starfleetctl-test")

	cmd := exec.Command("go", "build", "-o", binary, "./cmd/starfleetctl")
	cmd.Dir = repoRoot(t)
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("build failed: %v\n%s", err, out)
	}
	return binary
}

// TestGitignoreWormholes verifies that the repository's own .gitignore
// ignores the wormhole runtime directory (pid, state, relay spool) at the
// path the daemon actually uses: .starfleet-ai/var/wormholes/.
func TestGitignoreWormholes(t *testing.T) {
	base := t.TempDir()

	// Initialize git repo
	cmd := exec.Command("git", "init")
	cmd.Dir = base
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init failed: %v\n%s", err, out)
	}

	// Copy the repository's own .gitignore (the file shipped with starfleetctl)
	destGitignore := filepath.Join(base, ".gitignore")
	if err := copyFile(filepath.Join(repoRoot(t), ".gitignore"), destGitignore); err != nil {
		t.Fatalf("copy .gitignore failed: %v", err)
	}

	// Create the wormhole runtime files at the path WorkDir() actually uses
	wormholeDir := filepath.Join(base, ".starfleet-ai", "var", "wormholes")
	if err := os.MkdirAll(wormholeDir, 0o755); err != nil {
		t.Fatalf("mkdir wormhole dir: %v", err)
	}

	testFiles := []string{
		filepath.Join(wormholeDir, "pid"),
		filepath.Join(wormholeDir, "state"),
		filepath.Join(wormholeDir, "relay", "mail", "alpha", "new", "test.md"),
	}
	for _, f := range testFiles {
		if err := os.MkdirAll(filepath.Dir(f), 0o755); err != nil {
			t.Fatalf("mkdir for %s: %v", f, err)
		}
		if err := os.WriteFile(f, []byte("test"), 0o644); err != nil {
			t.Fatalf("write %s: %v", f, err)
		}
	}

	// A tracked workspace file that must NOT be ignored
	confDir := filepath.Join(base, ".starfleet-ai", "conf")
	if err := os.MkdirAll(confDir, 0o755); err != nil {
		t.Fatalf("mkdir conf: %v", err)
	}
	confFile := filepath.Join(confDir, "wormholes.yaml")
	if err := os.WriteFile(confFile, []byte("wormholes:\n  enabled: true\n"), 0o644); err != nil {
		t.Fatalf("write conf: %v", err)
	}

	// git check-ignore -v must report every runtime file as ignored and name
	// the responsible pattern from the copied .gitignore.
	for _, f := range testFiles {
		cmd := exec.Command("git", "check-ignore", "-v", f)
		cmd.Dir = base
		out, err := cmd.CombinedOutput()
		if err != nil {
			t.Errorf("file %s should be ignored by git but is not: %v\n%s", f, err, out)
			continue
		}
		if !strings.Contains(string(out), ".starfleet-ai/var/wormholes/") {
			t.Errorf("ignore pattern for %s should match .starfleet-ai/var/wormholes/, got: %s", f, out)
		}
	}

	// The config file must not be ignored (check-ignore exits 1 for not-ignored)
	cmd = exec.Command("git", "check-ignore", confFile)
	cmd.Dir = base
	if out, err := cmd.CombinedOutput(); err == nil {
		t.Errorf("config file %s should NOT be ignored but is:\n%s", confFile, out)
	}
}

func copyFile(src, dst string) error {
	data, err := os.ReadFile(src)
	if err != nil {
		return err
	}
	return os.WriteFile(dst, data, 0o644)
}

// TestSendCLIPublishesMessage proves the send CLI command (W0-026.3) end-to-end:
// it builds the current starfleetctl binary, invokes the real CLI entrypoint
// `starfleetctl wormhole send` against a local bare remote, and asserts the
// message is pushed to `mail/<recipient>/new/` with sender/recipient/payload
// preserved. Meaningful error cases are covered in a subtest.
func TestSendCLIPublishesMessage(t *testing.T) {
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	writeConfig(t, root, cfg)

	binary := buildTestBinary(t)

	runSend := func(envShip string, args ...string) (string, error) {
		allArgs := append([]string{"wormhole", "send"}, args...)
		cmd := exec.Command(binary, allArgs...)
		cmd.Dir = root
		cmd.Env = daemonTestEnv(root, envShip)
		out, err := cmd.CombinedOutput()
		return string(out), err
	}

	out, err := runSend("alpha", "--to", "beta", "--text", "hello from send CLI")
	if err != nil {
		t.Fatalf("wormhole send failed: %v\n%s", err, out)
	}
	if !strings.Contains(out, "Wormhole message published") {
		t.Fatalf("send should report publication, got: %s", out)
	}
	if !strings.Contains(out, "to=beta") {
		t.Fatalf("send output should report recipient, got: %s", out)
	}

	list := runGit(t, remote, "ls-tree", "-r", "--name-only", "main")
	var msgPath string
	for _, line := range strings.Split(list, "\n") {
		if strings.HasPrefix(line, "mail/beta/new/") && strings.HasSuffix(line, ".md") {
			msgPath = line
			break
		}
	}
	if msgPath == "" {
		t.Fatalf("remote must contain a published message for beta/new, got:\n%s", list)
	}
	content := remoteRead(t, remote, msgPath)
	if !strings.Contains(content, "from: alpha") {
		t.Fatalf("published message must preserve sender, got:\n%s", content)
	}
	if !strings.Contains(content, "to: beta") {
		t.Fatalf("published message must preserve recipient, got:\n%s", content)
	}
	if !strings.Contains(content, "hello from send CLI") {
		t.Fatalf("published message must preserve payload, got:\n%s", content)
	}

	// Error cases: the CLI must reject missing recipient and missing body with
	// a non-zero exit and must not publish anything to the remote.
	t.Run("errors", func(t *testing.T) {
		before := runGit(t, remote, "ls-tree", "-r", "--name-only", "main")

		out, err := runSend("alpha", "--text", "no recipient")
		if err == nil {
			t.Fatalf("send without --to must fail, got success: %s", out)
		}
		if !strings.Contains(out, "--to is required") {
			t.Fatalf("send without --to should report missing recipient, got: %s", out)
		}

		out, err = runSend("alpha", "--to", "beta")
		if err == nil {
			t.Fatalf("send without body must fail, got success: %s", out)
		}
		if !strings.Contains(out, "--text or --stdin is required") {
			t.Fatalf("send without body should report missing content, got: %s", out)
		}

		after := runGit(t, remote, "ls-tree", "-r", "--name-only", "main")
		if after != before {
			t.Fatalf("failed send invocations must not publish to the remote\nbefore:\n%s\nafter:\n%s", before, after)
		}
	})
}

// TestGitCommandEnvForcesCLocale proves git invocations are executed with a
// deterministic C locale (GT-7): a pre-existing LC_ALL in the environment must
// not leak duplicate entries (glibc resolves the first), and the child git
// process must report English diagnostics regardless of the host locale.
func TestGitCommandEnvForcesCLocale(t *testing.T) {
	t.Setenv("LC_ALL", "de_DE.UTF-8")
	env := gitCommandEnv("GIT_INDEX_FILE=/tmp/test-index")
	var lcAll []string
	for _, kv := range env {
		if strings.HasPrefix(kv, "LC_ALL=") {
			lcAll = append(lcAll, kv)
		}
	}
	if len(lcAll) != 1 || lcAll[0] != "LC_ALL=C" {
		t.Fatalf("git env must contain exactly one LC_ALL=C, got %v", lcAll)
	}
	var havePrompt, haveIndex bool
	for _, kv := range env {
		if kv == "GIT_TERMINAL_PROMPT=0" {
			havePrompt = true
		}
		if kv == "GIT_INDEX_FILE=/tmp/test-index" {
			haveIndex = true
		}
	}
	if !havePrompt {
		t.Fatal("git env must disable terminal prompts")
	}
	if !haveIndex {
		t.Fatal("git env must preserve extra entries")
	}

	// Behavioral proof on a real git invocation: despite ambient LC_ALL=de_DE,
	// the child must emit English diagnostics (locale-independent parsing).
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	writeConfig(t, root, cfg)
	cloneTimeout, commandTimeout := testTimeouts(cfg)
	sp := newSpool(root, cfg.Repositories[0], 0, "alpha", cloneTimeout, commandTimeout)
	if err := sp.Publish(NewMessage("alpha", "beta", "info", "locale-seed", "")); err != nil {
		t.Fatalf("publish seed: %v", err)
	}
	out, err := sp.git.gitLocked("rev-parse", "bogus-ref-xyz-locale")
	if err == nil {
		t.Fatal("rev-parse of a bogus ref must fail")
	}
	if !strings.Contains(string(out), "ambiguous argument") {
		t.Fatalf("git error must be emitted in C locale (English), got: %q (err: %v)", out, err)
	}
}

func TestEmptyMessageRejectedMovedToFailed(t *testing.T) {
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	writeConfig(t, root, cfg)
	t.Setenv("STARFLEET_SHIP_ID", "alpha")

	bus, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}
	cloneTimeout, commandTimeout := testTimeouts(cfg)
	sp := newSpool(root, cfg.Repositories[0], cfg.Sync.PushRetries, "alpha", cloneTimeout, commandTimeout)
	if err := sp.runOnce(nil, false); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	msg := NewMessage("beta", "alpha", "ship", "", "")
	msg.Content = ""
	if err := validateMessage(msg); err == nil {
		t.Fatal("expected empty message to be rejected by validation")
	}

	dir := filepath.Join(sp.git.CloneDir(), "mail", "alpha", "new")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, msg.ID+".md"), buildMessageBytes(msg), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := processPendingMessages(root); err != nil {
		t.Fatalf("process: %v", err)
	}

	failedDir := filepath.Join(sp.git.CloneDir(), "mail", "alpha", "failed")
	if _, err := os.Stat(filepath.Join(failedDir, msg.ID+".md")); err != nil {
		t.Fatalf("empty message not moved to failed/: %v", err)
	}
	if _, err := os.Stat(filepath.Join(failedDir, msg.ID+".md.reason")); err != nil {
		t.Fatalf("empty message failure diagnostic missing: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sp.git.CloneDir(), "mail", "alpha", "cur", msg.ID+".md")); !os.IsNotExist(err) {
		t.Fatal("empty message must not remain in cur/")
	}
	if n := countFiles(t, bus.MsgDir); n != 0 {
		t.Fatalf("empty message must not be imported, got %d comms messages", n)
	}
}

func TestPermanentPostExternalErrorMovesToFailed(t *testing.T) {
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	writeConfig(t, root, cfg)
	t.Setenv("STARFLEET_SHIP_ID", "alpha")

	bus, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}
	cloneTimeout, commandTimeout := testTimeouts(cfg)
	sp := newSpool(root, cfg.Repositories[0], cfg.Sync.PushRetries, "alpha", cloneTimeout, commandTimeout)
	if err := sp.runOnce(nil, false); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	msg := NewMessage("beta", "alpha", "ship", "first payload", "")
	// Pre-create an external record with same source+externalID but different payload
	// so PostExternal returns a permanent hash-mismatch error.
	if _, err := bus.PostExternal(sp.repo.ID, msg.ID, "beta", "alpha", "ship", "", "different payload"); err != nil {
		t.Fatalf("seed conflicting external record: %v", err)
	}

	dir := filepath.Join(sp.git.CloneDir(), "mail", "alpha", "new")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, msg.ID+".md"), buildMessageBytes(msg), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := processPendingMessages(root); err != nil {
		t.Fatalf("process: %v", err)
	}

	failedDir := filepath.Join(sp.git.CloneDir(), "mail", "alpha", "failed")
	if _, err := os.Stat(filepath.Join(failedDir, msg.ID+".md")); err != nil {
		t.Fatalf("permanent PostExternal error not moved to failed/: %v", err)
	}
	reason, err := os.ReadFile(filepath.Join(failedDir, msg.ID+".md.reason"))
	if err != nil {
		t.Fatalf("diagnostic missing: %v", err)
	}
	if !strings.Contains(string(reason), "hash mismatch") {
		t.Fatalf("diagnostic must be traceable to permanent reason, got: %s", reason)
	}
	if _, err := os.Stat(filepath.Join(sp.git.CloneDir(), "mail", "alpha", "cur", msg.ID+".md")); !os.IsNotExist(err) {
		t.Fatal("permanent PostExternal error must not remain in cur/")
	}
	// Seeded external record produced the only comms copy.
	if n := countFiles(t, bus.MsgDir); n != 1 {
		t.Fatalf("expected only seeded comms message, got %d", n)
	}
}

func TestTemporaryPostExternalErrorKeepsMessageInCur(t *testing.T) {
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	writeConfig(t, root, cfg)
	t.Setenv("STARFLEET_SHIP_ID", "alpha")

	cloneTimeout, commandTimeout := testTimeouts(cfg)
	sp := newSpool(root, cfg.Repositories[0], cfg.Sync.PushRetries, "alpha", cloneTimeout, commandTimeout)
	if err := sp.runOnce(nil, false); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	msg := NewMessage("beta", "alpha", "ship", "hello", "")
	dir := filepath.Join(sp.git.CloneDir(), "mail", "alpha", "new")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, msg.ID+".md"), buildMessageBytes(msg), 0o644); err != nil {
		t.Fatal(err)
	}

	// A bus whose lock path cannot be created forces a transient import error.
	badBusDir := filepath.Join(root, "badbus")
	if err := os.WriteFile(badBusDir, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	badBus := &comms.Bus{BusDir: badBusDir}

	if err := sp.runOnce(badBus, true); err == nil {
		t.Fatal("expected transient import error")
	}

	cur := filepath.Join(sp.git.CloneDir(), "mail", "alpha", "cur", msg.ID+".md")
	if _, err := os.Stat(cur); err != nil {
		t.Fatalf("temporary PostExternal error must remain in cur/: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sp.git.CloneDir(), "mail", "alpha", "failed", msg.ID+".md")); !os.IsNotExist(err) {
		t.Fatal("temporary PostExternal error must not move to failed/")
	}
	if _, err := os.Stat(filepath.Join(sp.git.CloneDir(), "mail", "alpha", "processed", msg.ID+".md")); !os.IsNotExist(err) {
		t.Fatal("message must not be marked processed after temporary import failure")
	}
}

func TestSuccessfulImportUnchanged(t *testing.T) {
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	writeConfig(t, root, cfg)
	t.Setenv("STARFLEET_SHIP_ID", "alpha")

	bus, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}
	cloneTimeout, commandTimeout := testTimeouts(cfg)
	sp := newSpool(root, cfg.Repositories[0], cfg.Sync.PushRetries, "beta", cloneTimeout, commandTimeout)
	msg := NewMessage("beta", "alpha", "ship", "unchanged success", "")
	if err := sp.Publish(msg); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := processPendingMessages(root); err != nil {
		t.Fatalf("process: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sp.git.CloneDir(), "mail", "alpha", "processed", msg.ID+".md")); err != nil {
		t.Fatalf("successful import must move to processed/: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sp.git.CloneDir(), "mail", "alpha", "cur", msg.ID+".md")); !os.IsNotExist(err) {
		t.Fatal("successful import must not remain in cur/")
	}
	if _, err := os.Stat(filepath.Join(sp.git.CloneDir(), "mail", "alpha", "failed", msg.ID+".md")); !os.IsNotExist(err) {
		t.Fatal("successful import must not move to failed/")
	}
	if n := countFiles(t, bus.MsgDir); n != 1 {
		t.Fatalf("expected exactly 1 comms message after successful import, got %d", n)
	}
}

func TestPermanentImportMoveToFailedErrorDoesNotLoseMessage(t *testing.T) {
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	writeConfig(t, root, cfg)
	t.Setenv("STARFLEET_SHIP_ID", "alpha")

	bus, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}
	cloneTimeout, commandTimeout := testTimeouts(cfg)
	sp := newSpool(root, cfg.Repositories[0], cfg.Sync.PushRetries, "alpha", cloneTimeout, commandTimeout)
	if err := sp.runOnce(nil, false); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	msg := NewMessage("beta", "alpha", "ship", "payload", "")
	// Trigger permanent PostExternal error (hash mismatch).
	if _, err := bus.PostExternal(sp.repo.ID, msg.ID, "beta", "alpha", "ship", "", "different payload"); err != nil {
		t.Fatalf("seed conflicting external record: %v", err)
	}

	dir := filepath.Join(sp.git.CloneDir(), "mail", "alpha", "cur")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	curPath := filepath.Join(dir, msg.ID+".md")
	if err := os.WriteFile(curPath, buildMessageBytes(msg), 0o644); err != nil {
		t.Fatal(err)
	}
	// Force failLocked move to fail by making failed/ path a file (not a dir).
	failedPath := filepath.Join(sp.git.CloneDir(), "mail", "alpha", "failed")
	if err := os.WriteFile(failedPath, []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}

	err = sp.runOnce(bus, true)
	if err == nil {
		t.Fatal("expected error when moving permanent rejection to failed/ fails")
	}

	// Message must not be lost: it remains in cur/ because move to failed/ failed.
	if _, statErr := os.Stat(curPath); statErr != nil {
		t.Fatalf("message lost: expected it to remain in cur/, got err=%v (run err=%v)", statErr, err)
	}
	if _, statErr := os.Stat(filepath.Join(sp.git.CloneDir(), "mail", "alpha", "processed", msg.ID+".md")); !os.IsNotExist(statErr) {
		t.Fatal("message must not be marked processed")
	}
}

func TestCorruptExternalRecordMovesToFailed(t *testing.T) {
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	writeConfig(t, root, cfg)
	t.Setenv("STARFLEET_SHIP_ID", "alpha")

	bus, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}
	cloneTimeout, commandTimeout := testTimeouts(cfg)
	sp := newSpool(root, cfg.Repositories[0], cfg.Sync.PushRetries, "alpha", cloneTimeout, commandTimeout)
	if err := sp.runOnce(nil, false); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	msg := NewMessage("beta", "alpha", "ship", "payload", "")
	// Corrupt the external record for this source+externalID so PostExternal fails permanently.
	sum := sha256.Sum256([]byte(sp.repo.ID + "\x00" + msg.ID))
	recPath := filepath.Join(bus.BusDir, "external", hex.EncodeToString(sum[:])+".json")
	if err := os.MkdirAll(filepath.Dir(recPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recPath, []byte("{not-json"), 0o644); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(sp.git.CloneDir(), "mail", "alpha", "new")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, msg.ID+".md"), buildMessageBytes(msg), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := processPendingMessages(root); err != nil {
		t.Fatalf("process: %v", err)
	}

	failedDir := filepath.Join(sp.git.CloneDir(), "mail", "alpha", "failed")
	if _, err := os.Stat(filepath.Join(failedDir, msg.ID+".md")); err != nil {
		t.Fatalf("corrupt external record not moved to failed/: %v", err)
	}
	reason, err := os.ReadFile(filepath.Join(failedDir, msg.ID+".md.reason"))
	if err != nil {
		t.Fatalf("diagnostic missing: %v", err)
	}
	if !strings.Contains(string(reason), "external record corrupt") {
		t.Fatalf("diagnostic must be traceable, got: %s", reason)
	}
	if _, err := os.Stat(filepath.Join(sp.git.CloneDir(), "mail", "alpha", "cur", msg.ID+".md")); !os.IsNotExist(err) {
		t.Fatal("must not remain in cur/")
	}
}

func TestExternalRecordKeyMismatchMovesToFailed(t *testing.T) {
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	writeConfig(t, root, cfg)
	t.Setenv("STARFLEET_SHIP_ID", "alpha")

	bus, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}
	cloneTimeout, commandTimeout := testTimeouts(cfg)
	sp := newSpool(root, cfg.Repositories[0], cfg.Sync.PushRetries, "alpha", cloneTimeout, commandTimeout)
	if err := sp.runOnce(nil, false); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	msg := NewMessage("beta", "alpha", "ship", "payload", "")
	sum := sha256.Sum256([]byte(sp.repo.ID + "\x00" + msg.ID))
	recPath := filepath.Join(bus.BusDir, "external", hex.EncodeToString(sum[:])+".json")
	if err := os.MkdirAll(filepath.Dir(recPath), 0o755); err != nil {
		t.Fatal(err)
	}
	mismatched := `{"version":1,"source":"other-source","external_id":"other-id","payload_sha256":"x","payload":"x","from":"beta","target":"alpha","comms_id":"m9999","created_at":"2026-01-01T00:00:00Z","state":"complete"}`
	if err := os.WriteFile(recPath, []byte(mismatched), 0o644); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(sp.git.CloneDir(), "mail", "alpha", "new")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, msg.ID+".md"), buildMessageBytes(msg), 0o644); err != nil {
		t.Fatal(err)
	}

	if err := processPendingMessages(root); err != nil {
		t.Fatalf("process: %v", err)
	}

	failedDir := filepath.Join(sp.git.CloneDir(), "mail", "alpha", "failed")
	if _, err := os.Stat(filepath.Join(failedDir, msg.ID+".md")); err != nil {
		t.Fatalf("key mismatch not moved to failed/: %v", err)
	}
	reason, err := os.ReadFile(filepath.Join(failedDir, msg.ID+".md.reason"))
	if err != nil {
		t.Fatalf("diagnostic missing: %v", err)
	}
	if !strings.Contains(string(reason), "key mismatch") {
		t.Fatalf("diagnostic must be traceable, got: %s", reason)
	}
	if _, err := os.Stat(filepath.Join(sp.git.CloneDir(), "mail", "alpha", "cur", msg.ID+".md")); !os.IsNotExist(err) {
		t.Fatal("must not remain in cur/")
	}
}

func TestRecipientCopyMismatchMovesToFailed(t *testing.T) {
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	writeConfig(t, root, cfg)
	t.Setenv("STARFLEET_SHIP_ID", "alpha")

	bus, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}
	cloneTimeout, commandTimeout := testTimeouts(cfg)
	sp := newSpool(root, cfg.Repositories[0], cfg.Sync.PushRetries, "alpha", cloneTimeout, commandTimeout)
	if err := sp.runOnce(nil, false); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	msg := NewMessage("beta", "alpha", "ship", "payload", "")
	data := buildMessageBytes(msg)
	hash := sha256.Sum256(data)

	// External record exists in posting with pending recipient, but recipient copy
	// file on disk differs -> PostExternal returns permanent recipient mismatch.
	sum := sha256.Sum256([]byte(sp.repo.ID + "\x00" + msg.ID))
	recPath := filepath.Join(bus.BusDir, "external", hex.EncodeToString(sum[:])+".json")
	recJSON := fmt.Sprintf(`{"version":1,"source":%q,"external_id":%q,"payload_sha256":%q,"payload":%q,"from":"beta","target":"alpha","type":"ship","comms_id":"m0999","created_at":"2026-01-01T00:00:00Z","state":"posting","recipients":[{"ship":"alpha","state":"pending"}]}`,
		sp.repo.ID, msg.ID, hex.EncodeToString(hash[:]), string(data))
	if err := os.MkdirAll(filepath.Dir(recPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(recPath, []byte(recJSON), 0o644); err != nil {
		t.Fatal(err)
	}
	msgPath := filepath.Join(bus.MsgDir, "alpha", "unseen", "m0999.json")
	if err := os.MkdirAll(filepath.Dir(msgPath), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(msgPath, []byte(`{"id":"m0999","text":"tampered"}`), 0o644); err != nil {
		t.Fatal(err)
	}

	dir := filepath.Join(sp.git.CloneDir(), "mail", "alpha", "new")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, msg.ID+".md"), data, 0o644); err != nil {
		t.Fatal(err)
	}

	if err := processPendingMessages(root); err != nil {
		t.Fatalf("process: %v", err)
	}

	failedDir := filepath.Join(sp.git.CloneDir(), "mail", "alpha", "failed")
	if _, err := os.Stat(filepath.Join(failedDir, msg.ID+".md")); err != nil {
		t.Fatalf("recipient copy mismatch not moved to failed/: %v", err)
	}
	reason, err := os.ReadFile(filepath.Join(failedDir, msg.ID+".md.reason"))
	if err != nil {
		t.Fatalf("diagnostic missing: %v", err)
	}
	if !strings.Contains(string(reason), "recipient copy mismatch") {
		t.Fatalf("diagnostic must be traceable, got: %s", reason)
	}
	if _, err := os.Stat(filepath.Join(sp.git.CloneDir(), "mail", "alpha", "cur", msg.ID+".md")); !os.IsNotExist(err) {
		t.Fatal("must not remain in cur/")
	}
}

// ---- central multi-ship incoming processing --------------------------------

// registerShipReservation creates a ship-name registry reservation using the
// same on-disk location and format as the shipnames registry.
func registerShipReservation(t *testing.T, root, name string) {
	t.Helper()
	bus, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}
	dir := filepath.Join(bus.BusDir, "ships")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, name), []byte("0:0:/dev/null\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeFleetShips writes a fleet.yaml carrying the flagship and declarative
// standing ships.
func writeFleetShips(t *testing.T, root, flagship string, ships ...string) {
	t.Helper()
	dir := filepath.Join(root, ".starfleet-ai", "conf")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	var b strings.Builder
	b.WriteString("fleet:\n")
	if flagship != "" {
		fmt.Fprintf(&b, "  flagship: %q\n", flagship)
	}
	if len(ships) > 0 {
		b.WriteString("  ships:\n")
		for _, s := range ships {
			fmt.Fprintf(&b, "    - name: %q\n", s)
			b.WriteString("      model: \"nim-primary\"\n")
			b.WriteString("      client: opencode\n")
			b.WriteString("      class: worker\n")
			b.WriteString("      launch_type: background\n")
			b.WriteString("      respawn: true\n")
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "fleet.yaml"), []byte(b.String()), 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeBoardStatus writes a comms status record for agent, representing a
// currently running ship on the board.
func writeBoardStatus(t *testing.T, root, agent string) {
	t.Helper()
	bus, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}
	rec := comms.StatusRecord{
		Epoch:  time.Now().Unix(),
		ISO:    time.Now().UTC().Format(time.RFC3339),
		Agent:  agent,
		State:  "idle",
		Handle: agent,
	}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bus.StatusDir, agent+".json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// setupCentralTest prepares a workspace with one wormhole repository, an
// initial sync, and no STARFLEET_SHIP_ID (the central daemon model).
func setupCentralTest(t *testing.T) (root string, sp *Spool) {
	t.Helper()
	remote := setupRemote(t)
	root = t.TempDir()
	cfg := testConfig(remote)
	writeConfig(t, root, cfg)
	t.Setenv("STARFLEET_SHIP_ID", "")
	// Keep the bus inside the test workspace. It must stay the default bus
	// dir: the ship-name registry lives at config.BusDir(root)/ships and does
	// not follow STARFLEET_BUS_DIR.
	t.Setenv("STARFLEET_BUS_DIR", config.BusDir(root))
	cloneTimeout, commandTimeout := testTimeouts(cfg)
	sp = newSpool(root, cfg.Repositories[0], cfg.Sync.PushRetries, "", cloneTimeout, commandTimeout)
	if err := sp.runOnce(nil, false); err != nil {
		t.Fatalf("initial sync: %v", err)
	}
	return root, sp
}

// TestCentralProcessingFlagshipAndWorkers proves that one cycle processes the
// mailboxes of the flagship, a declarative standing ship, a reserved worker
// and a worker known only from the board.
func TestCentralProcessingFlagshipAndWorkers(t *testing.T) {
	root, sp := setupCentralTest(t)

	writeFleetShips(t, root, "Enterprise", "Laforge")
	registerShipReservation(t, root, "Discovery")
	writeBoardStatus(t, root, "Voyager")

	ships := []string{"Enterprise", "Laforge", "Discovery", "Voyager"}
	for _, ship := range ships {
		if err := sp.Publish(NewMessage("Sender", ship, "ship", "hello "+ship, "")); err != nil {
			t.Fatalf("publish to %s: %v", ship, err)
		}
	}

	if err := processPendingMessages(root); err != nil {
		t.Fatalf("process: %v", err)
	}

	bus, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if n := countFiles(t, bus.MsgDir); n != len(ships) {
		t.Fatalf("expected %d imported messages, got %d", len(ships), n)
	}
	for _, ship := range ships {
		entries, err := os.ReadDir(filepath.Join(sp.git.CloneDir(), "mail", ship, "processed"))
		if err != nil {
			t.Fatalf("read processed/%s: %v", ship, err)
		}
		if len(entries) != 1 {
			t.Fatalf("ship %s: expected 1 processed message, got %d", ship, len(entries))
		}
	}
}

// TestCentralProcessingWithoutShipEnv proves that a ship is processed even
// when the daemon process has no STARFLEET_SHIP_ID at all.
func TestCentralProcessingWithoutShipEnv(t *testing.T) {
	root, sp := setupCentralTest(t)
	registerShipReservation(t, root, "Discovery")

	msg := NewMessage("Enterprise", "Discovery", "ship", "no env", "")
	if err := sp.Publish(msg); err != nil {
		t.Fatalf("publish: %v", err)
	}

	if err := processPendingMessages(root); err != nil {
		t.Fatalf("process: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sp.git.CloneDir(), "mail", "Discovery", "processed", msg.ID+".md")); err != nil {
		t.Fatalf("message not imported without STARFLEET_SHIP_ID: %v", err)
	}
}

// TestUserHostFallbackNeverUsedAsMailbox proves that the daemon's user/host
// fallback identity (e.g. "nekrad@x1") never becomes a mailbox, even though
// it is what comms.New falls back to when STARFLEET_SHIP_ID is unset.
func TestUserHostFallbackNeverUsedAsMailbox(t *testing.T) {
	root, sp := setupCentralTest(t)
	registerShipReservation(t, root, "Discovery")

	bus, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}
	fallback := bus.ShipID
	if validToken(fallback) {
		t.Fatalf("test requires a non-token fallback identity, got %q", fallback)
	}

	msg := NewMessage("Enterprise", "Discovery", "ship", "payload", "")
	if err := sp.Publish(msg); err != nil {
		t.Fatalf("publish: %v", err)
	}

	if err := processPendingMessages(root); err != nil {
		t.Fatalf("process: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sp.git.CloneDir(), "mail", fallback)); !os.IsNotExist(err) {
		t.Fatalf("fallback identity %q must never be used as a mailbox (stat err=%v)", fallback, err)
	}
	entries, err := os.ReadDir(filepath.Join(sp.git.CloneDir(), "mail"))
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".") {
			continue // transient message temp dir (mail/.tmp) is not a mailbox
		}
		if !validToken(e.Name()) {
			t.Fatalf("non-ship mailbox entry created: %q", e.Name())
		}
	}
}

// TestCentralProcessingDeduplicatesShipEntries proves that a ship present in
// several canonical sources is processed exactly once.
func TestCentralProcessingDeduplicatesShipEntries(t *testing.T) {
	root, sp := setupCentralTest(t)

	writeFleetShips(t, root, "Enterprise", "Discovery")
	registerShipReservation(t, root, "Discovery")
	writeBoardStatus(t, root, "Discovery")

	ships, err := localShips(root)
	if err != nil {
		t.Fatal(err)
	}
	seen := make(map[string]int)
	for _, s := range ships {
		seen[s]++
	}
	for s, n := range seen {
		if n != 1 {
			t.Fatalf("ship %q appears %d times in localShips", s, n)
		}
	}

	msg := NewMessage("Enterprise", "Discovery", "ship", "dedup", "")
	if err := sp.Publish(msg); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := processPendingMessages(root); err != nil {
		t.Fatalf("process: %v", err)
	}

	bus, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if n := countFiles(t, bus.MsgDir); n != 1 {
		t.Fatalf("expected exactly 1 imported message, got %d", n)
	}
	processed, err := os.ReadDir(filepath.Join(sp.git.CloneDir(), "mail", "Discovery", "processed"))
	if err != nil {
		t.Fatal(err)
	}
	if len(processed) != 1 {
		t.Fatalf("expected 1 processed message, got %d", len(processed))
	}
}

// TestCentralProcessingIsolatesMailboxErrors proves that a broken mailbox does
// not prevent the other mailboxes from being processed, while the error is
// still aggregated and reported.
func TestCentralProcessingIsolatesMailboxErrors(t *testing.T) {
	root, sp := setupCentralTest(t)

	registerShipReservation(t, root, "Discovery")
	registerShipReservation(t, root, "Broken")

	msg := NewMessage("Enterprise", "Discovery", "ship", "survives", "")
	if err := sp.Publish(msg); err != nil {
		t.Fatalf("publish: %v", err)
	}

	// A regular file where the mailbox directory is expected makes the layout
	// creation for this one ship fail.
	if err := os.WriteFile(filepath.Join(sp.git.CloneDir(), "mail", "Broken"), []byte("not a dir"), 0o644); err != nil {
		t.Fatal(err)
	}

	err := processPendingMessages(root)
	if err == nil {
		t.Fatal("expected an aggregated mailbox error")
	}
	if !strings.Contains(err.Error(), "Broken") {
		t.Fatalf("error must identify the failing mailbox: %v", err)
	}
	if _, err := os.Stat(filepath.Join(sp.git.CloneDir(), "mail", "Discovery", "processed", msg.ID+".md")); err != nil {
		t.Fatalf("other mailbox must still be processed: %v", err)
	}
}

// TestWormholeSyncUsesMultiShipLogic proves that `wormhole sync` runs the same
// central multi-ship cycle as the daemon.
func TestWormholeSyncUsesMultiShipLogic(t *testing.T) {
	root, sp := setupCentralTest(t)

	writeFleetShips(t, root, "Enterprise", "Laforge")
	registerShipReservation(t, root, "Discovery")

	ships := []string{"Enterprise", "Laforge", "Discovery"}
	for _, ship := range ships {
		if err := sp.Publish(NewMessage("Sender", ship, "ship", "via sync", "")); err != nil {
			t.Fatalf("publish to %s: %v", ship, err)
		}
	}

	if rc := runSync(root); rc != 0 {
		t.Fatalf("wormhole sync failed: rc=%d", rc)
	}
	bus, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if n := countFiles(t, bus.MsgDir); n != len(ships) {
		t.Fatalf("expected %d imported messages via wormhole sync, got %d", len(ships), n)
	}
}

// setupRemoteWithLegacyArchives creates a remote whose mail/ already carries
// the frozen top-level inbox/ and outbox/ archives.
func setupRemoteWithLegacyArchives(t *testing.T) string {
	t.Helper()
	base := t.TempDir()
	remote := filepath.Join(base, "remote.git")
	runGit(t, "", "init", "--bare", "-b", "main", remote)
	seed := filepath.Join(base, "seed")
	if err := os.MkdirAll(seed, 0o755); err != nil {
		t.Fatal(err)
	}
	runGit(t, seed, "init", "-b", "main")
	if err := os.WriteFile(filepath.Join(seed, "README.md"), []byte("seed\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	legacy := map[string]string{
		"mail/inbox/new/old-inbox.md":   "---\nid: w0-legacy-inbox\nfrom: beta\nto: Discovery\n---\nold inbox\n",
		"mail/outbox/new/old-outbox.md": "---\nid: w0-legacy-outbox\nfrom: Discovery\nto: beta\n---\nold outbox\n",
	}
	for rel, content := range legacy {
		p := filepath.Join(seed, rel)
		if err := os.MkdirAll(filepath.Dir(p), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(p, []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	runGit(t, seed, "add", "-A")
	runGit(t, seed, "commit", "-q", "-m", "seed with legacy archives")
	runGit(t, seed, "remote", "add", "origin", remote)
	runGit(t, seed, "push", "-q", "origin", "main")
	return remote
}

// TestLegacyTopLevelArchivesUntouched proves that the frozen top-level
// inbox/ and outbox/ archives are neither scanned nor imported.
func TestLegacyTopLevelArchivesUntouched(t *testing.T) {
	remote := setupRemoteWithLegacyArchives(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	writeConfig(t, root, cfg)
	t.Setenv("STARFLEET_SHIP_ID", "")
	cloneTimeout, commandTimeout := testTimeouts(cfg)
	sp := newSpool(root, cfg.Repositories[0], cfg.Sync.PushRetries, "", cloneTimeout, commandTimeout)
	if err := sp.runOnce(nil, false); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	registerShipReservation(t, root, "Discovery")
	msg := NewMessage("Enterprise", "Discovery", "ship", "active", "")
	if err := sp.Publish(msg); err != nil {
		t.Fatalf("publish: %v", err)
	}

	if err := processPendingMessages(root); err != nil {
		t.Fatalf("process: %v", err)
	}

	for _, rel := range []string{"mail/inbox/new/old-inbox.md", "mail/outbox/new/old-outbox.md"} {
		if _, err := os.Stat(filepath.Join(sp.git.CloneDir(), rel)); err != nil {
			t.Fatalf("legacy archive file must stay untouched: %v (%s)", err, rel)
		}
	}
	bus, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if n := countFiles(t, bus.MsgDir); n != 1 {
		t.Fatalf("legacy archive must not be imported, got %d comms messages", n)
	}
}

// TestCentralProcessingNoRedelivery proves that a second cycle does not import
// or move an already processed message again.
func TestCentralProcessingNoRedelivery(t *testing.T) {
	root, sp := setupCentralTest(t)
	registerShipReservation(t, root, "Discovery")

	msg := NewMessage("Enterprise", "Discovery", "ship", "once", "")
	if err := sp.Publish(msg); err != nil {
		t.Fatalf("publish: %v", err)
	}

	bus, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if err := processPendingMessages(root); err != nil {
		t.Fatalf("first process: %v", err)
	}
	if n := countFiles(t, bus.MsgDir); n != 1 {
		t.Fatalf("expected 1 imported message, got %d", n)
	}
	if err := processPendingMessages(root); err != nil {
		t.Fatalf("second process: %v", err)
	}
	if n := countFiles(t, bus.MsgDir); n != 1 {
		t.Fatalf("already processed message re-delivered: %d comms messages", n)
	}
	processed, err := os.ReadDir(filepath.Join(sp.git.CloneDir(), "mail", "Discovery", "processed"))
	if err != nil {
		t.Fatal(err)
	}
	if len(processed) != 1 {
		t.Fatalf("expected 1 processed message, got %d", len(processed))
	}
}

// TestCentralProcessingBaselineMultiShip proves that incoming.initial_scan:
// baseline establishes the baseline per ship without importing pre-existing
// messages, also when several ships are processed in one cycle.
func TestCentralProcessingBaselineMultiShip(t *testing.T) {
	remote := setupRemote(t)
	root := t.TempDir()
	cfg := testConfig(remote)
	cfg.Repositories[0].Incoming.InitialScan = "baseline"
	writeConfig(t, root, cfg)
	t.Setenv("STARFLEET_SHIP_ID", "")
	cloneTimeout, commandTimeout := testTimeouts(cfg)
	sp := newSpool(root, cfg.Repositories[0], cfg.Sync.PushRetries, "", cloneTimeout, commandTimeout)
	if err := sp.runOnce(nil, false); err != nil {
		t.Fatalf("initial sync: %v", err)
	}

	registerShipReservation(t, root, "Discovery")
	registerShipReservation(t, root, "Voyager")
	for _, ship := range []string{"Discovery", "Voyager"} {
		if err := sp.Publish(NewMessage("Enterprise", ship, "ship", "pre-existing", "")); err != nil {
			t.Fatalf("publish to %s: %v", ship, err)
		}
	}

	if err := processPendingMessages(root); err != nil {
		t.Fatalf("process: %v", err)
	}

	bus, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if n := countFiles(t, bus.MsgDir); n != 0 {
		t.Fatalf("baseline must not import pre-existing messages, got %d", n)
	}
	for _, ship := range []string{"Discovery", "Voyager"} {
		entries, err := os.ReadDir(filepath.Join(sp.git.CloneDir(), "mail", ship, "processed"))
		if err != nil {
			t.Fatalf("read processed/%s: %v", ship, err)
		}
		if len(entries) != 1 {
			t.Fatalf("ship %s: expected 1 baselined message, got %d", ship, len(entries))
		}
	}
}

// ---- regression: ended ships and legacy initial-scan marker ----------------

// deadPID returns the PID of a process that has already exited.
func deadPID(t *testing.T) int {
	t.Helper()
	for i := 0; i < 20; i++ {
		cmd := exec.Command("true")
		if err := cmd.Run(); err != nil {
			t.Fatalf("run true: %v", err)
		}
		pid := cmd.Process.Pid
		if err := syscall.Kill(pid, 0); errors.Is(err, syscall.ESRCH) {
			return pid
		}
	}
	t.Fatal("no dead pid found")
	return 0
}

// writeBoardStatusWithPID writes a comms status record with an explicit state
// and PID — the lifecycle information the board carries.
func writeBoardStatusWithPID(t *testing.T, root, agent, state string, pid int) {
	t.Helper()
	bus, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}
	rec := comms.StatusRecord{
		Epoch:  time.Now().Unix(),
		ISO:    time.Now().UTC().Format(time.RFC3339),
		Agent:  agent,
		State:  state,
		PID:    pid,
		Handle: agent,
	}
	data, err := json.Marshal(rec)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bus.StatusDir, agent+".json"), data, 0o644); err != nil {
		t.Fatal(err)
	}
}

// writeLegacyInitialScanMarker writes the legacy global initial-scan marker
// exactly as the single-ship implementation did.
func writeLegacyInitialScanMarker(t *testing.T, sp *Spool) {
	t.Helper()
	if err := os.MkdirAll(sp.git.stateDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(sp.legacyInitialScanStateFile(), []byte("done\n"), 0o644); err != nil {
		t.Fatal(err)
	}
}

// countMailboxFiles counts the regular files of a mailbox lifecycle directory.
func countMailboxFiles(t *testing.T, dir string) int {
	t.Helper()
	entries, err := os.ReadDir(dir)
	if err != nil {
		if os.IsNotExist(err) {
			return 0
		}
		t.Fatal(err)
	}
	n := 0
	for _, e := range entries {
		if !e.IsDir() {
			n++
		}
	}
	return n
}

// TestLocalShipsIgnoresEndedShipStatusRecord proves that status records of
// ships that have ended never activate a mailbox: a leftover record of a
// crashed ship (PID no longer alive) and a record announcing an intentional
// shutdown (state "offline") are both ignored — even though "idle" records
// never count as stale.
func TestLocalShipsIgnoresEndedShipStatusRecord(t *testing.T) {
	root, _ := setupCentralTest(t)

	writeBoardStatusWithPID(t, root, "Ghost", "idle", deadPID(t))
	writeBoardStatusWithPID(t, root, "Retired", "offline", os.Getpid())

	ships, err := localShips(root)
	if err != nil {
		t.Fatal(err)
	}
	for _, s := range ships {
		if s == "Ghost" || s == "Retired" {
			t.Fatalf("ended ship %q must not be a local ship, got %v", s, ships)
		}
	}
}

// TestEndedShipStatusRecordDoesNotActivateMailbox proves that a message for a
// ship that has ended is not imported: the leftover status record must not
// keep the mailbox alive.
func TestEndedShipStatusRecordDoesNotActivateMailbox(t *testing.T) {
	root, sp := setupCentralTest(t)
	writeBoardStatusWithPID(t, root, "Ghost", "idle", deadPID(t))

	msg := NewMessage("Enterprise", "Ghost", "ship", "for a ship that ended", "")
	if err := sp.Publish(msg); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := processPendingMessages(root); err != nil {
		t.Fatalf("process: %v", err)
	}

	bus, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if n := countFiles(t, bus.MsgDir); n != 0 {
		t.Fatalf("message for ended ship must not be imported, got %d comms messages", n)
	}
}

// TestRunningAdHocShipWithoutReservationIsDetected proves that a ship that is
// actually running is still recognized without a registry reservation: its
// live PID is the lifecycle information that proves it.
func TestRunningAdHocShipWithoutReservationIsDetected(t *testing.T) {
	root, sp := setupCentralTest(t)
	writeBoardStatusWithPID(t, root, "AdHoc", "working", os.Getpid())

	ships, err := localShips(root)
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, s := range ships {
		if s == "AdHoc" {
			found = true
		}
	}
	if !found {
		t.Fatalf("running ad-hoc ship must be a local ship, got %v", ships)
	}

	msg := NewMessage("Enterprise", "AdHoc", "ship", "for a running ship", "")
	if err := sp.Publish(msg); err != nil {
		t.Fatalf("publish: %v", err)
	}
	if err := processPendingMessages(root); err != nil {
		t.Fatalf("process: %v", err)
	}

	bus, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if n := countFiles(t, bus.MsgDir); n != 1 {
		t.Fatalf("expected 1 imported message, got %d", n)
	}
}

// setBaselineMode switches the test workspace's repository to baseline mode,
// so that a performed initial scan is observable (pre-existing messages are
// moved to processed/ without being imported).
func setBaselineMode(t *testing.T, root string) {
	t.Helper()
	cfg, err := LoadConfig(root)
	if err != nil {
		t.Fatal(err)
	}
	cfg.Repositories[0].Incoming.InitialScan = "baseline"
	writeConfig(t, root, cfg)
}

// TestLegacyInitialScanMarkerAppliesOnlyToOwnerMailbox proves that the legacy
// global initial-scan marker is inherited by the mailbox it belongs to — the
// one of the ship identity the single-ship implementation ran under — while a
// newly appearing mailbox still runs its own baseline scan.
func TestLegacyInitialScanMarkerAppliesOnlyToOwnerMailbox(t *testing.T) {
	root, sp := setupCentralTest(t)
	setBaselineMode(t, root)
	t.Setenv("STARFLEET_SHIP_ID", "alpha")
	registerShipReservation(t, root, "alpha")
	registerShipReservation(t, root, "beta")
	writeLegacyInitialScanMarker(t, sp)

	msgAlpha := NewMessage("Enterprise", "alpha", "ship", "for alpha", "")
	if err := sp.Publish(msgAlpha); err != nil {
		t.Fatalf("publish alpha: %v", err)
	}
	msgBeta := NewMessage("Enterprise", "beta", "ship", "for beta", "")
	if err := sp.Publish(msgBeta); err != nil {
		t.Fatalf("publish beta: %v", err)
	}

	if err := processPendingMessages(root); err != nil {
		t.Fatalf("process: %v", err)
	}

	bus, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}
	// The owner mailbox keeps its completed initial scan: its message is
	// imported instead of being baselined away.
	if n := countFiles(t, filepath.Join(bus.MsgDir, "alpha", "unseen")); n != 1 {
		t.Fatalf("owner mailbox must not be re-baselined, got %d imported messages", n)
	}
	// The new mailbox runs its own baseline scan: its message is not imported.
	if n := countFiles(t, filepath.Join(bus.MsgDir, "beta", "unseen")); n != 0 {
		t.Fatalf("new mailbox must run its own initial scan, got %d imported messages", n)
	}
	if n := countMailboxFiles(t, filepath.Join(sp.git.CloneDir(), "mail", "beta", "processed")); n != 1 {
		t.Fatalf("new mailbox must baseline its own message, got %d processed files", n)
	}
	// Both mailboxes carry their own marker now.
	for _, ship := range []string{"alpha", "beta"} {
		if _, err := os.Stat(sp.initialScanStateFile(ship)); err != nil {
			t.Fatalf("missing per-mailbox marker for %s: %v", ship, err)
		}
	}
}

// TestLegacyInitialScanMarkerOwnerFromMailboxEvidence proves that the owner of
// the legacy global marker is also found when the daemon runs without an
// explicitly configured ship identity: the mailbox showing evidence of the
// previous scan identifies it.
func TestLegacyInitialScanMarkerOwnerFromMailboxEvidence(t *testing.T) {
	root, sp := setupCentralTest(t)
	setBaselineMode(t, root)
	registerShipReservation(t, root, "alpha")
	registerShipReservation(t, root, "beta")
	writeLegacyInitialScanMarker(t, sp)

	// Evidence of the previous scan: the single-ship implementation had moved
	// a message of alpha's mailbox into processed/.
	legacyAbs := filepath.Join(sp.git.CloneDir(), "mail", "alpha", "processed", "legacy.md")
	if err := os.MkdirAll(filepath.Dir(legacyAbs), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(legacyAbs, []byte("---\nid: legacy\n---\nold\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	runGit(t, sp.git.CloneDir(), "add", "-A")
	runGit(t, sp.git.CloneDir(), "commit", "-q", "-m", "wormhole: legacy baseline")

	msgAlpha := NewMessage("Enterprise", "alpha", "ship", "for alpha", "")
	if err := sp.Publish(msgAlpha); err != nil {
		t.Fatalf("publish alpha: %v", err)
	}
	msgBeta := NewMessage("Enterprise", "beta", "ship", "for beta", "")
	if err := sp.Publish(msgBeta); err != nil {
		t.Fatalf("publish beta: %v", err)
	}

	if err := processPendingMessages(root); err != nil {
		t.Fatalf("process: %v", err)
	}

	bus, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if n := countFiles(t, filepath.Join(bus.MsgDir, "alpha", "unseen")); n != 1 {
		t.Fatalf("owner mailbox must not be re-baselined, got %d imported messages", n)
	}
	if n := countFiles(t, filepath.Join(bus.MsgDir, "beta", "unseen")); n != 0 {
		t.Fatalf("new mailbox must run its own initial scan, got %d imported messages", n)
	}
	if _, err := os.Stat(sp.initialScanStateFile("alpha")); err != nil {
		t.Fatalf("missing per-mailbox marker for alpha: %v", err)
	}
}

// TestInitialScanMarkersAreIndependentPerMailbox proves that every mailbox
// initializes itself independently when no legacy marker exists: each one runs
// its own baseline scan and gets its own marker.
func TestInitialScanMarkersAreIndependentPerMailbox(t *testing.T) {
	root, sp := setupCentralTest(t)
	setBaselineMode(t, root)
	registerShipReservation(t, root, "alpha")
	registerShipReservation(t, root, "beta")

	msgAlpha := NewMessage("Enterprise", "alpha", "ship", "for alpha", "")
	if err := sp.Publish(msgAlpha); err != nil {
		t.Fatalf("publish alpha: %v", err)
	}
	msgBeta := NewMessage("Enterprise", "beta", "ship", "for beta", "")
	if err := sp.Publish(msgBeta); err != nil {
		t.Fatalf("publish beta: %v", err)
	}

	if err := processPendingMessages(root); err != nil {
		t.Fatalf("process: %v", err)
	}

	bus, err := comms.New(root)
	if err != nil {
		t.Fatal(err)
	}
	if n := countFiles(t, bus.MsgDir); n != 0 {
		t.Fatalf("baseline must not import pre-existing messages, got %d", n)
	}
	for _, ship := range []string{"alpha", "beta"} {
		if _, err := os.Stat(sp.initialScanStateFile(ship)); err != nil {
			t.Fatalf("missing per-mailbox marker for %s: %v", ship, err)
		}
		if n := countMailboxFiles(t, filepath.Join(sp.git.CloneDir(), "mail", ship, "processed")); n != 1 {
			t.Fatalf("mailbox %s must baseline its own message, got %d processed files", ship, n)
		}
	}
	if _, err := os.Stat(sp.legacyInitialScanStateFile()); !os.IsNotExist(err) {
		t.Fatalf("legacy global marker must not be created, stat err=%v", err)
	}
}
