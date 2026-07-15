// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package ghpr is the Go port of the read-only GitHub-interaction scripts
// (scripts/pr-view, pr-ci, show-branch-file, backport-applies,
// show-pr-conflict) — DASHBOARD.md "starfleetctl" row, Phase 2. Each script
// becomes its own top-level starfleetctl subcommand (not grouped behind a
// single "gh" verb) since none of them share state or a lock file the way
// the fleet-coordination packages (agentbus/prclaim/wscommit) do — they are
// stateless, read-only wrappers around `gh`.
//
// All of them still shell out to the `gh` CLI rather than talking to the
// GitHub REST API directly: `gh` already owns auth (gh auth login) and
// config, and re-implementing that in Go buys nothing (see the DASHBOARD
// "starfleetctl" row's own reasoning: gh CLI quirks are external and would
// need re-encoding either way). What Go DOES eliminate is the brittle half
// of the bash originals — jq/grep/sed post-processing of gh's JSON — by
// parsing with encoding/json and formatting natively instead.
package ghpr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strings"
)

// prNumberRE matches a (possibly '#'-prefixed) numeric PR/issue id. Shared
// by every subcommand that injects a user-supplied PR number into a GitHub
// REST path, so a non-numeric or path-traversal value (e.g. "123/foo") can
// never reach the `gh api` URL.
var prNumberRE = regexp.MustCompile(`^#?[0-9]+$`)

// validPR normalizes a user-supplied PR/issue identifier: it strips a
// leading '#' and returns the bare number. It returns an error if the value
// is not a pure numeric id, so callers can refuse malformed input before
// building a REST path from it.
func validPR(pr string) (string, error) {
	if !prNumberRE.MatchString(pr) {
		return "", fmt.Errorf("ghpr: invalid PR number %q (expected a numeric id, e.g. 123 or #123)", pr)
	}
	return strings.TrimPrefix(pr, "#"), nil
}

// Repo resolves the GitHub repo slug from $STARFLEET_GITHUB_REPO or (deprecated) $REPO.
// Returns an error if neither is set, so callers can choose a fallback (e.g. URL
// override) before giving up.
func Repo() (string, error) {
	if r := os.Getenv("STARFLEET_GITHUB_REPO"); r != "" {
		return r, nil
	}
	if r := os.Getenv("REPO"); r != "" {
		return r, nil
	}
	return "", fmt.Errorf("no GitHub repo: set $STARFLEET_GITHUB_REPO or (deprecated) $REPO")
}

// repo is the CLI convenience helper: calls Repo() and exits on failure.
func repo() string {
	r, err := Repo()
	if err != nil {
		fprintErr("ghpr", err)
		os.Exit(1)
	}
	return r
}

// runGH execs `gh <args...>` and returns its stdout. Mirrors the bash
// scripts' bare `gh ...` calls: stderr is passed through directly (so auth
// prompts / rate-limit errors reach the user the same way), only stdout is
// captured for parsing.
func runGH(args ...string) ([]byte, error) {
	cmd := exec.Command("gh", args...)
	cmd.Stderr = os.Stderr
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	return out.Bytes(), err
}

// ghJSON runs `gh <args...>` and decodes its stdout as JSON into v.
func ghJSON(v any, args ...string) error {
	out, err := runGH(args...)
	if err != nil {
		return err
	}
	return json.Unmarshal(out, v)
}

// runGHQuiet is runGH with stderr discarded — for speculative lookups (e.g.
// show-branch-file's candidate-path probing) where a 404 is an expected,
// silent outcome, not an error to surface. Mirrors the bash originals'
// `2>/dev/null` on the same probe calls.
func runGHQuiet(args ...string) ([]byte, error) {
	cmd := exec.Command("gh", args...)
	var out bytes.Buffer
	cmd.Stdout = &out
	err := cmd.Run()
	return out.Bytes(), err
}

func fprintErr(cmd string, err error) {
	fmt.Fprintf(os.Stderr, "%s: %v\n", cmd, err)
}
