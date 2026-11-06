// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Wormhole git sync manager — handles git clone/fetch/rebase/push for
// wormhole repositories with one repository-scoped lock shared by *all*
// writers (sync and Maildir spool transitions), exact-path staging and
// failure-atomic push/rebase recovery.
package wormhole

import (
	"context"
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
	// PushRetries is the number of push retries after a non-fast-forward race.
	PushRetries int `yaml:"push_retries"`
	// CloneTimeout is the timeout for git clone operations. Zero means no timeout.
	CloneTimeout time.Duration
	// CommandTimeout is the timeout for all other git commands. Zero means no timeout.
	CommandTimeout time.Duration
}

// SyncResult holds the result of a sync operation.
type SyncResult struct {
	RepoID    string
	Action    string // "cloned", "fetched", "rebased", "pushed", "skipped", "failed"
	Message   string
	Err       error
	BeforeSHA string
	AfterSHA  string
	Duration  time.Duration
}

// GitSync manages git sync operations for one wormhole repository checkout.
type GitSync struct {
	root     string // workspace root
	config   GitSyncConfig
	lockFile string // path to per-repo lock file
	cloneDir string // path to the git checkout
	stateDir string // path to .starfleet-state/ inside clone
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
func (g *GitSync) ID() string { return g.config.ID }

// Enabled returns whether this repository is enabled for sync.
func (g *GitSync) Enabled() bool { return g.config.Enabled }

// Remote returns the remote URL.
func (g *GitSync) Remote() string { return g.config.Remote }

// Branch returns the target branch.
func (g *GitSync) Branch() string { return g.config.Branch }

// Access returns the access mode.
func (g *GitSync) Access() string { return g.config.Access }

// CloneDir returns the repository checkout path (mail/ lives below this).
func (g *GitSync) CloneDir() string { return g.cloneDir }

// LockFile returns the repository-scoped lock path shared by all writers.
func (g *GitSync) LockFile() string { return g.lockFile }

// readOnly reports whether this repository must not be written/pushed.
func (g *GitSync) readOnly() bool {
	return g.config.Access == "read-only"
}

// Lock acquires the single repository-scoped writer lock. Every git operation
// on this checkout (sync, publish, claim, processed/failed transition) must be
// performed while holding it, so concurrent starfleetctl processes cannot
// interleave fetch/rebase/commit/push.
func (g *GitSync) Lock(timeout time.Duration) (*flock.Handle, error) {
	if err := os.MkdirAll(filepath.Dir(g.lockFile), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir lock dir: %w", err)
	}
	return flock.Lock(g.lockFile, flock.Options{
		Timeout:     timeout,
		HolderLabel: "wormhole:" + g.config.ID,
	})
}

// Sync performs a full sync cycle: lock, ensure clone, fetch, rebase, push.
func (g *GitSync) Sync() (*SyncResult, error) {
	result := &SyncResult{RepoID: g.config.ID}
	start := time.Now()

	lock, err := g.Lock(60 * time.Second)
	if err != nil {
		return nil, fmt.Errorf("wormhole sync %s: lock: %w", g.config.ID, err)
	}
	defer lock.Close()

	result.BeforeSHA = g.currentHEAD()
	if err := g.SyncLocked(); err != nil {
		result.Action = "failed"
		result.Err = err
		result.Duration = time.Since(start)
		return result, err
	}
	result.AfterSHA = g.currentHEAD()
	if g.readOnly() {
		result.Action = "fetched"
	} else {
		result.Action = "pushed"
	}
	result.Duration = time.Since(start)
	return result, nil
}

// SyncLocked performs ensure-clone, dirty-transition recovery, fetch, rebase
// and (read-write only) push. Caller must hold the repository lock.
// For read-only repos, local Maildir transitions are committed to keep the
// working tree clean for rebase, but not pushed to the remote.
func (g *GitSync) SyncLocked() error {
	if err := g.EnsureCloneLocked(); err != nil {
		return err
	}
	// Always recover dirty mail state before rebasing, regardless of access mode.
	// For read-only repos, this commits local Maildir transitions so rebase can
	// proceed with a clean working tree, but the commits are not pushed.
	if err := g.commitDirtyMailLocked(); err != nil {
		return fmt.Errorf("recover dirty mail state: %w", err)
	}
	if err := g.fetchLocked(); err != nil {
		return err
	}
	if err := g.rebaseLocked(); err != nil {
		return err
	}
	return g.pushLocked()
}

// EnsureCloneLocked ensures the repository is cloned and valid.
func (g *GitSync) EnsureCloneLocked() error {
	gitDir := filepath.Join(g.cloneDir, ".git")
	if _, err := os.Stat(gitDir); err == nil {
		return g.validateClone()
	} else if !os.IsNotExist(err) {
		return fmt.Errorf("stat clone dir: %w", err)
	}

	// A leftover non-git directory is unusable; replace it atomically.
	if _, err := os.Stat(g.cloneDir); err == nil {
		if err := os.RemoveAll(g.cloneDir); err != nil {
			return fmt.Errorf("remove invalid clone dir: %w", err)
		}
	}

	// Ensure the parent directory of the clone directory exists.
	if err := os.MkdirAll(filepath.Dir(g.cloneDir), 0o755); err != nil {
		return fmt.Errorf("mkdir parent of clone dir: %w", err)
	}

	// Clone into a temporary sibling directory first (failure-atomic).
	tmpDir := g.cloneDir + ".tmp." + strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := os.MkdirAll(filepath.Dir(tmpDir), 0o755); err != nil {
		return fmt.Errorf("mkdir temp clone: %w", err)
	}
	defer os.RemoveAll(tmpDir)

	ctx := context.Background()
	var cancel context.CancelFunc
	if g.config.CloneTimeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, g.config.CloneTimeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, "git", "clone", "--branch", g.config.Branch, g.config.Remote, tmpDir)
	cmd.Env = gitCommandEnv()
	if out, err := cmd.CombinedOutput(); err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return fmt.Errorf("clone timed out after %v: %w\n%s", g.config.CloneTimeout, err, string(out))
		}
		return fmt.Errorf("clone failed: %w\n%s", err, string(out))
	}
	if err := g.validateCloneDir(tmpDir); err != nil {
		return fmt.Errorf("clone validation failed: %w", err)
	}
	if err := os.Rename(tmpDir, g.cloneDir); err != nil {
		return fmt.Errorf("rename clone: %w", err)
	}
	if err := os.MkdirAll(g.stateDir, 0o755); err != nil {
		return fmt.Errorf("mkdir state dir: %w", err)
	}
	return g.ensureExcludes()
}

// validateClone validates an existing clone.
func (g *GitSync) validateClone() error {
	if err := g.validateCloneDir(g.cloneDir); err != nil {
		return err
	}
	if err := os.MkdirAll(g.stateDir, 0o755); err != nil {
		return fmt.Errorf("mkdir state dir: %w", err)
	}
	return g.ensureExcludes()
}

// validateCloneDir validates a cloned repo directory.
func (g *GitSync) validateCloneDir(dir string) error {
	if _, err := os.Stat(filepath.Join(dir, ".git")); err != nil {
		return fmt.Errorf("not a git repo: %s", dir)
	}
	remoteURL, err := g.gitConfigIn(dir, "remote.origin.url")
	if err != nil {
		return fmt.Errorf("no remote.origin.url: %w", err)
	}
	if strings.TrimSpace(remoteURL) != g.config.Remote {
		return fmt.Errorf("remote URL mismatch: got %q, want %q", strings.TrimSpace(remoteURL), g.config.Remote)
	}
	branch, err := g.gitRevParseIn(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		return fmt.Errorf("cannot determine branch: %w", err)
	}
	if strings.TrimSpace(branch) != g.config.Branch {
		return fmt.Errorf("branch mismatch: got %q, want %q", strings.TrimSpace(branch), g.config.Branch)
	}
	return nil
}

// ensureExcludes records runtime-only paths in .git/info/exclude so they can
// never be staged as part of a message transition.
func (g *GitSync) ensureExcludes() error {
	path := filepath.Join(g.cloneDir, ".git", "info", "exclude")
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("read .git/info/exclude: %w", err)
	}
	have := string(data)
	add := ""
	for _, entry := range []string{"/.starfleet-state/", "/mail/.tmp/"} {
		if !strings.Contains(have, entry) {
			add += entry + "\n"
		}
	}
	if add == "" {
		return nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return fmt.Errorf("open .git/info/exclude: %w", err)
	}
	defer f.Close()
	if _, err := f.WriteString(add); err != nil {
		return fmt.Errorf("write .git/info/exclude: %w", err)
	}
	return nil
}

func (g *GitSync) gitConfigIn(dir, key string) (string, error) {
	ctx := context.Background()
	if g.config.CommandTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, g.config.CommandTimeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, "git", "-C", dir, "config", "--get", key)
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("git config timeout after %v: %w", g.config.CommandTimeout, err)
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (g *GitSync) gitRevParseIn(dir string, args ...string) (string, error) {
	ctx := context.Background()
	if g.config.CommandTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, g.config.CommandTimeout)
		defer cancel()
	}
	argv := append([]string{"-C", dir}, args...)
	cmd := exec.CommandContext(ctx, "git", argv...)
	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return "", fmt.Errorf("git rev-parse timeout after %v: %w", g.config.CommandTimeout, err)
		}
		return "", err
	}
	return strings.TrimSpace(string(out)), nil
}

func (g *GitSync) currentHEAD() string {
	ctx := context.Background()
	if g.config.CommandTimeout > 0 {
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, g.config.CommandTimeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, "git", "-C", g.cloneDir, "rev-parse", "HEAD")
	out, err := cmd.Output()
	if err != nil {
		return ""
	}
	return strings.TrimSpace(string(out))
}

// gitCommandEnv returns an environment for git invocations that forces a
// deterministic C locale, so git's diagnostic messages — and anything parsed
// from them, like the non-fast-forward push-rejection classification — do not
// depend on the host locale. A pre-existing LC_ALL entry is removed first:
// duplicate entries resolve to the first occurrence, which would otherwise
// shadow the override.
func gitCommandEnv(extra ...string) []string {
	var env []string
	for _, kv := range os.Environ() {
		if strings.HasPrefix(kv, "LC_ALL=") {
			continue
		}
		env = append(env, kv)
	}
	env = append(env, "LC_ALL=C", "GIT_TERMINAL_PROMPT=0")
	return append(env, extra...)
}

// gitLocked runs a git command in the checkout with CommandTimeout. Caller holds the lock.
func (g *GitSync) gitLocked(args ...string) ([]byte, error) {
	return g.gitIndexLocked("", g.config.CommandTimeout, args...)
}

// gitIndexLocked runs a git command in the checkout with an optional isolated
// index file and a timeout. An empty index means the repository's normal index.
// Caller holds the lock. If timeout is 0, no timeout is applied.
func (g *GitSync) gitIndexLocked(index string, timeout time.Duration, args ...string) ([]byte, error) {
	argv := append([]string{"-C", g.cloneDir}, args...)
	ctx := context.Background()
	var cancel context.CancelFunc
	if timeout > 0 {
		ctx, cancel = context.WithTimeout(ctx, timeout)
		defer cancel()
	}
	cmd := exec.CommandContext(ctx, "git", argv...)
	env := gitCommandEnv()
	if index != "" {
		env = append(env, "GIT_INDEX_FILE="+index)
	}
	cmd.Env = env
	out, err := cmd.CombinedOutput()
	if err != nil {
		if ctx.Err() == context.DeadlineExceeded {
			return out, fmt.Errorf("git command timed out after %v: %w", timeout, err)
		}
	}
	return out, err
}

func (g *GitSync) fetchLocked() error {
	out, err := g.gitLocked("fetch", "origin", g.config.Branch)
	if err != nil {
		return fmt.Errorf("fetch failed: %w\n%s", err, string(out))
	}
	return nil
}

func (g *GitSync) statusLocked(pathspec ...string) (string, error) {
	args := []string{"status", "--porcelain", "--"}
	args = append(args, pathspec...)
	out, err := g.gitLocked(args...)
	if err != nil {
		return "", fmt.Errorf("git status: %w\n%s", err, string(out))
	}
	return strings.TrimSpace(string(out)), nil
}

func (g *GitSync) rebaseLocked() error {
	status, err := g.statusLocked()
	if err != nil {
		return err
	}
	if status != "" {
		return fmt.Errorf("working tree not clean, cannot rebase: %s", status)
	}
	// Rename heuristics are disabled for two reasons: wormhole lifecycle
	// commits are exact add/delete pairs of one .md file per state
	// directory, so following renames adds nothing; and git's directory
	// rename detection misfires when a local claim commit empties new/ and
	// is replayed onto a remote commit that added another file into new/ —
	// it infers a directory rename new/→cur/ and conflicts on every new
	// remote message. This hits read-only repositories (whose lifecycle
	// commits exist only locally and are rebased on every sync).
	// -X no-renames disables rename following; -c merge.directoryRenames=false
	// disables directory rename detection (not covered by -X no-renames).
	out, err := g.gitLocked("-c", "merge.directoryRenames=false",
		"rebase", "-X", "no-renames", "origin/"+g.config.Branch)
	if err != nil {
		_, _ = g.gitLocked("rebase", "--abort")
		return fmt.Errorf("rebase failed: %w\n%s", err, string(out))
	}
	return nil
}

// isNonFastForward reports whether a push failure is a recoverable
// non-fast-forward rejection that fetch+rebase can resolve.
func isNonFastForward(out []byte) bool {
	s := string(out)
	return strings.Contains(s, "non-fast-forward") ||
		strings.Contains(s, "fetch first") ||
		strings.Contains(s, "[rejected]") ||
		strings.Contains(s, "failed to push some refs")
}

// pushLocked pushes the branch, and on a non-fast-forward rejection fetches,
// rebases and retries (bounded). It never force-pushes and never discards
// local commits; a rebase conflict leaves the local commits intact.
func (g *GitSync) pushLocked() error {
	if g.readOnly() {
		return nil
	}
	retries := g.config.PushRetries
	if retries < 0 {
		retries = 0
	}
	var lastErr error
	var lastOut []byte
	for attempt := 0; attempt <= retries; attempt++ {
		out, err := g.gitLocked("push", "origin", g.config.Branch)
		if err == nil {
			return nil
		}
		lastErr, lastOut = err, out
		if !isNonFastForward(out) {
			return fmt.Errorf("push failed: %w\n%s", err, string(out))
		}
		if ferr := g.fetchLocked(); ferr != nil {
			return ferr
		}
		if rerr := g.rebaseLocked(); rerr != nil {
			return rerr
		}
	}
	return fmt.Errorf("push failed after %d retries: %w\n%s", retries, lastErr, string(lastOut))
}

// stageExactAtLocked stages exactly one repository-relative path in the given
// index (empty = normal index): an added/modified file, or the deletion of an
// already-tracked file. It never stages a directory or uses broad globs.
func (g *GitSync) stageExactAtLocked(index, rel string, timeout time.Duration) error {
	rel = filepath.ToSlash(rel)
	abs := filepath.Join(g.cloneDir, filepath.FromSlash(rel))
	if _, err := os.Stat(abs); err == nil {
		if out, err := g.gitIndexLocked(index, timeout, "add", "--", rel); err != nil {
			return fmt.Errorf("git add %s: %w\n%s", rel, err, string(out))
		}
		return nil
	}
	// Path is gone: stage the deletion only if it was actually tracked.
	if _, err := g.gitIndexLocked(index, timeout, "ls-files", "--error-unmatch", "--", rel); err != nil {
		return nil // untracked and absent: nothing to record
	}
	if out, err := g.gitIndexLocked(index, timeout, "add", "--", rel); err != nil {
		return fmt.Errorf("git add (delete) %s: %w\n%s", rel, err, string(out))
	}
	return nil
}

// hasStagedAtLocked reports whether the given index differs from HEAD.
func (g *GitSync) hasStagedAtLocked(index string) (bool, error) {
	out, err := g.gitIndexLocked(index, g.config.CommandTimeout, "diff", "--cached", "--name-only")
	if err != nil {
		return false, fmt.Errorf("git diff --cached: %w\n%s", err, string(out))
	}
	return strings.TrimSpace(string(out)) != "", nil
}

func (g *GitSync) commitAtLocked(index, message string) error {
	out, err := g.gitIndexLocked(index, g.config.CommandTimeout,
		"-c", "user.name=starfleetctl",
		"-c", "user.email=starfleetctl@localhost",
		"commit", "-q", "-m", message,
	)
	if err != nil {
		return fmt.Errorf("git commit: %w\n%s", err, string(out))
	}
	return nil
}

// newTempIndexLocked returns a unique path for a temporary Git index and a
// cleanup function. The file must not exist before git read-tree writes it.
func (g *GitSync) newTempIndexLocked() (string, func(), error) {
	if err := os.MkdirAll(g.stateDir, 0o755); err != nil {
		return "", nil, fmt.Errorf("mkdir state dir: %w", err)
	}
	f, err := os.CreateTemp(g.stateDir, "index-*")
	if err != nil {
		return "", nil, fmt.Errorf("create temp index: %w", err)
	}
	name := f.Name()
	if err := f.Close(); err != nil {
		return "", nil, fmt.Errorf("close temp index: %w", err)
	}
	if err := os.Remove(name); err != nil {
		return "", nil, fmt.Errorf("remove temp index placeholder: %w", err)
	}
	return name, func() { _ = os.Remove(name) }, nil
}

// commitExactPathsLocked commits exactly the given repository-relative paths
// using an isolated index seeded from HEAD. Foreign changes that are staged in
// the normal index are never included and remain staged for their owner.
//
// After the commit, the normal index is refreshed for exactly those paths (so
// it stays consistent with the new HEAD) without touching any other staged
// entry. Caller must hold the repository lock.
func (g *GitSync) commitExactPathsLocked(relPaths []string, message string) (bool, error) {
	index, cleanup, err := g.newTempIndexLocked()
	if err != nil {
		return false, err
	}
	defer cleanup()

	if out, err := g.gitIndexLocked(index, g.config.CommandTimeout, "read-tree", "HEAD"); err != nil {
		return false, fmt.Errorf("git read-tree HEAD: %w\n%s", err, string(out))
	}

	seen := make(map[string]bool)
	paths := make([]string, 0, len(relPaths))
	for _, rel := range relPaths {
		if rel == "" {
			continue
		}
		rel = filepath.ToSlash(rel)
		if seen[rel] {
			continue
		}
		seen[rel] = true
		paths = append(paths, rel)
		if err := g.stageExactAtLocked(index, rel, g.config.CommandTimeout); err != nil {
			return false, err
		}
	}

	staged, err := g.hasStagedAtLocked(index)
	if err != nil {
		return false, err
	}
	if !staged {
		return false, nil
	}
	if err := g.commitAtLocked(index, message); err != nil {
		return false, err
	}

	// Bring the normal index up to date for the committed paths only. Any
	// unrelated staged entry is left untouched.
	for _, rel := range paths {
		if err := g.stageExactAtLocked("", rel, g.config.CommandTimeout); err != nil {
			return false, err
		}
	}
	return true, nil
}

// CommitAndPushLocked stages exactly the given repository-relative paths,
// commits them as one transition (if anything is staged) and pushes. It is
// used for every publish and lifecycle transition; the commit is only
// considered durable once pushLocked returns nil.
func (g *GitSync) CommitAndPushLocked(relPaths []string, message string) error {
	if _, err := g.commitExactPathsLocked(relPaths, message); err != nil {
		return err
	}
	return g.pushLocked()
}

// commitDirtyMailLocked recovers lifecycle transitions that were moved locally
// but not yet committed (crash between rename and commit). It stages exactly
// the paths reported by git status under mail/ and commits them as one
// recovery commit. No push happens here; SyncLocked pushes afterwards.
func (g *GitSync) commitDirtyMailLocked() error {
	out, err := g.gitLocked("status", "--porcelain", "-z", "--untracked-files=all", "--", "mail")
	if err != nil {
		return fmt.Errorf("git status: %w\n%s", err, string(out))
	}
	if len(out) == 0 {
		return nil
	}
	fields := strings.Split(string(out), "\x00")
	paths := make([]string, 0, len(fields))
	for i := 0; i < len(fields); i++ {
		entry := fields[i]
		if len(entry) < 4 {
			continue
		}
		xy := entry[:2]
		paths = append(paths, entry[3:])
		// With -z, a rename/copy record stores the source path as a second
		// NUL-separated field immediately after the destination.
		if (xy[0] == 'R' || xy[0] == 'C') && i+1 < len(fields) {
			i++
			paths = append(paths, fields[i])
		}
	}
	_, err = g.commitExactPathsLocked(paths, "wormhole: recover interrupted lifecycle transition")
	return err
}

// IsLocked returns whether the repository lock is currently held.
func (g *GitSync) IsLocked() bool {
	lock, err := g.Lock(100 * time.Millisecond)
	if err != nil {
		return true
	}
	_ = lock.Close()
	return false
}

// String returns a human-readable summary of the sync result.
func (r *SyncResult) String() string {
	if r.Err != nil {
		return fmt.Sprintf("%s: FAILED: %v", r.RepoID, r.Err)
	}
	before, after := r.BeforeSHA, r.AfterSHA
	if len(before) > 8 {
		before = before[:8]
	}
	if len(after) > 8 {
		after = after[:8]
	}
	return fmt.Sprintf("%s: %s (%s -> %s)", r.RepoID, r.Action, before, after)
}
