// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package util

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"
	"time"
)

// GH represents a GitHub API client using the `gh` CLI.
type GH struct {
	// Timeout for gh commands
	Timeout time.Duration
}

// NewGH creates a new GH client.
func NewGH() *GH {
	return &GH{
		Timeout: 30 * time.Second,
	}
}

// WithTimeout sets the command timeout.
func (gh *GH) WithTimeout(d time.Duration) *GH {
	gh.Timeout = d
	return gh
}

// Run executes a gh command and returns stdout.
func (gh *GH) Run(args ...string) (string, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gh.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr

	var out bytes.Buffer
	cmd.Stdout = &out

	err := cmd.Run()
	return TrimTrailingNewline(out.String()), err
}

// RunJSON executes a gh command that outputs JSON and unmarshals it into v.
func (gh *GH) RunJSON(v interface{}, args ...string) error {
	out, err := gh.Run(args...)
	if err != nil {
		return err
	}
	return json.Unmarshal([]byte(out), v)
}

// RunRaw executes a gh command and returns raw stdout bytes.
func (gh *GH) RunRaw(args ...string) ([]byte, error) {
	ctx, cancel := context.WithTimeout(context.Background(), gh.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Stdin = os.Stdin
	cmd.Stderr = os.Stderr

	return cmd.Output()
}

// API makes a direct GitHub REST API call using `gh api`.
func (gh *GH) API(method, path string, body io.Reader, headers map[string]string) ([]byte, error) {
	args := []string{"api", "--method", method, path}
	if body != nil {
		args = append(args, "--input", "-")
	}
	for k, v := range headers {
		args = append(args, "-H", k+": "+v)
	}

	ctx, cancel := context.WithTimeout(context.Background(), gh.Timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, "gh", args...)
	cmd.Stdin = body
	cmd.Stderr = os.Stderr

	var out bytes.Buffer
	cmd.Stdout = &out

	err := cmd.Run()
	return out.Bytes(), err
}

// APIJSON makes a direct GitHub REST API call and unmarshals the JSON response.
func (gh *GH) APIJSON(v interface{}, method, path string, body io.Reader, headers map[string]string) error {
	data, err := gh.API(method, path, body, headers)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, v)
}

// PRView fetches PR metadata using `gh pr view`.
func (gh *GH) PRView(prNumber int, repo string, fields ...string) (map[string]interface{}, error) {
	args := []string{"pr", "view", fmt.Sprintf("%d", prNumber)}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	if len(fields) > 0 {
		args = append(args, "--json", strings.Join(fields, ","))
	} else {
		args = append(args, "--json", "number,title,state,author,baseRefName,headRefName,url,createdAt,updatedAt,mergeable,mergeStateStatus")
	}
	var result map[string]interface{}
	err := gh.RunJSON(&result, args...)
	return result, err
}

// PRList lists PRs using `gh pr list`.
func (gh *GH) PRList(repo string, state string, limit int) ([]map[string]interface{}, error) {
	args := []string{"pr", "list", "--json", "number,title,state,author,baseRefName,headRefName,url,createdAt,updatedAt"}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	if state != "" {
		args = append(args, "--state", state)
	}
	if limit > 0 {
		args = append(args, "--limit", fmt.Sprintf("%d", limit))
	}
	var result []map[string]interface{}
	err := gh.RunJSON(&result, args...)
	return result, err
}

// PRChecks fetches CI status for a PR using `gh pr checks`.
func (gh *GH) PRChecks(prNumber int, repo string) (map[string]interface{}, error) {
	args := []string{"pr", "checks", fmt.Sprintf("%d", prNumber), "--json", "name,status,conclusion,startedAt,completedAt,detailsUrl"}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	var result map[string]interface{}
	err := gh.RunJSON(&result, args...)
	return result, err
}

// PRComment adds a comment to a PR using `gh pr comment`.
func (gh *GH) PRComment(prNumber int, repo, body string) error {
	args := []string{"pr", "comment", fmt.Sprintf("%d", prNumber), "--body", body}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	_, err := gh.Run(args...)
	return err
}

// PRLabels adds labels to a PR using `gh pr edit --add-label`.
func (gh *GH) PRLabels(prNumber int, repo string, labels []string) error {
	args := []string{"pr", "edit", fmt.Sprintf("%d", prNumber)}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	for _, label := range labels {
		args = append(args, "--add-label", label)
	}
	_, err := gh.Run(args...)
	return err
}

// PRRemoveLabels removes labels from a PR using `gh pr edit --remove-label`.
func (gh *GH) PRRemoveLabels(prNumber int, repo string, labels []string) error {
	args := []string{"pr", "edit", fmt.Sprintf("%d", prNumber)}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	for _, label := range labels {
		args = append(args, "--remove-label", label)
	}
	_, err := gh.Run(args...)
	return err
}

// PRCheckout checks out a PR locally using `gh pr checkout`.
func (gh *GH) PRCheckout(prNumber int, repo string) error {
	args := []string{"pr", "checkout", fmt.Sprintf("%d", prNumber)}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	_, err := gh.Run(args...)
	return err
}

// PRCreate creates a PR using `gh pr create`.
func (gh *GH) PRCreate(repo, title, body, base, head string, draft bool) (string, error) {
	args := []string{"pr", "create", "--title", title, "--body", body}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	if base != "" {
		args = append(args, "--base", base)
	}
	if head != "" {
		args = append(args, "--head", head)
	}
	if draft {
		args = append(args, "--draft")
	}
	return gh.Run(args...)
}

// RepoView fetches repository metadata using `gh repo view`.
func (gh *GH) RepoView(repo string) (map[string]interface{}, error) {
	args := []string{"repo", "view", "--json", "name,owner,description,url,isPrivate,defaultBranchRef"}
	if repo != "" {
		args = append(args, repo)
	}
	var result map[string]interface{}
	err := gh.RunJSON(&result, args...)
	return result, err
}

// IssueView fetches issue metadata using `gh issue view`.
func (gh *GH) IssueView(issueNumber int, repo string) (map[string]interface{}, error) {
	args := []string{"issue", "view", fmt.Sprintf("%d", issueNumber), "--json", "number,title,state,author,url,createdAt,updatedAt,labels,assignees"}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	var result map[string]interface{}
	err := gh.RunJSON(&result, args...)
	return result, err
}

// WorkflowRunList lists workflow runs using `gh run list`.
func (gh *GH) WorkflowRunList(repo string, limit int) ([]map[string]interface{}, error) {
	args := []string{"run", "list", "--json", "databaseId,name,status,conclusion,createdAt,headBranch,headSha,url"}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	if limit > 0 {
		args = append(args, "--limit", fmt.Sprintf("%d", limit))
	}
	var result []map[string]interface{}
	err := gh.RunJSON(&result, args...)
	return result, err
}

// WorkflowRunView views a workflow run using `gh run view`.
func (gh *GH) WorkflowRunView(runID int, repo string) (map[string]interface{}, error) {
	args := []string{"run", "view", fmt.Sprintf("%d", runID), "--json", "databaseId,name,status,conclusion,createdAt,headBranch,headSha,url,jobs"}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	var result map[string]interface{}
	err := gh.RunJSON(&result, args...)
	return result, err
}

// WorkflowRunRerun reruns a workflow using `gh run rerun`.
func (gh *GH) WorkflowRunRerun(runID int, repo string, failedOnly bool) error {
	args := []string{"run", "rerun", fmt.Sprintf("%d", runID)}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	if failedOnly {
		args = append(args, "--failed")
	}
	_, err := gh.Run(args...)
	return err
}

// ReleaseList lists releases using `gh release list`.
func (gh *GH) ReleaseList(repo string, limit int) ([]map[string]interface{}, error) {
	args := []string{"release", "list", "--json", "name,tagName,isDraft,isPrerelease,createdAt,url"}
	if repo != "" {
		args = append(args, "--repo", repo)
	}
	if limit > 0 {
		args = append(args, "--limit", fmt.Sprintf("%d", limit))
	}
	var result []map[string]interface{}
	err := gh.RunJSON(&result, args...)
	return result, err
}

// AuthStatus checks authentication status using `gh auth status`.
func (gh *GH) AuthStatus() (string, error) {
	return gh.Run("auth", "status")
}

// IsAuthenticated checks if gh is authenticated.
func (gh *GH) IsAuthenticated() bool {
	_, err := gh.AuthStatus()
	return err == nil
}
