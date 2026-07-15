// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Go port of scripts/backport-commit: back-port a single master commit to
// one release line's incubator and open the PR — refresh the agent clone,
// cherry-pick -x (falling back to a path-remapped apply for the
// Xext/<ext>/ <-> <ext>/ directory reorg between master and older
// releases), then hand off to xx-make-pr.
//
// PARITY NOTE (disclosed divergence): the bash original applies its path
// remap to the diff text with `sed "s#$mp#$tp#g;..."`, i.e. $mp is
// interpreted as a *basic regular expression* (so e.g. a literal '.' in a
// path matches any character). This port uses a literal strings.ReplaceAll
// per remapped path instead — behaviourally identical for every real path
// in this tree (plain word/word/word.c names, no regex metacharacters), but
// deliberately not reproducing the BRE-matching edge case, which is safer
// and more predictable. Flagging this rather than silently matching or
// silently "fixing" it without a note.
package ghpr

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

const backportCommitUsage = `usage: starfleetctl backport-commit <release> <commit-ish|PR-number> [agent-name]
`

// RunBackportCommit implements `starfleetctl backport-commit`.
func RunBackportCommit(root string, args []string) int {
	if len(args) >= 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Print(backportCommitUsage)
		return 0
	}
	if len(args) < 2 {
		fmt.Fprint(os.Stderr, backportCommitUsage)
		return 2
	}
	rel := args[0]
	what := args[1]
	name := "default"
	if len(args) >= 3 {
		name = args[2]
	}

	// 1. refresh/create the isolated agent clone (lands on rfc/backport-<rel>)
	dest, err := EnsureAgentClone(root, rel, name, os.Stdout)
	if err != nil {
		fprintErr("backport-commit", err)
		return 1
	}

	originURL := gitConfigGet(dest, "remote.origin.url")
	repoSlug := parseGitHubRepoSlug(originURL)
	upRemote := gitConfigGet(dest, "make-pr.upstream-remote")

	// 2. resolve a PR number to its merge commit; otherwise use the arg verbatim
	var sha string
	if prNumberRE.MatchString(what) {
		prNum := strings.TrimPrefix(what, "#")
		out, err := runGH("pr", "view", prNum, "-R", repoSlug, "--json", "mergeCommit", "--jq", ".mergeCommit.oid")
		if err != nil {
			fprintErr("backport-commit", err)
			return 1
		}
		sha = trimTrailingNewline(string(out))
		if sha == "" || sha == "null" {
			fmt.Fprintf(os.Stderr, "backport-commit: PR #%s is not merged / has no merge commit\n", prNum)
			return 1
		}
		fmt.Printf("backport-commit: PR #%s -> merge commit %s\n", prNum, sha)
	} else {
		sha = what
	}

	// make sure the commit object is reachable locally
	if _, err := gitCaptureQuiet(dest, "cat-file", "-e", sha+"^{commit}"); err != nil {
		_ = gitRunSilent(dest, "fetch", upRemote, "master")
		if _, err := gitCaptureQuiet(dest, "cat-file", "-e", sha+"^{commit}"); err != nil {
			if err := gitRunErr(dest, "fetch", upRemote, sha); err != nil {
				fmt.Fprintf(os.Stderr, "backport-commit: commit not found on %s: %s\n", upRemote, sha)
				return 1
			}
		}
	}

	// 3. Apply the change onto rfc/backport-<rel>: try a normal cherry-pick
	// first; on failure, check whether it's purely the directory-reorg path
	// move and, if so, remap and apply; a genuine content conflict bails.
	if err := gitRunErr(dest, "cherry-pick", "-x", sha); err != nil {
		_ = gitRunSilent(dest, "cherry-pick", "--abort")
		fmt.Fprintln(os.Stderr, "backport-commit: cherry-pick failed; trying path-remapped apply (dir reorg)...")

		rc := applyViaPathRemap(dest, sha)
		if rc != 0 {
			return rc
		}
		fmt.Fprintln(os.Stderr, "backport-commit: applied via path remap.")
	}

	newSHA, err := gitCapture(dest, "rev-parse", "HEAD")
	if err != nil {
		fprintErr("backport-commit", err)
		return 1
	}
	fmt.Printf("backport-commit: prepared %s on rfc/backport-%s\n", newSHA, rel)
	if err := gitRun(dest, "show", "--stat", newSHA); err != nil {
		fprintErr("backport-commit", err)
		return 1
	}

	// 4. submit the PR from inside the agent clone (xx-make-pr reads cwd config)
	return RunXXMakePR(dest, []string{newSHA})
}

// applyViaPathRemap implements the cherry-pick fallback: locate each file
// touched by sha by basename on dest's current branch, remap the diff's
// paths, and apply it, reconstructing the same commit (author, message,
// cherry-picked-from provenance). Returns 0 on success, or the exit code
// backport-commit should return (3 for the documented "do it manually"
// cases, matching the bash original's exit codes).
func applyViaPathRemap(dest, sha string) int {
	namesOut, err := gitCapture(dest, "show", "--format=", "--name-only", sha)
	if err != nil {
		fprintErr("backport-commit", err)
		return 1
	}

	type remap struct{ from, to string }
	var remaps []remap
	remapped := false

	for _, mp := range strings.Split(namesOut, "\n") {
		if mp == "" {
			continue
		}
		var tp string
		if _, err := gitCaptureQuiet(dest, "ls-files", "--error-unmatch", mp); err == nil {
			tp = mp // same path exists on this branch
		} else {
			base := filepath.Base(mp)
			candOut, _ := gitCapture(dest, "ls-files", "--", "*/"+base, base)
			tp = pickBestSuffixMatch(candOut, mp)
			if tp == "" {
				fmt.Fprintf(os.Stderr, "backport-commit: cannot uniquely locate '%s' on this release (missing or ambiguous) — do it manually in %s\n", base, dest)
				return 3
			}
		}
		if tp != mp {
			remaps = append(remaps, remap{mp, tp})
			remapped = true
			fmt.Fprintf(os.Stderr, "  remap: %s -> %s\n", mp, tp)
		}
	}

	if !remapped {
		fmt.Fprintf(os.Stderr, "backport-commit: not a path reorg - code differs. Do a manual/adapted backport in %s\n", dest)
		return 3
	}

	diffOut, err := gitCapture(dest, "show", "--format=", sha)
	if err != nil {
		fprintErr("backport-commit", err)
		return 1
	}
	for _, r := range remaps {
		diffOut = strings.ReplaceAll(diffOut, r.from, r.to)
	}
	diff := diffOut + "\n"

	if err := gitApplyStdinQuiet(dest, diff, "--index", "--check", "-"); err != nil {
		fmt.Fprintf(os.Stderr, "backport-commit: path-remapped diff still doesn't apply (code differs) — do a manual/adapted backport in %s\n", dest)
		return 3
	}
	if err := gitApplyStdin(dest, diff, "--index", "-"); err != nil {
		fprintErr("backport-commit", err)
		return 1
	}

	msg, err := gitCapture(dest, "log", "-1", "--format=%B", sha)
	if err != nil {
		fprintErr("backport-commit", err)
		return 1
	}
	author, err := gitCapture(dest, "log", "-1", "--format=%an <%ae>", sha)
	if err != nil {
		fprintErr("backport-commit", err)
		return 1
	}

	tmp, err := os.CreateTemp("", "backport-commit-msg-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "backport-commit:", err)
		return 1
	}
	defer os.Remove(tmp.Name())
	fmt.Fprintf(tmp, "%s\n(cherry picked from commit %s)\n", msg, sha)
	tmp.Close()

	if err := gitRunErr(dest, "commit", "--quiet", "--author="+author, "-F", tmp.Name()); err != nil {
		fprintErr("backport-commit", err)
		return 1
	}
	return 0
}

// pickBestSuffixMatch mirrors the bash original's awk helper: among
// candidates (one per line), pick the one sharing the longest trailing
// path-segment run with mp; returns "" if the choice is ambiguous (tied, or
// no candidates).
func pickBestSuffixMatch(candidatesOut, mp string) string {
	best := 0
	pick := ""
	for _, c := range strings.Split(candidatesOut, "\n") {
		if c == "" {
			continue
		}
		s := suffixSegmentMatch(c, mp)
		if s > best {
			best = s
			pick = c
		} else if s == best {
			pick = pick + " " + c
		}
	}
	if strings.Contains(pick, " ") {
		return ""
	}
	return pick
}

// suffixSegmentMatch counts how many trailing '/'-separated path segments a
// and b share, working backward from the end until the first mismatch.
func suffixSegmentMatch(a, b string) int {
	as := strings.Split(a, "/")
	bs := strings.Split(b, "/")
	na, nb := len(as), len(bs)
	n := 0
	for na-n > 0 && nb-n > 0 && as[na-1-n] == bs[nb-1-n] {
		n++
	}
	return n
}

// gitApplyStdin runs `git -C dest apply <args...>` feeding diff on stdin,
// with stderr passed through (mirrors the bash original's un-redirected
// real `git apply --index -` call).
func gitApplyStdin(dest, diff string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", dest, "apply"}, args...)...)
	cmd.Stdin = strings.NewReader(diff)
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// gitApplyStdinQuiet is gitApplyStdin with stderr discarded — mirrors the
// bash original's `--check ... 2>/dev/null` probe.
func gitApplyStdinQuiet(dest, diff string, args ...string) error {
	cmd := exec.Command("git", append([]string{"-C", dest, "apply"}, args...)...)
	cmd.Stdin = strings.NewReader(diff)
	return cmd.Run()
}
