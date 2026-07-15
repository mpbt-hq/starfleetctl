// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Go port of scripts/pr-set-body and scripts/pr-append-body: set (or
// fetch-append-set) a PR body via the REST API PATCH, working around `gh pr
// edit`'s "Projects classic deprecation" GraphQL error on this repo.
package ghpr

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const prSetBodyUsage = `usage: starfleetctl pr-set-body <pr-number> <body-file>
env: STARFLEET_GITHUB_REPO   repo slug (or $REPO for backward compat)
`

// RunPRSetBody implements `starfleetctl pr-set-body`.
func RunPRSetBody(args []string) int {
	if len(args) >= 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Print(prSetBodyUsage)
		return 0
	}
	if len(args) < 2 {
		fmt.Fprint(os.Stderr, prSetBodyUsage)
		return 2
	}
	pr := args[0]
	file := args[1]
	return setPRBody(pr, file)
}

// setPRBody implements the actual PATCH, shared with RunPRAppendBody so the
// latter doesn't need to shell out to its own binary the way the bash
// version re-invokes scripts/pr-set-body as a subprocess.
func setPRBody(prArg, file string) int {
	pr, err := validPR(prArg)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		return 2
	}

	if _, err := os.Stat(file); err != nil {
		fmt.Fprintf(os.Stderr, "pr-set-body: no such file: %s\n", file)
		return 1
	}

	// gh's -F flag reads the value from a file itself when prefixed with
	// '@' — pass that through literally, exactly like the bash original,
	// rather than reading the file ourselves.
	cmd := exec.Command("gh", "api", "--method", "PATCH", "repos/"+repo()+"/pulls/"+pr,
		"-F", "body=@"+file, "--jq", ".html_url")
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		fprintErr("pr-set-body", err)
		return 1
	}
	return 0
}

const prAppendBodyUsage = `usage: starfleetctl pr-append-body <pr#> <text-file>
env: STARFLEET_GITHUB_REPO   repo slug (or $REPO for backward compat)
`

// RunPRAppendBody implements `starfleetctl pr-append-body`.
func RunPRAppendBody(args []string) int {
	if len(args) >= 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Print(prAppendBodyUsage)
		return 0
	}
	if len(args) < 2 {
		fmt.Fprint(os.Stderr, prAppendBodyUsage)
		return 2
	}
	pr := strings.TrimPrefix(args[0], "#")
	file := args[1]

	if _, err := os.Stat(file); err != nil {
		fmt.Fprintf(os.Stderr, "pr-append-body: no such file: %s\n", file)
		return 1
	}

	current, err := runGH("api", "repos/"+repo()+"/pulls/"+pr, "--jq", ".body")
	if err != nil {
		fprintErr("pr-append-body", err)
		return 1
	}
	addition, err := os.ReadFile(file)
	if err != nil {
		fmt.Fprintln(os.Stderr, "pr-append-body:", err)
		return 1
	}

	tmp, err := os.CreateTemp("", "pr-append-body-*.md")
	if err != nil {
		fmt.Fprintln(os.Stderr, "pr-append-body:", err)
		return 1
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(current); err != nil {
		tmp.Close()
		fmt.Fprintln(os.Stderr, "pr-append-body:", err)
		return 1
	}
	if _, err := tmp.Write(addition); err != nil {
		tmp.Close()
		fmt.Fprintln(os.Stderr, "pr-append-body:", err)
		return 1
	}
	tmp.Close()

	return setPRBody(pr, tmp.Name())
}
