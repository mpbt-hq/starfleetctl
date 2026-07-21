// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Go port of scripts/show-branch-file: print a source file (or a
// function/symbol region) from the given repo at a given ref, straight
// from the GitHub contents API, without a local clone. Transparently
// resolves the Xext/<ext>/ <-> <ext>/ directory reorg between newer and
// older release lines: if the given path 404s, retries with a leading
// "Xext/" toggled.
package ghpr

import (
	"fmt"
	"os"
	"strconv"
	"strings"

	"github.com/metux/starfleetctl/internal/projectconfig"
)

const showBranchFileUsage = `usage: starfleetctl show-branch-file <ref> <path> [symbol]
  <ref>     branch/tag/sha, e.g. release/25.2, master
  <path>    repo path as on master, e.g. Xext/render/render.c
  [symbol]  optional literal substring; prints CONTEXT lines after each hit
            (grep -A semantics: merges overlapping blocks, "--" between others)
env:
  STARFLEET_GITHUB_REPO   repo slug (or $REPO for backward compat)
  CONTEXT                 lines of context printed after a symbol hit (default 45)
`

// fetchBranchFile fetches path (or its Xext/-toggled twin) at ref, returning
// the file content and the path that actually resolved. Mirrors
// scripts/show-branch-file's candidate-list + first-success loop.
func fetchBranchFile(ref, pathIn string) (content, used string, err error) {
	// Load project config for path remapping
	projCfg, err := projectconfig.Load("")
	if err != nil {
		// If config fails, fall back to default Xext/ remapping
		candidates := []string{pathIn}
		if strings.HasPrefix(pathIn, "Xext/") {
			candidates = append(candidates, strings.TrimPrefix(pathIn, "Xext/"))
		} else {
			candidates = append(candidates, "Xext/"+pathIn)
		}

		var lastErr error
		for _, p := range candidates {
			out, e := runGHQuiet("api", fmt.Sprintf("repos/%s/contents/%s?ref=%s", repo(), p, ref),
				"-H", "Accept: application/vnd.github.raw")
			if e == nil {
				return string(out), p, nil
			}
			lastErr = e
		}
		return "", "", fmt.Errorf("not found on %s: %s (%v)", ref, strings.Join(candidates, ", "), lastErr)
	} else {
		candidates := projCfg.GetRemapCandidates(pathIn)

		var lastErr error
		for _, p := range candidates {
			out, e := runGHQuiet("api", fmt.Sprintf("repos/%s/contents/%s?ref=%s", repo(), p, ref),
				"-H", "Accept: application/vnd.github.raw")
			if e == nil {
				return string(out), p, nil
			}
			lastErr = e
		}
		return "", "", fmt.Errorf("not found on %s: %s (%v)", ref, strings.Join(candidates, ", "), lastErr)
	}
}

// RunShowBranchFile implements `starfleetctl show-branch-file <ref> <path> [symbol]`.
func RunShowBranchFile(args []string) int {
	if len(args) >= 1 && (args[0] == "-h" || args[0] == "--help") {
		fmt.Print(showBranchFileUsage)
		return 0
	}
	if len(args) < 2 {
		fmt.Fprint(os.Stderr, showBranchFileUsage)
		return 2
	}
	ref, pathIn := args[0], args[1]
	symbol := ""
	if len(args) >= 3 {
		symbol = args[2]
	}
	context := 45
	if v := os.Getenv("CONTEXT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			context = n
		}
	}

	content, used, err := fetchBranchFile(ref, pathIn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "show-branch-file:", err)
		return 1
	}
	fmt.Fprintf(os.Stderr, "show-branch-file: %s@%s :: %s\n", repo(), ref, used)

	if symbol == "" {
		fmt.Print(content)
		if !strings.HasSuffix(content, "\n") {
			fmt.Println()
		}
		return 0
	}

	// Mirrors `grep -n -A context -e symbol`: every match gets its own
	// context block (':' marks a match line, '-' a context-only line);
	// overlapping/adjacent blocks merge without duplicating lines, and
	// non-adjacent blocks get a "--" separator, matching GNU grep exactly.
	lines := strings.Split(content, "\n")
	matched := make([]bool, len(lines))
	found := false
	for i, line := range lines {
		if strings.Contains(line, symbol) {
			matched[i] = true
			found = true
		}
	}
	if !found {
		fmt.Fprintln(os.Stderr, "show-branch-file: pattern not found:", symbol)
		return 2
	}

	printedUpTo := -1
	for i, isMatch := range matched {
		if !isMatch {
			continue
		}
		end := i + context
		if end >= len(lines) {
			end = len(lines) - 1
		}
		if printedUpTo >= 0 && i > printedUpTo+1 {
			fmt.Println("--")
		}
		from := i
		if printedUpTo+1 > from {
			from = printedUpTo + 1
		}
		for j := from; j <= end; j++ {
			marker := "-"
			if matched[j] {
				marker = ":"
			}
			fmt.Printf("%d%s%s\n", j+1, marker, lines[j])
		}
		if end > printedUpTo {
			printedUpTo = end
		}
	}
	return 0
}
