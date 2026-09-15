// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package util

import (
	"bytes"
	"io"
	"os"
	"os/exec"
	"strings"
)

// Git represents a git repository at a specific directory.
type Git struct {
	Dir string
}

// NewGit creates a new Git instance for the given directory.
func NewGit(dir string) *Git {
	return &Git{Dir: dir}
}

// gitCmd returns a *exec.Cmd configured to run git in the repository directory.
func (g *Git) gitCmd(args ...string) *exec.Cmd {
	cmd := exec.Command("git", append([]string{"-C", g.Dir}, args...)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd
}

// gitCmdEnv is like gitCmd but with extra environment variables.
func (g *Git) gitCmdEnv(env []string, args ...string) *exec.Cmd {
	cmd := exec.Command("git", append([]string{"-C", g.Dir}, args...)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	cmd.Env = append(os.Environ(), env...)
	return cmd
}

// gitCmdErr is like gitCmd but with stdout routed to stderr.
func (g *Git) gitCmdErr(args ...string) *exec.Cmd {
	cmd := exec.Command("git", append([]string{"-C", g.Dir}, args...)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stderr
	cmd.Stderr = os.Stderr
	return cmd
}

// gitCmdSilent runs git with all output discarded.
func (g *Git) gitCmdSilent(args ...string) *exec.Cmd {
	return exec.Command("git", append([]string{"-C", g.Dir}, args...)...)
}

// gitCmdCapture runs git and captures stdout.
func (g *Git) gitCmdCapture(args ...string) (*exec.Cmd, *bytes.Buffer) {
	cmd := exec.Command("git", append([]string{"-C", g.Dir}, args...)...)
	var out bytes.Buffer
	cmd.Stdout = &out
	cmd.Stderr = os.Stderr
	return cmd, &out
}

// gitCmdCaptureQuiet runs git and captures stdout, discarding stderr.
func (g *Git) gitCmdCaptureQuiet(args ...string) (*exec.Cmd, *bytes.Buffer) {
	cmd := exec.Command("git", append([]string{"-C", g.Dir}, args...)...)
	var out bytes.Buffer
	cmd.Stdout = &out
	return cmd, &out
}

// Run executes the git command and returns an error if it fails.
func (g *Git) Run(args ...string) error {
	return g.gitCmd(args...).Run()
}

// RunEnv executes the git command with extra environment variables.
func (g *Git) RunEnv(env []string, args ...string) error {
	return g.gitCmdEnv(env, args...).Run()
}

// RunErr executes the git command with stdout routed to stderr.
func (g *Git) RunErr(args ...string) error {
	return g.gitCmdErr(args...).Run()
}

// RunSilent executes the git command discarding all output.
func (g *Git) RunSilent(args ...string) error {
	return g.gitCmdSilent(args...).Run()
}

// Capture executes the git command and returns trimmed stdout.
func (g *Git) Capture(args ...string) (string, error) {
	cmd, buf := g.gitCmdCapture(args...)
	err := cmd.Run()
	return TrimTrailingNewline(buf.String()), err
}

// CaptureQuiet executes the git command and returns trimmed stdout, discarding stderr.
func (g *Git) CaptureQuiet(args ...string) (string, error) {
	cmd, buf := g.gitCmdCaptureQuiet(args...)
	err := cmd.Run()
	return TrimTrailingNewline(buf.String()), err
}

// CaptureNUL executes the git command and returns raw stdout (for NUL-delimited output).
func (g *Git) CaptureNUL(args ...string) (string, error) {
	cmd, buf := g.gitCmdCapture(args...)
	err := cmd.Run()
	return buf.String(), err
}

// RunTo executes the git command with stdout directed to the given writer.
func (g *Git) RunTo(stdout io.Writer, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", g.Dir}, args...)...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ConfigGet reads a single git config key, returning "" if unset.
func (g *Git) ConfigGet(key string) string {
	v, err := g.CaptureQuiet("config", "--get", key)
	if err != nil {
		return ""
	}
	return v
}

// RevParseAbsoluteGitDir returns the absolute path to the .git directory.
func (g *Git) RevParseAbsoluteGitDir() (string, error) {
	return g.Capture("rev-parse", "--absolute-git-dir")
}

// RevParseAbbrevRef returns the abbreviated ref name (e.g., branch name).
func (g *Git) RevParseAbbrevRef(ref string) (string, error) {
	return g.Capture("rev-parse", "--abbrev-ref", ref)
}

// RevParseShowToplevel returns the top-level directory of the working tree.
func (g *Git) RevParseShowToplevel() (string, error) {
	return g.Capture("rev-parse", "--show-toplevel")
}

// Status runs `git status --porcelain` and returns the output.
func (g *Git) Status(porcelain bool) (string, error) {
	args := []string{"status"}
	if porcelain {
		args = append(args, "--porcelain")
	}
	return g.Capture(args...)
}

// Diff runs `git diff` with the given arguments.
func (g *Git) Diff(args ...string) (string, error) {
	allArgs := append([]string{"diff"}, args...)
	return g.Capture(allArgs...)
}

// DiffCached runs `git diff --cached` with the given arguments.
func (g *Git) DiffCached(args ...string) (string, error) {
	allArgs := append([]string{"diff", "--cached"}, args...)
	return g.Capture(allArgs...)
}

// Add runs `git add` with the given paths.
func (g *Git) Add(paths ...string) error {
	allArgs := append([]string{"add"}, paths...)
	return g.Run(allArgs...)
}

// Commit runs `git commit` with the given message.
func (g *Git) Commit(message string) error {
	return g.Run("commit", "-m", message)
}

// CommitAll runs `git commit -a` with the given message.
func (g *Git) CommitAll(message string) error {
	return g.Run("commit", "-a", "-m", message)
}

// Pull runs `git pull` with the given arguments.
func (g *Git) Pull(args ...string) error {
	allArgs := append([]string{"pull"}, args...)
	return g.Run(allArgs...)
}

// Push runs `git push` with the given arguments.
func (g *Git) Push(args ...string) error {
	allArgs := append([]string{"push"}, args...)
	return g.Run(allArgs...)
}

// Fetch runs `git fetch` with the given arguments.
func (g *Git) Fetch(args ...string) error {
	allArgs := append([]string{"fetch"}, args...)
	return g.Run(allArgs...)
}

// Branch runs `git branch` with the given arguments.
func (g *Git) Branch(args ...string) error {
	allArgs := append([]string{"branch"}, args...)
	return g.Run(allArgs...)
}

// BranchList returns a list of local branches.
func (g *Git) BranchList() ([]string, error) {
	out, err := g.Capture("branch", "--format=%(refname:short)")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return []string{}, nil
	}
	return strings.Split(out, "\n"), nil
}

// BranchRemoteList returns a list of remote tracking branches.
func (g *Git) BranchRemoteList() ([]string, error) {
	out, err := g.Capture("branch", "-r", "--format=%(refname:short)")
	if err != nil {
		return nil, err
	}
	if out == "" {
		return []string{}, nil
	}
	return strings.Split(out, "\n"), nil
}

// Checkout runs `git checkout` with the given arguments.
func (g *Git) Checkout(args ...string) error {
	allArgs := append([]string{"checkout"}, args...)
	return g.Run(allArgs...)
}

// Merge runs `git merge` with the given arguments.
func (g *Git) Merge(args ...string) error {
	allArgs := append([]string{"merge"}, args...)
	return g.Run(allArgs...)
}

// Rebase runs `git rebase` with the given arguments.
func (g *Git) Rebase(args ...string) error {
	allArgs := append([]string{"rebase"}, args...)
	return g.Run(allArgs...)
}

// Log runs `git log` with the given arguments and returns the output.
func (g *Git) Log(args ...string) (string, error) {
	allArgs := append([]string{"log"}, args...)
	return g.Capture(allArgs...)
}

// LogOneline returns the log in oneline format.
func (g *Git) LogOneline(n int) (string, error) {
	args := []string{"log", "--oneline"}
	if n > 0 {
		args = append(args, "-n", string(rune(n)))
	}
	return g.Capture(args...)
}

// Show runs `git show` with the given arguments.
func (g *Git) Show(args ...string) (string, error) {
	allArgs := append([]string{"show"}, args...)
	return g.Capture(allArgs...)
}

// Clone runs `git clone` (does not use -C since dest doesn't exist yet).
func Clone(repoURL, dest string, args ...string) error {
	allArgs := append([]string{"clone"}, args...)
	allArgs = append(allArgs, repoURL, dest)
	cmd := exec.Command("git", allArgs...)
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// WorktreeAdd runs `git worktree add`.
func (g *Git) WorktreeAdd(dest, branch string, createNew bool) error {
	if createNew {
		return g.Run("worktree", "add", "-b", "wt/"+branch, dest, branch)
	}
	return g.Run("worktree", "add", dest, branch)
}

// WorktreeList runs `git worktree list`.
func (g *Git) WorktreeList() (string, error) {
	return g.Capture("worktree", "list")
}

// WorktreeRemove runs `git worktree remove`.
func (g *Git) WorktreeRemove(path string) error {
	return g.Run("worktree", "remove", path)
}

// WorktreePrune runs `git worktree prune`.
func (g *Git) WorktreePrune(verbose bool) error {
	args := []string{"worktree", "prune"}
	if verbose {
		args = append(args, "-v")
	}
	return g.Run(args...)
}

// SymbolicRef returns the symbolic ref (e.g., refs/heads/main).
func (g *Git) SymbolicRef(ref string) (string, error) {
	return g.Capture("symbolic-ref", "-q", "--short", ref)
}

// ShowRefVerify checks if a ref exists.
func (g *Git) ShowRefVerify(ref string) error {
	return g.Run("show-ref", "--verify", "--quiet", ref)
}

// BranchDelete runs `git branch -D`.
func (g *Git) BranchDelete(name string) error {
	return g.Run("branch", "-D", name)
}

// ConfigSet sets a git config key.
func (g *Git) ConfigSet(key, value string) error {
	return g.Run("config", key, value)
}

// Init runs `git init`.
func (g *Git) Init(bare bool) error {
	args := []string{"init"}
	if bare {
		args = append(args, "--bare")
	}
	return g.Run(args...)
}

// IsRepo checks if the directory is a git repository.
func (g *Git) IsRepo() bool {
	_, err := g.RevParseShowToplevel()
	return err == nil
}
