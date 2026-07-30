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
	"github.com/metux/starfleetctl/internal/filestore"
)

const usage = `starfleetctl reports — fleet report system

Usage:
  reports submit <title> [--subtitle <text>] [--body <text>] [--body-file <path>]
                       [--tags <tag1,tag2>] [--task-ref <slug>]
                       [--attachment <file> ...]                   submit a new report
  reports list [--ship <name>] [--tag <tag>] [--json]             list reports (newest first)
  reports show <id>                                                show one report
  reports delete <id>                                              delete a report

Examples:
  reports submit "Build completed" --body "all tests pass" --tags "build,ci"
  reports submit "Release 25.2" --subtitle "CI-Status" --task-ref xlibre/release-25-2
  reports submit "Test log" --body-file test.log --attachment test.log --tags "ci,test"
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
		fmt.Fprintln(os.Stderr, "usage: reports submit <title> [--subtitle <text>] [--body <text>] [--body-file <path>] [--tags <tag1,tag2>] [--task-ref <slug>] [--attachment <file> ...]")
		return 2
	}
	title := args[0]
	subtitle := ""
	body := ""
	tagsStr := ""
	taskRef := ""
	var bodyFile string
	var attachments []string

	for i := 1; i < len(args); i++ {
		switch args[i] {
		case "--subtitle":
			if i+1 < len(args) {
				i++
				subtitle = args[i]
			}
		case "--body":
			if i+1 < len(args) {
				i++
				body = args[i]
			}
		case "--body-file":
			if i+1 < len(args) {
				i++
				bodyFile = args[i]
			}
		case "--tags":
			if i+1 < len(args) {
				i++
				tagsStr = args[i]
			}
		case "--task-ref":
			if i+1 < len(args) {
				i++
				taskRef = args[i]
			}
		case "--attachment":
			if i+1 < len(args) {
				i++
				attachments = append(attachments, args[i])
			}
		}
	}

	if bodyFile != "" {
		data, err := os.ReadFile(bodyFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "reports: read body-file:", err)
			return 1
		}
		body = string(data)
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

	// Upload attachments to filestore
	fstore, err := filestore.New(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "reports: filestore:", err)
		return 1
	}
	var attachNames []string
	for _, path := range attachments {
		name, err := fstore.Put(path, 0)
		if err != nil {
			fmt.Fprintln(os.Stderr, "reports: upload attachment:", err)
			return 1
		}
		attachNames = append(attachNames, name)
	}

	b, err := comms.New(root)
	if err != nil {
		fmt.Fprintln(os.Stderr, "reports:", err)
		return 1
	}

	rec := &ReportRecord{
		ID:          fmt.Sprintf("r-%d", time.Now().UnixNano()),
		Title:       title,
		Subtitle:    subtitle,
		Ship:        b.ShipID,
		Body:        body,
		Tags:        tags,
		TaskRef:     taskRef,
		Attachments: attachNames,
		Created:     time.Now().Unix(),
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
	fmt.Printf("ID:       %s\n", r.ID)
	fmt.Printf("Title:    %s\n", r.Title)
	if r.Subtitle != "" {
		fmt.Printf("Subtitle: %s\n", r.Subtitle)
	}
	fmt.Printf("Ship:     %s\n", r.Ship)
	fmt.Printf("Created:  %s\n", time.Unix(r.Created, 0).Format(time.RFC3339))
	if len(r.Tags) > 0 {
		fmt.Printf("Tags:     %s\n", strings.Join(r.Tags, ", "))
	}
	if r.TaskRef != "" {
		fmt.Printf("TaskRef:  %s\n", r.TaskRef)
	}
	if len(r.Attachments) > 0 {
		fmt.Printf("Attachments:\n")
		for _, a := range r.Attachments {
			fmt.Printf("  %s\n", a)
		}
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
