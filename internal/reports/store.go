// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Report store: file-based CRUD for report records. Each report is a single
// RFC2822/email-style file named <message-id>.eml — the .eml suffix marks it
// as a mail-format file and distinguishes it from any other status files that
// may later share the directory.
package reports

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

const (
	// reportExt is the on-disk suffix for report files (RFC2822/email format).
	reportExt = ".eml"
	// legacyReportExt is the pre-rename suffix, still accepted on read so a
	// store that has not been migrated yet keeps its reports visible.
	legacyReportExt = ".json"
)

// Store provides file-based report CRUD.
type Store struct {
	dir string
}

// NewStore returns a store rooted at the reports directory, creating it.
// Reports are stored under .starfleet-ai/var/reports/. That path is NOT in
// .starfleet-ai/.gitignore's ephemeral list, so report files are (unlike
// ships/comms/timers) git-tracked — but only once an agent commits them: this
// package has no git path of its own (see doc/reports.md).
func NewStore(root string) (*Store, error) {
	dir := filepath.Join(root, ".starfleet-ai", "var", "reports")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("reports store: mkdir %s: %w", dir, err)
	}
	s := &Store{dir: dir}
	s.migrateLegacySuffix()
	return s, nil
}

// migrateLegacySuffix renames legacy <id>.json report files to <id>.eml.
// Idempotent and a no-op once every file carries the current suffix (an
// existing .eml is never overwritten).
func (s *Store) migrateLegacySuffix() {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), legacyReportExt) {
			continue
		}
		oldPath := filepath.Join(s.dir, e.Name())
		newPath := filepath.Join(s.dir, strings.TrimSuffix(e.Name(), legacyReportExt)+reportExt)
		if _, err := os.Stat(newPath); err == nil {
			continue // already migrated
		}
		_ = os.Rename(oldPath, newPath)
	}
}

// reportIDFromName returns the report id for a filename carrying either the
// current (.eml) or the legacy (.json) suffix.
func reportIDFromName(name string) (string, bool) {
	switch {
	case strings.HasSuffix(name, reportExt):
		return strings.TrimSuffix(name, reportExt), true
	case strings.HasSuffix(name, legacyReportExt):
		return strings.TrimSuffix(name, legacyReportExt), true
	default:
		return "", false
	}
}

// Dir returns the store directory path.
func (s *Store) Dir() string { return s.dir }

// Path returns the report file path for a given ID (Message-ID without brackets).
func (s *Store) Path(id string) string {
	// Strip angle brackets if present
	id = strings.Trim(id, "<>")
	return filepath.Join(s.dir, id+reportExt)
}

// Create persists a new report record. Returns an error if the ID exists.
func (s *Store) Create(r *ReportRecord) (string, error) {
	if r.MessageID == "" {
		return "", fmt.Errorf("reports store: MessageID is required")
	}
	id := strings.Trim(r.MessageID, "<>")
	if _, err := s.Get(id); err == nil {
		return "", fmt.Errorf("reports store: %s already exists", id)
	}
	if r.Created == 0 {
		r.Created = time.Now().Unix()
	}
	if r.Date == "" {
		r.Date = time.Unix(r.Created, 0).Format(time.RFC3339)
	}
	// Write in RFC2822 format
	data := r.toRFC2822String()
	if err := os.WriteFile(s.Path(r.MessageID), []byte(data), 0o644); err != nil {
		return "", fmt.Errorf("reports store: write %s: %w", id, err)
	}
	return id, nil
}

// Get reads a report record by ID (Message-ID without brackets).
// Supports both RFC2822 format (new) and legacy JSON format (old).
func (s *Store) Get(id string) (*ReportRecord, error) {
	id = strings.Trim(id, "<>")
	data, err := os.ReadFile(s.Path(id))
	if err != nil {
		return nil, fmt.Errorf("reports store: get %s: %w", id, err)
	}
	r, err := parseRFC2822(data, id)
	if err != nil {
		return nil, fmt.Errorf("reports store: parse %s: %w", id, err)
	}
	return r, nil
}

// Delete removes a report record.
func (s *Store) Delete(id string) error {
	id = strings.Trim(id, "<>")
	return os.Remove(s.Path(id))
}

// List returns all report records, newest first.
// Supports both RFC2822 format (new) and legacy JSON format (old).
func (s *Store) List() ([]*ReportRecord, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("reports store: readdir: %w", err)
	}
	var out []*ReportRecord
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		id, ok := reportIDFromName(e.Name())
		if !ok {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		r, err := parseRFC2822(data, id)
		if err != nil {
			continue
		}
		out = append(out, r)
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i].Created > out[j].Created
	})
	return out, nil
}

// ListJSON returns all report records as JSON-safe structs.
func (s *Store) ListJSON() ([]ReportJSON, error) {
	recs, err := s.List()
	if err != nil {
		return nil, err
	}
	out := make([]ReportJSON, len(recs))
	for i, r := range recs {
		j := recordToJSON(r)
		j.Ago = ago(r.Created)
		out[i] = j
	}
	return out, nil
}

func ago(epoch int64) string {
	d := time.Since(time.Unix(epoch, 0))
	if d < time.Minute {
		return "gerade"
	}
	if d < time.Hour {
		return fmt.Sprintf("vor %dm", int(d.Minutes()))
	}
	if d < 24*time.Hour {
		return fmt.Sprintf("vor %dh", int(d.Hours()))
	}
	return fmt.Sprintf("vor %dt", int(d.Hours()/24))
}
