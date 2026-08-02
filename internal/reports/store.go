// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Report store: file-based CRUD for report records. Each report is a single
// JSON file (<id>.json) in the reports directory under the comms dir.
package reports

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
)

// Store provides file-based report CRUD.
type Store struct {
	dir string
}

// NewStore returns a store rooted at the reports directory, creating it.
// Reports are stored under .starfleet-ai/reports/ (not under var/) so they
// are persisted to git like dashboard topics, not treated as ephemeral runtime state.
func NewStore(root string) (*Store, error) {
	dir := filepath.Join(root, ".starfleet-ai", "reports")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("reports store: mkdir %s: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

// Dir returns the store directory path.
func (s *Store) Dir() string { return s.dir }

// Path returns the report file path for a given ID.
func (s *Store) Path(id string) string {
	return filepath.Join(s.dir, id+".json")
}

// Create persists a new report record. Returns an error if the ID exists.
func (s *Store) Create(r *ReportRecord) (string, error) {
	if r.ID == "" {
		return "", fmt.Errorf("reports store: ID is required")
	}
	if _, err := s.Get(r.ID); err == nil {
		return "", fmt.Errorf("reports store: %s already exists", r.ID)
	}
	if r.Created == 0 {
		r.Created = time.Now().Unix()
	}
	data, err := json.MarshalIndent(r, "", "  ")
	if err != nil {
		return "", fmt.Errorf("reports store: marshal: %w", err)
	}
	if err := os.WriteFile(s.Path(r.ID), append(data, '\n'), 0o644); err != nil {
		return "", fmt.Errorf("reports store: write %s: %w", r.ID, err)
	}
	return r.ID, nil
}

// Get reads a report record by ID.
func (s *Store) Get(id string) (*ReportRecord, error) {
	data, err := os.ReadFile(s.Path(id))
	if err != nil {
		return nil, fmt.Errorf("reports store: get %s: %w", id, err)
	}
	var r ReportRecord
	if err := json.Unmarshal(data, &r); err != nil {
		return nil, fmt.Errorf("reports store: parse %s: %w", id, err)
	}
	return &r, nil
}

// Delete removes a report record.
func (s *Store) Delete(id string) error {
	return os.Remove(s.Path(id))
}

// List returns all report records, newest first.
func (s *Store) List() ([]*ReportRecord, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("reports store: readdir: %w", err)
	}
	var out []*ReportRecord
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var r ReportRecord
		if err := json.Unmarshal(data, &r); err != nil {
			continue
		}
		out = append(out, &r)
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
