// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// CLI dispatcher for the `reports` subcommand.
package reports

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/metux/starfleetctl/internal/comms"
)

const usage = `starfleetctl reports — fleet report system

Usage:
  reports submit <title> [--body <text>] [--tags <tag1,tag2>]   submit a new report
  reports list [--ship <name>] [--tag <tag>] [--json]           list reports (newest first)
  reports show <id>                                              show one report
  reports delete <id>                                            delete a report

Examples:
  reports submit "Build completed" --body "all tests pass" --tags "build,ci"
  reports list --ship Enterprise
  reports list --json
  reports show build-report-abc123
`

// Run dispatches a `reports` invocation.
func Run(root string, args []string) int {
	if len(args) == 0 || args[0] == "-h" || args[0] == "--help" {
		fmt.Print(usage)
		return 0
	}
	switch args[0] {
	case "submit":
		return runSubmit(root, args[1:])
	case "list":
		return runList(root, args[1:])
	case "show":
		return runShow(root, args[1:])
	case "delete":
		return runDelete(root, args[1:])
	default:
		fmt.Fprintf(os.Stderr, "reports: unknown subcommand: %s\n", args[0])
		return 2
	}
}

func runSubmit(root string, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: reports submit <title> [--body <text>] [--tags <tag1,tag2>]")
		return 2
	}
	title := args[0]
	body := ""
	tagsStr := ""
	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--body":
			if i+1 < len(args) {
				i++
				body = args[i]
			}
		case "--tags":
			if i+1 < len(args) {
				i++
				tagsStr = args[i]
			}
		}
	}
	var tags []string
	if tagsStr != "" {
		tags = strings.Split(tagsStr, ",")
	}

	store, err := NewStore(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "reports:", err)
		return 1
	}

	b, err := comms.New(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "reports:", err)
		return 1
	}

	rec := &ReportRecord{
		ID:      fmt.Sprintf("r-%d", time.Now().UnixNano()),
		Title:   title,
		Ship:    b.ShipID,
		Body:    body,
		Tags:    tags,
		Created: time.Now().Unix(),
	}

	id, err := store.Create(rec)
	if err != nil {
		fmt.Fprintln(os.Stderr, "reports:", err)
		return 1
	}
	fmt.Printf("report submitted: %s\n", id)
	return 0
}

func runList(root string, args []string) int {
	filterShip := ""
	filterTag := ""
	asJSON := false

	for i := 0; i < len(args); i++ {
		switch args[i] {
		case "--ship":
			if i+1 < len(args) {
				i++
				filterShip = args[i]
			}
		case "--tag":
			if i+1 < len(args) {
				i++
				filterTag = args[i]
			}
		case "--json":
			asJSON = true
		}
	}

	store, err := NewStore(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "reports:", err)
		return 1
	}

	recs, err := store.List()
	if err != nil {
		fmt.Fprintln(os.Stderr, "reports:", err)
		return 1
	}

	var filtered []*ReportRecord
	for _, r := range recs {
		if filterShip != "" && r.Ship != filterShip {
			continue
		}
		if filterTag != "" {
			hasTag := false
			for _, t := range r.Tags {
				if t == filterTag {
					hasTag = true
					break
				}
			}
			if !hasTag {
				continue
			}
		}
		filtered = append(filtered, r)
	}

	if asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		_ = enc.Encode(filtered)
		return 0
	}

	if len(filtered) == 0 {
		fmt.Println("no reports")
		return 0
	}
	for _, r := range filtered {
		tags := ""
		if len(r.Tags) > 0 {
			tags = fmt.Sprintf(" [%s]", strings.Join(r.Tags, ","))
		}
		fmt.Printf("%s  %s  %s%s\n", ago(r.Created), r.Ship, r.Title, tags)
	}
	return 0
}

func runShow(root string, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: reports show <id>")
		return 2
	}
	store, err := NewStore(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "reports:", err)
		return 1
	}
	r, err := store.Get(args[0])
	if err != nil {
		fmt.Fprintln(os.Stderr, "reports:", err)
		return 1
	}
	fmt.Printf("ID:      %s\n", r.ID)
	fmt.Printf("Title:   %s\n", r.Title)
	fmt.Printf("Ship:    %s\n", r.Ship)
	fmt.Printf("Created: %s\n", time.Unix(r.Created, 0).Format(time.RFC3339))
	if len(r.Tags) > 0 {
		fmt.Printf("Tags:    %s\n", strings.Join(r.Tags, ", "))
	}
	if r.Body != "" {
		fmt.Println("---")
		fmt.Println(r.Body)
	}
	return 0
}

func runDelete(root string, args []string) int {
	if len(args) == 0 {
		fmt.Fprintln(os.Stderr, "usage: reports delete <id>")
		return 2
	}
	store, err := NewStore(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "reports:", err)
		return 1
	}
	if err := store.Delete(args[0]); err != nil {
		fmt.Fprintln(os.Stderr, "reports:", err)
		return 1
	}
	fmt.Printf("report deleted: %s\n", args[0])
	return 0
}
