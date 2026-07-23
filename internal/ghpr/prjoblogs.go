// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Go port of scripts/pr-job-logs: fetch raw CI logs for a PR's failing jobs
// (or a specific job by id) and print a quick failure summary.
package ghpr

import (
	"bufio"
	"fmt"
	"os"
	"regexp"
	"strconv"
	"strings"
)

const prJobLogsUsage = `usage: starfleetctl pr-job-logs <pr#> [--all] [--no-grep]
       starfleetctl pr-job-logs --job <job-id> [--no-grep]
env:
  STARFLEET_GITHUB_REPO   repo slug (or $REPO for backward compat)
  OUTDIR                  directory for log files (default: a fresh temp dir)
`

var failureMarkers = []string{
	// build / link
	`FAILED:`,
	`ninja: build stopped`,
	`: error:`,
	`fatal error:`,
	`undefined reference to`,
	`collect2: error:`,
	`cannot find -l`,
	// meson configure
	`meson\.build:[0-9]+:[0-9]+: ERROR:`,
	`Dependency "[^"]*" not found`,
	`ERROR: [A-Z]`,
	// dep install
	`emerge: there are no ebuilds`,
	`!!! `,                             // portage error lines
	`ERROR: unable to select packages`, // apk (alpine)
	`E: `,                              // apt (debian/ubuntu)
	`error: target not found`,          // pacman (arch)
	`No match for argument`,            // dnf (fedora)
	// test phase
	`Summary of Failures`,
	`Fail:[[:space:]]+[1-9]`,
	`Caught signal`,
	`Segmentation fault`,
	// generic GitHub Actions
	`Process completed with exit code [1-9]`,
}

var failureRe = regexp.MustCompile(strings.Join(failureMarkers, "|"))

// stripTimestamp strips the leading timestamp prefix from log lines for
// readable summary output: "123:2026-07-14T18:00:00Z some text" -> "123: some text"
var stripTimestampRe = regexp.MustCompile(`^(\d+):[0-9T:.Z+\-]*Z `)

func RunPRJobLogs(args []string) int {
	pr := ""
	jobID := ""
	wantAll := false
	doGrep := true

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "-h", "--help":
			fmt.Print(prJobLogsUsage)
			return 0
		case "--job":
			i++
			if i >= len(args) {
				fmt.Fprintln(os.Stderr, "pr-job-logs: --job needs a job id")
				return 2
			}
			jobID = args[i]
		case "--all":
			wantAll = true
		case "--no-grep":
			doGrep = false
		case "--":
			i++
			if i < len(args) {
				pr = args[i]
			}
		case "-":
			fmt.Fprintln(os.Stderr, "pr-job-logs: unknown arg:", args[i])
			return 2
		default:
			if strings.HasPrefix(args[i], "-") {
				fmt.Fprintln(os.Stderr, "pr-job-logs: unknown arg:", args[i])
				return 2
			}
			pr = args[i]
		}
	}

	repoVal, err := Repo()
	if err != nil {
		fmt.Fprintln(os.Stderr, "pr-job-logs:", err)
		return 2
	}

	outdir := os.Getenv("OUTDIR")
	if outdir == "" {
		tmp, err := os.MkdirTemp("", "pr-job-logs-*")
		if err != nil {
			fmt.Fprintln(os.Stderr, "pr-job-logs:", err)
			return 1
		}
		outdir = tmp
	}
	os.MkdirAll(outdir, 0o755)

	// Resolve job IDs
	var jobIDs []string
	if jobID != "" {
		jobIDs = []string{jobID}
	} else if pr != "" {
		ids, err := resolveJobIDs(repoVal, pr, wantAll)
		if err != nil {
			fmt.Fprintf(os.Stderr, "pr-job-logs: %v\n", err)
			return 1
		}
		jobIDs = ids
	} else {
		fmt.Fprintln(os.Stderr, "pr-job-logs: need a <pr#> or --job <id>")
		return 2
	}

	if len(jobIDs) == 0 {
		fmt.Fprintf(os.Stderr, "pr-job-logs: no matching jobs (PR #%s — already green? use --all to fetch all)\n", pr)
		return 1
	}

	fail := 0
	for _, id := range jobIDs {
		outpath := outdir + "/job-" + id + ".log"
		if err := fetchJobLog(repoVal, id, outpath); err != nil {
			fmt.Fprintf(os.Stderr, "pr-job-logs: failed to fetch job %s: %v\n", id, err)
			fail++
			continue
		}
		fmt.Println(outpath)
		if !doGrep {
			continue
		}
		fmt.Printf("  --- summary (job %s) ---\n", id)
		printSummary(outpath)
	}

	return fail
}

// resolveJobIDs gets the list of job IDs from gh pr checks.
func resolveJobIDs(repo, pr string, wantAll bool) ([]string, error) {
	out, err := runGHQuiet("pr", "checks", pr, "--repo", repo)
	// gh pr checks exits nonzero when a check is failing/pending
	checks := string(out)
	_ = err

	lines := strings.Split(strings.TrimSpace(checks), "\n")
	seen := map[string]bool{}
	var ids []string
	for _, line := range lines {
		if line == "" {
			continue
		}
		fields := strings.SplitN(line, "\t", 3)
		if len(fields) < 2 {
			continue
		}
		state := fields[1]
		if !wantAll && state != "fail" && state != "error" {
			continue
		}
		// Extract job ID from the details URL: .../runs/<r>/job/<id>
		for _, f := range fields {
			if idx := strings.LastIndex(f, "/job/"); idx >= 0 {
				id := f[idx+5:]
				if id != "" && !seen[id] {
					seen[id] = true
					ids = append(ids, id)
				}
			}
		}
	}
	return ids, nil
}

// fetchJobLog downloads a job's log via the GitHub REST API.
func fetchJobLog(repo, jobID, outpath string) error {
	raw, err := runGH("api", "repos/"+repo+"/actions/jobs/"+jobID+"/logs")
	if err != nil {
		return fmt.Errorf("API error: %v", err)
	}
	return os.WriteFile(outpath, raw, 0o644)
}

// printSummary scans a log file for known failure markers and prints matching lines.
func printSummary(path string) {
	f, err := os.Open(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "  (cannot read %s: %v)\n", path, err)
		return
	}
	defer f.Close()

	var matches []string
	var lastLines []string
	scanner := bufio.NewScanner(f)
	lineNum := 0
	for scanner.Scan() {
		lineNum++
		line := scanner.Text()
		numLine := strconv.Itoa(lineNum) + ":" + line
		if failureRe.MatchString(line) {
			matches = append(matches, numLine)
		}
		if strings.TrimSpace(line) != "" {
			lastLines = append(lastLines, numLine)
			if len(lastLines) > 20 {
				lastLines = lastLines[1:]
			}
		}
	}

	show := matches
	prefix := "summary"
	if len(matches) == 0 {
		show = lastLines
		prefix = "(no known failure markers matched — last 20 non-empty lines)"
	}
	if len(show) > 25 {
		show = show[len(show)-25:]
	}
	fmt.Printf("  %s\n", prefix)
	for _, l := range show {
		l = stripTimestampRe.ReplaceAllString(l, "${1}: ")
		fmt.Printf("    %s\n", l)
	}
}
