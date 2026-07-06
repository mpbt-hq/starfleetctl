// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Go port of scripts/xx-make-pr.sh: submit one or more commits from the
// current (incubator) branch as a PR against the configured upstream, mark
// the commits with the resulting PR number, and rebase the incubator branch
// onto the result. Operates on a given working directory (defaults to cwd
// when invoked directly) — same as the bash original, which reads its
// config from `git config` in whatever directory it's run from.
//
// PARITY NOTE: this preserves the bash original's DEFAULT_MODE="rebase" and
// its documented consequence — the "rebase" mode's marker rewrite touches
// the pushed PR branch itself, not just the incubator (see AGENTS.md "PR
// workflow" — this is a known, not-yet-fixed leak of the "[PR #N]" prefix
// onto merged commits). Faithfully porting the bug, not silently fixing it,
// since this task is a parity port, not a redesign; the "incubator" mode
// that avoids the leak exists in the code (like the bash original) but has
// no CLI flag to select it in either version — dead code kept for parity.
package ghpr

import (
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
	"time"
)

const xxMakePRUsage = `usage: starfleetctl xx-make-pr [options] <commit> [<commit> ...]

Options:
  --rebase            Use rebase mode (markers added to PR branch, then incubator rebased).
  --branch <name>     Explicitly set PR branch name instead of auto-generating it.

Arguments:
  One or more commit SHAs (not necessarily consecutive) to include in the PR.
`

// RunXXMakePR implements `starfleetctl xx-make-pr`, operating in dir (the
// caller's cwd when invoked as a standalone subcommand, or an explicit
// clone dir when called internally from RunBackportCommit).
func RunXXMakePR(dir string, args []string) int {
	upstreamRemote := gitConfigGet(dir, "make-pr.upstream-remote")
	upstreamBranch := gitConfigGet(dir, "make-pr.upstream-branch")
	reviewers := gitConfigGet(dir, "make-pr.reviewers")

	if len(args) < 1 {
		fmt.Print(xxMakePRUsage)
		return 1
	}
	if upstreamRemote == "" {
		fmt.Fprintln(os.Stderr, "xx-make-pr: missing git config entry: make-pr.upstream-remote")
		return 1
	}
	if upstreamBranch == "" {
		fmt.Fprintln(os.Stderr, "xx-make-pr: missing git config entry: make-pr.upstream-branch")
		return 1
	}
	if reviewers == "" {
		fmt.Fprintln(os.Stderr, "xx-make-pr: missing git config entry: make-pr.reviewers")
		return 1
	}
	upstreamRef := upstreamRemote + "/" + upstreamBranch

	mode := "rebase" // DEFAULT_MODE, matches the bash original
	branchName := ""
	var commits []string
	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--rebase":
			mode = "rebase"
		case "--branch":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "xx-make-pr: --branch needs a value")
				return 1
			}
			branchName = args[i]
		default:
			commits = append(commits, args[i])
		}
	}
	if len(commits) == 0 {
		fmt.Fprintln(os.Stderr, "Error: At least one commit must be specified.")
		return 1
	}

	incubatorBranch, err := gitCapture(dir, "rev-parse", "--abbrev-ref", "HEAD")
	if err != nil {
		fprintErr("xx-make-pr", err)
		return 1
	}

	if branchName == "" {
		subject, err := gitCapture(dir, "log", "-1", "--pretty=%s", commits[0])
		if err != nil {
			fprintErr("xx-make-pr", err)
			return 1
		}
		branchName = fmt.Sprintf("pr/%s-%s_%s", upstreamBranch, slugify(subject), time.Now().Format("2006-01-02_15-04-05"))
	}
	tmpBranch := "tmp-" + branchName

	fmt.Printf("Mode: %s\n", mode)
	fmt.Printf("Incubator: %s\n", incubatorBranch)
	fmt.Printf("New PR branch: %s\n", branchName)
	fmt.Printf("Commits: %s\n", strings.Join(commits, " "))

	if err := gitRun(dir, "fetch", upstreamRemote); err != nil {
		fprintErr("xx-make-pr", err)
		return 1
	}
	if err := gitRun(dir, "checkout", "-b", tmpBranch, upstreamRef); err != nil {
		fprintErr("xx-make-pr", err)
		return 1
	}

	for _, c := range commits {
		if err := gitRun(dir, "cherry-pick", c); err != nil {
			fmt.Fprintf(os.Stderr, "Cherry-pick of %s failed. Please resolve manually!\n", c)
			return 1
		}
	}

	if err := gitRun(dir, "branch", "-M", branchName); err != nil {
		fprintErr("xx-make-pr", err)
		return 1
	}

	// Strip any incubator-only "[PR #N] " subject prefix that may have come
	// in via cherry-picking an already-marked incubator commit.
	stripExec := `git log -1 --format=%B | sed "1s/^\[PR #[0-9]*\] //" | git commit --amend -F -`
	if err := gitRun(dir, "rebase", upstreamRef, "--exec", stripExec); err != nil {
		fprintErr("xx-make-pr", err)
		return 1
	}

	if err := gitRun(dir, "push", upstreamRemote, branchName); err != nil {
		fprintErr("xx-make-pr", err)
		return 1
	}

	if len(commits) == 1 {
		subject, err := gitCapture(dir, "log", "-1", "--pretty=format:%s")
		if err != nil {
			fprintErr("xx-make-pr", err)
			return 1
		}
		title := fmt.Sprintf("(%s) %s", upstreamBranch, subject)
		if err := createPR(dir, branchName, []string{
			"-a", "@me", "--fill", "--title", title,
			"-B", upstreamBranch, "-H", branchName, "--reviewer", reviewers,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "xx-make-pr: gh pr create failed after retries. Branch '%s' is already pushed — create the PR manually: gh pr create -B %s -H %s --reviewer %s\n",
				branchName, upstreamBranch, branchName, reviewers)
			return 1
		}
	} else {
		tmp, err := os.CreateTemp("", "xx-make-pr-*.md")
		if err != nil {
			fmt.Fprintln(os.Stderr, "xx-make-pr:", err)
			return 1
		}
		defer os.Remove(tmp.Name())
		fmt.Fprintln(tmp, "# Pull Request description (edit below, lines starting with # are ignored)")
		fmt.Fprintln(tmp)
		tmp.Close()
		logArgs := append([]string{"log", "--format=%h %s"}, commits...)
		out, err := gitCapture(dir, logArgs...)
		if err != nil {
			fprintErr("xx-make-pr", err)
			return 1
		}
		f, err := os.OpenFile(tmp.Name(), os.O_APPEND|os.O_WRONLY, 0o644)
		if err != nil {
			fmt.Fprintln(os.Stderr, "xx-make-pr:", err)
			return 1
		}
		fmt.Fprintln(f, out)
		f.Close()

		editor := os.Getenv("EDITOR")
		if editor == "" {
			editor = "vi"
		}
		editCmd := exec.Command(editor, tmp.Name())
		editCmd.Stdin = os.Stdin
		editCmd.Stdout = os.Stdout
		editCmd.Stderr = os.Stderr
		_ = editCmd.Run() // matches bash: editor failure isn't checked

		title := fmt.Sprintf("(%s) PR: %s", upstreamBranch, strings.Join(commits, " "))
		if err := createPR(dir, branchName, []string{
			"-a", "@me", "--title", title, "--body-file", tmp.Name(),
			"-B", upstreamBranch, "-H", branchName, "--reviewer", reviewers,
		}); err != nil {
			fmt.Fprintf(os.Stderr, "xx-make-pr: gh pr create failed after retries. Branch '%s' is already pushed — create the PR manually: gh pr create -B %s -H %s --reviewer %s\n",
				branchName, upstreamBranch, branchName, reviewers)
			return 1
		}
	}

	prURL, err := ghRetryCapture(5, "pr", "view", "--json", "url", "-q", ".url", branchName)
	if err != nil || prURL == "" {
		fmt.Fprintf(os.Stderr, "xx-make-pr: could not resolve PR URL for '%s' (gh pr view failed). The PR may exist; check: gh pr view %s\n", branchName, branchName)
		return 1
	}
	prNumber := extractTrailingDigits(prURL)

	if mode == "incubator" {
		if err := gitRun(dir, "checkout", incubatorBranch); err != nil {
			fprintErr("xx-make-pr", err)
			return 1
		}
		for range commits {
			markExec := fmt.Sprintf(`git log --format=%%B -1 HEAD | sed "1s/^/[PR #%s] /" | git commit --amend -F - --trailer "PR: %s"`, prNumber, prURL)
			if err := gitRun(dir, "rebase", "-i", "--autosquash", "--keep-empty", "--exec", markExec); err != nil {
				fprintErr("xx-make-pr", err)
				return 1
			}
		}
	} else {
		if err := gitRun(dir, "checkout", branchName); err != nil {
			fprintErr("xx-make-pr", err)
			return 1
		}
		markExec := fmt.Sprintf(`git log --format=%%B -1 HEAD | sed "1s/^/[PR #%s] /" | git commit --amend -F - --trailer "PR: %s"`, prNumber, prURL)
		if err := gitRun(dir, "rebase", upstreamRef, "--exec", markExec); err != nil {
			fprintErr("xx-make-pr", err)
			return 1
		}
		if err := gitRun(dir, "checkout", incubatorBranch); err != nil {
			fprintErr("xx-make-pr", err)
			return 1
		}
		if err := gitRun(dir, "rebase", branchName); err != nil {
			fprintErr("xx-make-pr", err)
			return 1
		}
	}

	if err := gitRun(dir, "checkout", incubatorBranch); err != nil {
		fprintErr("xx-make-pr", err)
		return 1
	}
	if err := gitRun(dir, "branch", "-D", branchName); err != nil {
		fprintErr("xx-make-pr", err)
		return 1
	}

	fmt.Printf("Done. PR created: %s\n", prURL)
	return 0
}

var nonAlnum = regexp.MustCompile(`[^a-z0-9]+`)

// slugify mirrors bash's
// `tr '[:upper:]' '[:lower:]' | tr -cs 'a-z0-9' '-'` — lowercase, then
// squeeze every run of non-alphanumeric characters into a single '-'.
func slugify(s string) string {
	return nonAlnum.ReplaceAllString(strings.ToLower(s), "-")
}

var trailingDigits = regexp.MustCompile(`[0-9]+$`)

func extractTrailingDigits(s string) string {
	return trailingDigits.FindString(s)
}

// createPR mirrors bash's create_pr(): retry `gh pr create`, tolerating a
// transient failure by checking whether a PR already exists for the branch
// (a prior attempt or a re-run) before giving up.
func createPR(dir, branchName string, ghArgs []string) error {
	const max = 5
	for tries := 0; ; {
		args := append([]string{"pr", "create"}, ghArgs...)
		if err := gitStyleGHRun(dir, args...); err == nil {
			return nil
		}
		if _, err := runGHQuiet("pr", "view", branchName, "--json", "url"); err == nil {
			fmt.Fprintf(os.Stderr, "xx-make-pr: a PR already exists for %s, continuing.\n", branchName)
			return nil
		}
		tries++
		if tries >= max {
			return fmt.Errorf("gh pr create failed after %d attempts", max)
		}
		wait := time.Duration(tries*3) * time.Second
		fmt.Fprintf(os.Stderr, "xx-make-pr: 'gh pr create' failed (attempt %d/%d), retrying in %s...\n", tries, max, wait)
		time.Sleep(wait)
	}
}

// gitStyleGHRun runs `gh <args...>` with dir as its working directory (so
// gh's own repo auto-detection matches the bash original running from
// inside the clone) and stdio passed through.
func gitStyleGHRun(dir string, args ...string) error {
	cmd := exec.Command("gh", args...)
	cmd.Dir = dir
	cmd.Stdin = os.Stdin
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// ghRetryCapture mirrors bash's gh_retry(): retry a `gh` call through
// transient failures (sporadic 401/5xx), sleeping tries*2 seconds between
// attempts, returning stdout on eventual success.
func ghRetryCapture(max int, args ...string) (string, error) {
	for tries := 0; ; {
		out, err := runGH(args...)
		if err == nil {
			return trimTrailingNewline(string(out)), nil
		}
		tries++
		if tries >= max {
			return "", err
		}
		time.Sleep(time.Duration(tries*2) * time.Second)
	}
}
