// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Report data model. Each report is a structured RFC2822-style document
// submitted by a ship, stored under .starfleet-ai/var/reports/.
package reports

import (
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// ReportRecord is a single structured report submitted by a ship.
// Uses RFC2822-style headers for the on-disk format.
type ReportRecord struct {
	MessageID   string   `json:"message_id"`   // Message-ID header
	Date        string   `json:"date"`         // Date header (RFC3339)
	From        string   `json:"from"`         // From header (ship ID)
	Subject     string   `json:"subject"`      // Subject header
	To          string   `json:"to,omitempty"` // To header (optional, fleet/ship)
	Tags        []string `json:"tags,omitempty"`
	TaskRef     string   `json:"task_ref,omitempty"`
	Attachments []string `json:"attachments,omitempty"`
	Body        string   `json:"body"`
	Created     int64    `json:"created"` // epoch seconds (for sorting)
}

// ReportJSON is the JSON-serializable shape returned by the web API.
type ReportJSON struct {
	MessageID   string   `json:"message_id"`
	Date        string   `json:"date"`
	From        string   `json:"from"`
	Subject     string   `json:"subject"`
	To          string   `json:"to,omitempty"`
	Tags        []string `json:"tags,omitempty"`
	TaskRef     string   `json:"task_ref,omitempty"`
	Attachments []string `json:"attachments,omitempty"`
	Body        string   `json:"body"`
	Created     int64    `json:"created"`
	Ago         string   `json:"ago"`
}

func recordToJSON(r *ReportRecord) ReportJSON {
	return ReportJSON{
		MessageID:   r.MessageID,
		Date:        r.Date,
		From:        r.From,
		Subject:     r.Subject,
		To:          r.To,
		Tags:        r.Tags,
		TaskRef:     r.TaskRef,
		Attachments: r.Attachments,
		Body:        r.Body,
		Created:     r.Created,
	}
}

// toRFC2822String converts ReportRecord to RFC2822-style string for storage.
func (r *ReportRecord) toRFC2822String() string {
	var b strings.Builder
	b.WriteString("Message-ID: " + r.MessageID + "\n")
	b.WriteString("Date: " + r.Date + "\n")
	b.WriteString("From: " + r.From + "\n")
	b.WriteString("Subject: " + r.Subject + "\n")
	if r.To != "" {
		b.WriteString("To: " + r.To + "\n")
	}
	if len(r.Tags) > 0 {
		b.WriteString("Tags: " + strings.Join(r.Tags, ",") + "\n")
	}
	if r.TaskRef != "" {
		b.WriteString("Task-Ref: " + r.TaskRef + "\n")
	}
	if len(r.Attachments) > 0 {
		b.WriteString("Attachments: " + strings.Join(r.Attachments, ",") + "\n")
	}
	b.WriteString("Created: " + r.Date + "\n") // also store epoch as Date for sorting
	b.WriteString("\n")
	b.WriteString(r.Body)
	return b.String()
}

// parseRFC2822 parses an RFC2822-style report string into a ReportRecord.
// Also supports legacy JSON format for backward compatibility.
func parseRFC2822(data []byte, filename string) (*ReportRecord, error) {
	s := string(data)
	// Try RFC2822 format first (has headers with colons at start of line)
	if strings.Contains(s, "Message-ID:") || strings.Contains(s, "Subject:") || strings.Contains(s, "From:") {
		return parseRFC2822Format(s, filename)
	}
	// Fall back to legacy JSON format
	return parseLegacyJSON(data, filename)
}

func parseRFC2822Format(s string, filename string) (*ReportRecord, error) {
	lines := strings.Split(s, "\n")
	var headers []string
	bodyStart := 0
	for i, line := range lines {
		if strings.TrimSpace(line) == "" {
			bodyStart = i + 1
			break
		}
		headers = append(headers, line)
	}
	body := strings.Join(lines[bodyStart:], "\n")
	body = strings.TrimRight(body, "\n")

	r := &ReportRecord{Body: body}
	for _, h := range headers {
		colon := strings.IndexByte(h, ':')
		if colon < 0 {
			continue
		}
		key := strings.TrimSpace(h[:colon])
		val := strings.TrimSpace(h[colon+1:])
		switch strings.ToLower(key) {
		case "message-id":
			r.MessageID = val
		case "date":
			r.Date = val
			// Try to parse date as RFC3339 for Created epoch
			if t, err := time.Parse(time.RFC3339, val); err == nil {
				r.Created = t.Unix()
			}
		case "from":
			r.From = val
		case "subject":
			r.Subject = val
		case "to":
			r.To = val
		case "tags":
			if val != "" {
				r.Tags = strings.Split(val, ",")
			}
		case "task-ref":
			r.TaskRef = val
		case "attachments":
			if val != "" {
				r.Attachments = strings.Split(val, ",")
			}
		case "created":
			if val != "" {
				if t, err := time.Parse(time.RFC3339, val); err == nil {
					r.Created = t.Unix()
				} else if epoch, err := parseEpoch(val); err == nil {
					r.Created = epoch
				}
			}
		}
	}
	// Ensure Message-ID exists
	if r.MessageID == "" {
		r.MessageID = "<" + filename + ">"
	}
	// Ensure Created is set
	if r.Created == 0 {
		r.Created = time.Now().Unix()
		r.Date = time.Unix(r.Created, 0).Format(time.RFC3339)
	}
	return r, nil
}

func parseLegacyJSON(data []byte, filename string) (*ReportRecord, error) {
	var legacy struct {
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
	if err := json.Unmarshal(data, &legacy); err != nil {
		return nil, err
	}
	// Convert legacy to new format
	now := time.Now()
	date := now.Format(time.RFC3339)
	if legacy.Created > 0 {
		date = time.Unix(legacy.Created, 0).Format(time.RFC3339)
	}
	r := &ReportRecord{
		MessageID:   "<" + legacy.ID + ">",
		Date:        date,
		From:        legacy.Ship,
		Subject:     legacy.Title,
		To:          "",
		Tags:        legacy.Tags,
		TaskRef:     legacy.TaskRef,
		Attachments: legacy.Attachments,
		Body:        legacy.Body,
		Created:     legacy.Created,
	}
	if r.Created == 0 {
		r.Created = now.Unix()
	}
	return r, nil
}

func parseEpoch(s string) (int64, error) {
	// Try to parse as epoch seconds
	var epoch int64
	_, err := fmt.Sscanf(s, "%d", &epoch)
	return epoch, err
}
