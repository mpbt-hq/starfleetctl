// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Wormhole git sync manager — handles git clone/fetch/rebase/push for
// wormhole repositories with per-repository locking and failure-atomic
// operations.
package wormhole

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/metux/starfleetctl/internal/flock"
)

// GitSyncConfig holds configuration for a single wormhole repository sync.
type GitSyncConfig struct {
	// ID is the repository identifier. Must be unique, safe path component.
	ID string `yaml:"id"`
	// Remote is the git remote URL (SSH or HTTPS). Required for active repos.
	Remote string `yaml:"remote"`
	// Branch is the git branch to track. Required for active repos.
	Branch string `yaml:"branch"`
	// Enabled toggles this repository on/off. Default: true.
	Enabled bool `yaml:"enabled"`
	// Access is read-only or read-write. Default: read-write.
	Access string `yaml:"access"`
}

// SyncResult holds the result of a sync operation.
type SyncResult struct {
	RepoID   string
	Action   string // "cloned", "fetched", "rebased", "pushed", "skipped", "failed"
	Message  string
	Err      error
	BeforeSHA string
	AfterSHA  string
	Duration time.Duration
}

// GitSync manages git sync operations for wormhole repositories.
type GitSync struct {
	root      string        // workspace root
	config    GitSyncConfig
	lockFile  string        // path to per-repo lock file
	cloneDir  string        // path to the git checkout
	stateDir  string        // path to .starfleet-state/ inside clone
	lock      *flock.Handle // per-repo lock handle
}

// NewGitSync creates a new GitSync instance for the given repository config.
func NewGitSync(root string, cfg GitSyncConfig) *GitSync {
	return &GitSync{
		root:     root,
		config:   cfg,
		lockFile: filepath.Join(WorkDir(root), cfg.ID+".lock"),
		cloneDir: filepath.Join(WorkDir(root), cfg.ID),
		stateDir: filepath.Join(WorkDir(root), cfg.ID, ".starfleet-state"),
	}
}

// ID returns the repository identifier.
func (g *GitSync) ID() string {
	return g.config.ID
}

// Enabled returns whether this repository is enabled for sync.
func (g *GitSync) Enabled() bool {
	return g.config.Enabled
}

// Remote returns the remote URL.
func (g *GitSync) Remote() string {
	return g.config.Remote
}

// Branch returns the target branch.
func (g *GitSync) Branch() string {
	return g.config.Branch
}

// Access returns the access mode.
func (g *GitSync) Access() string {
	return g.config.Access
}

// Sync performs a full sync cycle: lock, validate/clone, fetch, rebase, push (if read-write), unlock.
func (g *GitSync) Sync() (*SyncResult, error) {
	result := &SyncResult{
		RepoID: g.config.ID,
	}

	// Acquire per-repository lock
	lock, err := flock.Lock(g.lockFile, flock.Options{Timeout: 60 * time.Second})
	if err != nil {
		return nil, fmt.Errorf("wormhole sync %s: lock: %w", g.config.ID, err)
	}
	defer lock.Close()

	// Validate existing clone or clone fresh
	if err := g.ensureClone(); err != nil {
		result.Action = "failed"
		result.Err = err
		return result, err
	}

	// Fetch from remote
	if err := g.fetch(); err != nil {
		result.Action = "failed"
		result.Err = err
		return result, err
	}
	result.BeforeSHA = g.currentHEAD()

	// Rebase onto origin/branch
	if g.config.Access == "read-write" {
		if err := g.rebase(); err != nil {
			result.Action = "failed"
			result.Err = err
			return result, err
		}
	}

	// Push changes (read-write only)
	if g.config.Access == "read-write" {
		if err := g.push(); err != nil {
			result.Action = "failed"
			result.Err = err
			return result, err
		}
		result.Action = "pushed"
	} else {
		result.Action = "fetched"
	}

	result.AfterSHA = g.currentHEAD()
	return result, nil
}

// ensureClone ensures the repository is cloned and valid.
func (g *GitSync) ensureClone() error {
	cloneDir := g.cloneDir

	// Check if clone exists
	gitDir := filepath.Join(cloneDir, ".git")
	if _, err := os.Stat(gitDir); err == nil {
		// Clone exists — validate it
		return g.validateClone()
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("wormhole sync %s: stat clone dir: %w", g.config.ID, err)
	}

	// Clone into temporary sibling directory first (failure-atomic)
	tmpDir := cloneDir + ".tmp." + strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return fmt.Errorf("wormhole sync %s: mkdir temp clone: %w", g.config.ID, err)
	}
	defer os.RemoveAll(tmpDir)

	// Clone into temp directory
	cmd := exec.Command("git", "clone", "--branch", g.config.Branch, g.config.Remote, tmpDir)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	if out, err := cmd.CombinedOutput(); err != nil {
		return fmt.Errorf("wormhole sync %s: clone failed: %w\n%s", g.config.ID, err, string(out))
	}

	// Validate cloned repo
	if err := g.validateCloneDir(tmpDir); err != nil {
		return fmt.Errorf("wormhole sync %s: clone validation failed: %w", g.config.ID, err)
	}

	// Atomic rename to final path
	if err := os.Rename(tmpDir, cloneDir); err != nil {
		os.RemoveAll(tmpDir)
		return fmt.Errorf("wormhole sync %s: rename clone: %w", g.config.ID, err)
	}

	// Initialize state directory
	if err := os.MkdirAll(g.stateDir, 0o755); err != nil {
		return fmt.Errorf("wormhole sync %s: mkdir state dir: %w", g.config.ID, err)
	}

	return nil
}

// validateClone validates an existing clone.
func (g *GitSync) validateClone() error {
	cloneDir := g.cloneDir

	// Check it's a valid git repo
	gitDir := filepath.Join(cloneDir, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		if os.IsNotExist(err) {
			return fmt.Errorf("wormhole sync %s: not a git repo: %s", g.config.ID, cloneDir)
		}
		return err
	}

	// Check remote URL matches
	remoteURL, err := g.gitConfig("remote.origin.url")
	if err != nil {
		return fmt.Errorf("wormhole sync %s: no remote.origin.url: %w", g.config.ID, err)
	}
	if strings.TrimSpace(remoteURL) != g.config.Remote {
		return fmt.Errorf("wormhole sync %s: remote URL mismatch: got %q, want %q", g.config.ID, strings.TrimSpace(remoteURL), g.config.Remote)
	}

	// Check branch matches
	branch, err := g.gitRevParse("--abbrev-ref", "HEAD")
	if err != nil {
		return fmt.Errorf("wormhole sync %s: cannot determine branch: %w", g.config.ID, err)
	}
	if strings.TrimSpace(branch) != g.config.Branch {
		return fmt.Errorf("wormhole sync %s: branch mismatch: got %q, want %q", g.config.ID, strings.TrimSpace(branch), g.config.Branch)
	}

	// Check .starfleet-state is excluded (not tracked)
	gitIgnorePath := filepath.Join(cloneDir, ".git", "info", "exclude")
	if _, err := os.Stat(gitIgnorePath); err == nil {
		data, err := os.ReadFile(gitIgnorePath)
		if err != nil {
			return fmt.Errorf("wormhole sync %s: read .git/info/exclude: %w", g.config.ID, err)
		}
		if !strings.Contains(string(data), "/.starfleet-state/") {
			// Add .starfleet-state/ to exclude
			if err := os.WriteFile(gitIgnorePath, append(data, []byte("/.starfleet-state/\n")...), 0o644); err != nil {
				return fmt.Errorf("wormhole sync %s: write .git/info/exclude: %w", g.config.ID, err)
			}
		}
	}

	return nil
}

// validateCloneDir validates a cloned repo directory.
func (g *GitSync) validateCloneDir(dir string) error {
	gitDir := filepath.Join(dir, ".git")
	if _, err := os.Stat(gitDir); err != nil {
		return fmt.Errorf("not a git repo: %s", dir)
	}

	// Check remote URL matches
	remoteURL, err := g.gitConfigIn(dir, "remote.origin.url")
	if err != nil {
		return fmt.Errorf("no remote.origin.url: %w", err)
	}
	if strings.TrimSpace(remoteURL) != g.config.Remote {
		return fmt.Errorf("remote URL mismatch: got %q, want %q", strings.TrimSpace(remoteURL), g.config.Remote)
	}

	// Check branch matches
	branch, err := g.gitRevParseIn(dir, "--abbrev-ref", "HEAD")
	if err != nil {
		return fmt.Errorf("cannot determine branch: %w", err)
	}
	if strings.TrimSpace(branch) != g.config.Branch {
		return fmt.Errorf("branch mismatch: got %q, want %q", strings.TrimSpace(branch), g.config.Branch)
	}

	return nil
}

// gitConfig runs git config --get in the clone directory.
func (g *GitSync) gitConfig(key string) (string, error) {
	return g.gitConfigIn(g.cloneDir, key)
}

// gitConfigIn runs git config --get in the given directory.
func (g *GitSync) gitConfigIn(dir, key string) (string, error) {
	cmd := exec.Command("git", "-C", dir, "config", "--get", key)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// gitRevParse runs git rev-parse in the clone directory.
func (g *GitSync) gitRevParse(args ...string) (string, error) {
	return g.gitRevParseIn(g.cloneDir, args...)
}

// gitRevParseIn runs git rev-parse in the given directory.
func (g *GitSync) gitRevParseIn(dir string, args ...string) (string, error) {
	args = append([]string{"-C", dir}, args...)
	cmd := exec.Command("git", args...)
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// currentHEAD returns the current HEAD commit hash.
func (g *GitSync) currentHEAD() string {
	hash, err := g.gitRevParse("HEAD")
	if err != nil {
		return ""
	}
	return strings.TrimSpace(hash)
}

// fetch fetches from the remote.
func (g *GitSync) fetch() error {
	cmd := exec.Command("git", "-C", g.cloneDir, "fetch", g.config.Remote, g.config.Branch)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("fetch failed: %w\n%s", err, string(out))
	}
	return nil
}

// rebase rebases the current branch onto origin/branch.
func (g *GitSync) rebase() error {
	// First, check if there are local changes that would block rebase
	status, err := g.gitStatus()
	if err != nil {
		return fmt.Errorf("git status: %w", err)
	}
	if status != "" {
		return fmt.Errorf("working tree not clean, cannot rebase: %s", status)
	}

	cmd := exec.Command("git", "-C", g.cloneDir, "rebase", "origin/"+g.config.Branch)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		// Try to abort the rebase
		exec.Command("git", "-C", g.cloneDir, "rebase", "--abort").Run()
		return fmt.Errorf("rebase failed: %w\n%s", err, string(out))
	}
	return nil
}

// push pushes the current branch to origin.
func (g *GitSync) push() error {
	cmd := exec.Command("git", "-C", g.cloneDir, "push", g.config.Remote, g.config.Branch)
	cmd.Env = append(os.Environ(), "GIT_TERMINAL_PROMPT=0")
	out, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("push failed: %w\n%s", err, string(out))
	}
	return nil
}

// gitStatus returns the output of git status --porcelain.
func (g *GitSync) gitStatus() (string, error) {
	cmd := exec.Command("git", "-C", g.cloneDir, "status", "--porcelain")
	out, err := cmd.Output()
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

// IsLocked returns whether the repository lock is currently held.
func (g *GitSync) IsLocked() bool {
	// Try to acquire the lock; if we can, it's not locked
	lock, err := flock.Lock(g.lockFile, flock.Options{Timeout: 100 * time.Millisecond})
	if err != nil {
		return true // locked by someone else
	}
	lock.Close()
	return false
}

// gitSyncResult returns a human-readable summary of the sync result.
func (r *SyncResult) String() string {
	if r.Err != nil {
		return fmt.Sprintf("%s: FAILED: %v", r.RepoID, r.Err)
	}
	return fmt.Sprintf("%s: %s (%s -> %s)", r.RepoID, r.Action, r.BeforeSHA[:8], r.AfterSHA[:8])
}
