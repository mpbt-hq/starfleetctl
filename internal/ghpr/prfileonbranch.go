// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Fetch and display a file from a remote branch via the GitHub contents API.
package ghpr

import (
	"encoding/base64"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

type contentsResponse struct {
	Type     string `json:"type"`
	Content  string `json:"content"`
	Encoding string `json:"encoding"`
	Name     string `json:"name"`
	Path     string `json:"path"`
	Size     int    `json:"size"`
}

const prFileOnBranchUsage = `usage: starfleetctl pr file-on-branch <branch> <path>
  Fetch a file from a remote branch and print its decoded content.
  Branch can be a ref name (e.g. "release/25.2", "master", "v1.0").

  Note: the path is relative to the repo root, e.g. "README.md"
        or ".github/workflows/build-xserver.yml".
`

func RunPRFileOnBranch(args []string) int {
	branch := ""
	path := ""

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			fmt.Print(prFileOnBranchUsage)
			return 0
		default:
			if strings.HasPrefix(args[i], "-") {
				fmt.Fprintln(os.Stderr, "pr file-on-branch: unknown arg:", args[i])
				return 2
			}
			if branch == "" {
				branch = args[i]
			} else if path == "" {
				path = args[i]
			} else {
				fmt.Fprintln(os.Stderr, "pr file-on-branch: too many arguments")
				return 2
			}
		}
	}
	if branch == "" || path == "" {
		fmt.Fprintln(os.Stderr, "pr file-on-branch: need <branch> <path>")
		return 2
	}

	repoVal, err := Repo()
	if err != nil {
		fmt.Fprintln(os.Stderr, "pr file-on-branch:", err)
		return 2
	}

	raw, err := runGHQuiet("api", "repos/"+repoVal+"/contents/"+path+"?ref="+branch)
	if err != nil {
		// Check if it's a 404
		if len(raw) > 0 {
			var e struct {
				Message string `json:"message"`
			}
			if json.Unmarshal(raw, &e) == nil {
				if strings.Contains(e.Message, "Not Found") {
					fmt.Fprintf(os.Stderr, "pr file-on-branch: %s on branch %s not found\n", path, branch)
					return 1
				}
				fmt.Fprintf(os.Stderr, "pr file-on-branch: %s\n", e.Message)
				return 1
			}
		}
		fmt.Fprintf(os.Stderr, "pr file-on-branch: API error: %v\n", err)
		return 1
	}

	var resp contentsResponse
	if err := json.Unmarshal(raw, &resp); err != nil {
		fmt.Fprintf(os.Stderr, "pr file-on-branch: parsing response: %v\n", err)
		return 1
	}

	if resp.Type == "dir" {
		fmt.Fprintf(os.Stderr, "pr file-on-branch: %s is a directory, not a file\n", path)
		return 1
	}

	if resp.Encoding != "base64" {
		fmt.Fprintf(os.Stderr, "pr file-on-branch: unexpected encoding %q (expected base64)\n", resp.Encoding)
		return 1
	}

	decoded, err := base64.StdEncoding.DecodeString(resp.Content)
	if err != nil {
		fmt.Fprintf(os.Stderr, "pr file-on-branch: base64 decode: %v\n", err)
		return 1
	}

	os.Stdout.Write(decoded)
	return 0
}
