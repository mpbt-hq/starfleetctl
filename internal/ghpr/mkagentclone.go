// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Go port of scripts/mk-agent-clone: create (or refresh) a dedicated
// agent-owned clone for backport work on a release line, isolated from the
// user's hand-edited clone but sharing its git object store via alternates.
//
// Exposed as EnsureAgentClone so RunPRCheckout/RunBackportCommit can call it
// directly instead of shelling out to the bash script — this was the one
// remaining bash dependency blocking those two from being fully
// self-contained Go (see DASHBOARD.md "starfleetctl" row).
package ghpr

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
)

const mkAgentCloneUsage = `usage: starfleetctl mk-agent-clone <release> [name]   (e.g. mk-agent-clone 25.2)
`

// RunMkAgentClone implements `starfleetctl mk-agent-clone`.
func RunMkAgentClone(root string, args []string) int {
	if len(args) >= 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Print(mkAgentCloneUsage)
		return 0
	}
	if len(args) < 1 {
		fmt.Fprint(os.Stderr, mkAgentCloneUsage)
		return 2
	}
	rel := args[0]
	name := "default"
	if len(args) >= 2 {
		name = args[1]
	}
	if _, err := EnsureAgentClone(root, rel, name, os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, "mk-agent-clone:", err)
		return 1
	}
	return 0
}

// EnsureAgentClone creates or refreshes the agent clone for release rel
// (agent name "name"), landing it on the shared rfc/backport-<rel>
// incubator branch, and returns its path. Mirrors scripts/mk-agent-clone
// exactly, including its progress output — which callers redirect
// differently (unredirected for the standalone CLI and backport-commit,
// `>&2`-style for pr-checkout), so it's routed through the given stdout
// writer rather than hardcoded to os.Stdout.
func EnsureAgentClone(root, rel, name string, stdout io.Writer) (string, error) {
	ref := filepath.Join(root, "_WORK_", "xserver-"+rel, "sources", "xlibre", "xserver")
	dest := filepath.Join(root, "_WORK_", "xserver-"+rel, "agent", name, "xserver")
	incubator := "rfc/backport-" + rel

	if fi, err := os.Stat(filepath.Join(ref, ".git")); err != nil || !fi.IsDir() {
		return "", fmt.Errorf("reference clone not found: %s", ref)
	}

	url, err := gitCapture(ref, "remote", "get-url", "origin")
	if err != nil {
		return "", err
	}
	upBranch := gitConfigGet(ref, "make-pr.upstream-branch")

	if fi, err := os.Stat(filepath.Join(dest, ".git")); err == nil && fi.IsDir() {
		fmt.Fprintf(stdout, "mk-agent-clone: agent clone exists; fetching -> %s\n", dest)
		if err := gitRunTo(dest, stdout, "fetch", "origin", "--prune"); err != nil {
			return "", err
		}
	} else {
		if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
			return "", err
		}
		// origin = real upstream (PRs push to GitHub); objects borrowed
		// from the reference clone via --reference (alternates).
		if err := runPassthroughTo("git", stdout, "clone", "--reference", ref, url, dest); err != nil {
			return "", err
		}
	}

	// always (re)apply — idempotent, so a refresh repairs config drift too
	if err := copyConfigSection(ref, dest, `make-pr\.`); err != nil {
		return "", err
	}
	if err := copyConfigSection(ref, dest, `remote\.`); err != nil {
		return "", err
	}
	// pick up namespaced tags / the xorg remote per the just-copied refspecs
	if err := gitRunTo(dest, stdout, "fetch", "origin", "--prune"); err != nil {
		return "", err
	}

	// land on the shared incubator branch (track the remote one if it
	// exists, otherwise fork it from the upstream release branch)
	if err := gitRunSilent(dest, "ls-remote", "--exit-code", "--heads", "origin", incubator); err == nil {
		if err := gitRunTo(dest, stdout, "checkout", "-B", incubator, "origin/"+incubator); err != nil {
			return "", err
		}
	} else {
		if err := gitRunTo(dest, stdout, "checkout", "-B", incubator, "origin/"+upBranch); err != nil {
			return "", err
		}
	}

	upRemote := gitConfigGet(dest, "make-pr.upstream-remote")
	fmt.Fprintf(stdout, "mk-agent-clone: ready -> %s\n", dest)
	fmt.Fprintf(stdout, "  branch: %s   upstream: %s/%s\n", incubator, upRemote, upBranch)
	fmt.Fprintf(stdout, "  objects shared from: %s\n", ref)
	return dest, nil
}

// copyConfigSection reproduces the mpbt-set-up config (make-pr.*,
// remote.*) from ref into dest by copying whole sections verbatim, rather
// than re-encoding individual keys — mirrors bash's copy_cfg_section():
// drop dest's existing keys under prefix first (so multi-valued keys like
// remote.origin.fetch don't accumulate duplicates across refreshes), then
// re-add every value from ref in order.
func copyConfigSection(ref, dest, prefix string) error {
	existing, _ := gitCaptureQuiet(dest, "config", "--name-only", "--get-regexp", "^"+prefix)
	seen := map[string]bool{}
	for _, k := range strings.Split(existing, "\n") {
		if k == "" || seen[k] {
			continue
		}
		seen[k] = true
		_ = gitRunSilent(dest, "config", "--unset-all", k)
	}

	out, err := gitConfigGetRegexpNUL(ref, "^"+prefix)
	if err != nil {
		// no matches is the normal case for a fresh clone (no make-pr.*
		// yet) — mirrors bash's `|| true` swallowing get-regexp's exit 1.
		return nil
	}
	for _, entry := range out {
		key, val, ok := strings.Cut(entry, "\n")
		if !ok {
			key, val = entry, ""
		}
		if err := gitRunSilent(dest, "config", "--add", key, val); err != nil {
			return fmt.Errorf("config --add %s: %w", key, err)
		}
	}
	return nil
}

// gitConfigGetRegexpNUL runs `git config --null --get-regexp <pattern>` in
// dir and splits the NUL-delimited output into entries (each "key\nvalue").
func gitConfigGetRegexpNUL(dir, pattern string) ([]string, error) {
	out, err := gitCaptureNUL(dir, "config", "--null", "--get-regexp", pattern)
	if err != nil {
		return nil, err
	}
	var entries []string
	for _, e := range strings.Split(out, "\x00") {
		if e != "" {
			entries = append(entries, e)
		}
	}
	return entries, nil
}
