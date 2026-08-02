// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Report data model. Each report is a structured JSON document
// submitted by a ship, stored under .starfleet-ai/reports/ (persisted to git
// like dashboard topics).
package reports

// ReportRecord is a single structured report submitted by a ship.
type ReportRecord struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Subtitle    string   `json:"subtitle,omitempty"`
	Ship        string   `json:"ship"`
	Body        string   `json:"body"`
	Tags        []string `json:"tags,omitempty"`
	TaskRef     string   `json:"task_ref,omitempty"`
	Attachments []string `json:"attachments,omitempty"`
	Created     int64    `json:"created"`
}

// ReportJSON is the JSON-serializable shape returned by the web API.
type ReportJSON struct {
	ID          string   `json:"id"`
	Title       string   `json:"title"`
	Subtitle    string   `json:"subtitle,omitempty"`
	Ship        string   `json:"ship"`
	Body        string   `json:"body"`
	Tags        []string `json:"tags,omitempty"`
	TaskRef     string   `json:"task_ref,omitempty"`
	Attachments []string `json:"attachments,omitempty"`
	Created     int64    `json:"created"`
	Ago         string   `json:"ago"`
}

func recordToJSON(r *ReportRecord) ReportJSON {
	return ReportJSON{
		ID:          r.ID,
		Title:       r.Title,
		Subtitle:    r.Subtitle,
		Ship:        r.Ship,
		Body:        r.Body,
		Tags:        r.Tags,
		TaskRef:     r.TaskRef,
		Attachments: r.Attachments,
		Created:     r.Created,
	}
}
