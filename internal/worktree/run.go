// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

// Package worktree is the Go port of scripts/worktree — create/list/remove
// throwaway git worktrees for ANY existing repo (not just xserver release-line
// clones that mk-agent-clone handles), for temporary / per-session / per-task
// isolation without a full clone.
package worktree

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

// Worktree holds one invocation's resolved locations.
type Worktree struct {
	Root string // workspace root (toplevel of the git checkout)
}

func Run(root string, args []string) int {
	if len(args) < 1 {
		usage()
		return 2
	}
	cmd := args[0]
	args = args[1:]

	w := &Worktree{Root: root}

	switch cmd {
	case "add":
		return w.doAdd(args)
	case "list":
		return w.doList(args)
	case "remove":
		return w.doRemove(args)
	case "prune":
		return w.doPrune(args)
	default:
		fmt.Fprintf(os.Stderr, "worktree: unknown subcommand: %s\n", cmd)
		usage()
		return 2
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usage: worktree add <repo-path> [name] [--from <ref>] [--branch <existing-branch>]
       worktree list [repo-path]
       worktree remove <repo-path> <name> [--force] [--keep-branch]
       worktree prune [repo-path]
`)
}

func (w *Worktree) doAdd(args []string) int {
	if len(args) < 1 {
		usage()
		return 2
	}
	repo, err := repoToplevel(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "worktree:", err)
		return 1
	}
	args = args[1:]

	name := fmt.Sprintf("wt-%d", os.Getpid())
	from := ""
	branch := ""
	for len(args) > 0 {
		switch args[0] {
		case "--from":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "worktree: --from needs a ref")
				return 2
			}
			from = args[1]
			args = args[2:]
		case "--branch":
			if len(args) < 2 {
				fmt.Fprintln(os.Stderr, "worktree: --branch needs a branch name")
				return 2
			}
			branch = args[1]
			args = args[2:]
		default:
			if name == fmt.Sprintf("wt-%d", os.Getpid()) && !strings.HasPrefix(args[0], "--") {
				name = args[0]
				args = args[1:]
			} else {
				usage()
				return 2
			}
		}
	}

	reponame := filepath.Base(repo)
	destRoot := filepath.Join(w.Root, "_WORK_", "worktrees", reponame)
	dest := filepath.Join(destRoot, name)

	if _, err := os.Stat(dest); err == nil {
		fmt.Fprintf(os.Stderr, "worktree: already exists: %s\n", dest)
		return 1
	}
	if err := os.MkdirAll(destRoot, 0o755); err != nil {
		fmt.Fprintf(os.Stderr, "worktree: mkdir: %v\n", err)
		return 1
	}

	if branch != "" {
		if err := run(repo, "git", "worktree", "add", dest, branch); err != nil {
			fmt.Fprintf(os.Stderr, "worktree: add: %v\n", err)
			return 1
		}
	} else {
		if from == "" {
			out, err := runCapture(repo, "git", "symbolic-ref", "-q", "--short", "refs/remotes/origin/HEAD")
			if err != nil {
				out, err = runCapture(repo, "git", "rev-parse", "--abbrev-ref", "HEAD")
				if err != nil {
					fmt.Fprintf(os.Stderr, "worktree: resolve default branch: %v\n", err)
					return 1
				}
			}
			from = strings.TrimSpace(out)
		}
		if err := run(repo, "git", "worktree", "add", "-b", "wt/"+name, dest, from); err != nil {
			fmt.Fprintf(os.Stderr, "worktree: add: %v\n", err)
			return 1
		}
	}
	fmt.Println(dest)
	return 0
}

func (w *Worktree) doList(args []string) int {
	if len(args) >= 1 {
		repo, err := repoToplevel(args[0])
		if err != nil {
			fmt.Fprintln(os.Stderr, "worktree:", err)
			return 1
		}
		if err := run(repo, "git", "worktree", "list"); err != nil {
			return 1
		}
		return 0
	}

	root := filepath.Join(w.Root, "_WORK_", "worktrees")
	if _, err := os.Stat(root); err != nil {
		fmt.Println("worktree: no worktrees created yet")
		return 0
	}

	entries, err := os.ReadDir(root)
	if err != nil {
		fmt.Fprintf(os.Stderr, "worktree: read worktrees root: %v\n", err)
		return 1
	}
	for _, re := range entries {
		if !re.IsDir() {
			continue
		}
		reponame := re.Name()
		reporoot := filepath.Join(root, reponame)
		wtEntries, err := os.ReadDir(reporoot)
		if err != nil {
			continue
		}
		for _, wte := range wtEntries {
			if !wte.IsDir() {
				continue
			}
			wtPath := filepath.Join(reporoot, wte.Name())
			if _, err := runCapture(wtPath, "git", "rev-parse", "--show-toplevel"); err != nil {
				continue
			}
			branchOut, _ := runCapture(wtPath, "git", "rev-parse", "--abbrev-ref", "HEAD")
			fmt.Printf("%s\t%s\n", wtPath, strings.TrimSpace(branchOut))
		}
	}
	return 0
}

func (w *Worktree) doRemove(args []string) int {
	if len(args) < 2 {
		usage()
		return 2
	}
	repo, err := repoToplevel(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "worktree:", err)
		return 1
	}
	name := args[1]
	args = args[2:]

	force := false
	keepBranch := false
	for _, a := range args {
		switch a {
		case "--force":
			force = true
		case "--keep-branch":
			keepBranch = true
		default:
			usage()
			return 2
		}
	}

	reponame := filepath.Base(repo)
	dest := filepath.Join(w.Root, "_WORK_", "worktrees", reponame, name)

	if _, err := os.Stat(dest); err != nil {
		fmt.Fprintf(os.Stderr, "worktree: no such worktree: %s\n", dest)
		return 1
	}

	rmArgs := []string{"worktree", "remove"}
	if force {
		rmArgs = append(rmArgs, "--force")
	}
	rmArgs = append(rmArgs, dest)
	if err := run(repo, "git", rmArgs...); err != nil {
		fmt.Fprintf(os.Stderr, "worktree: remove: %v\n", err)
		return 1
	}

	if !keepBranch {
		branch := "wt/" + name
		_, err := runCapture(repo, "git", "show-ref", "--verify", "--quiet", "refs/heads/"+branch)
		if err == nil {
			if err := run(repo, "git", "branch", "-D", branch); err != nil {
				fmt.Fprintf(os.Stderr, "worktree: delete branch: %v\n", err)
				return 1
			}
		}
	}
	return 0
}

func (w *Worktree) doPrune(args []string) int {
	if len(args) >= 1 {
		repo, err := repoToplevel(args[0])
		if err != nil {
			fmt.Fprintln(os.Stderr, "worktree:", err)
			return 1
		}
		if err := run(repo, "git", "worktree", "prune", "-v"); err != nil {
			return 1
		}
		return 0
	}

	root := filepath.Join(w.Root, "_WORK_", "worktrees")
	if _, err := os.Stat(root); err != nil {
		return 0
	}

	repos, err := os.ReadDir(root)
	if err != nil {
		return 0
	}
	for _, re := range repos {
		if !re.IsDir() {
			continue
		}
		reporoot := filepath.Join(root, re.Name())
		wts, err := os.ReadDir(reporoot)
		if err != nil {
			continue
		}
		for _, wte := range wts {
			if !wte.IsDir() {
				continue
			}
			wtPath := filepath.Join(reporoot, wte.Name())
			if _, err := runCapture(wtPath, "git", "rev-parse", "--show-toplevel"); err != nil {
				fmt.Printf("worktree: pruning stale dir (no longer a worktree): %s\n", wtPath)
				os.Remove(wtPath)
			}
		}
		os.Remove(reporoot)
	}
	return 0
}

func repoToplevel(path string) (string, error) {
	out, err := runCapture(path, "git", "rev-parse", "--show-toplevel")
	if err != nil {
		return "", fmt.Errorf("not a git repo: %s", path)
	}
	return strings.TrimSpace(out), nil
}

func run(dir, cmd string, args ...string) error {
	c := exec.Command(cmd, args...)
	c.Dir = dir
	c.Stdout = os.Stdout
	c.Stderr = os.Stderr
	return c.Run()
}

func runCapture(dir, cmd string, args ...string) (string, error) {
	c := exec.Command(cmd, args...)
	c.Dir = dir
	out, err := c.CombinedOutput()
	return string(out), err
}
