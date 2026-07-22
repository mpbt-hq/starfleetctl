// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Timer store: file-based CRUD for timer records. Each timer is a single JSON
// file (t001.json, t002.json, ...) in a timer directory. A .counter file
// tracks the next ID. The store is NOT safe for concurrent use — the worker
// daemon is the sole writer at any given time.
package timer

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// Store provides file-based timer CRUD for a single timer directory.
type Store struct {
	dir string
}

// NewStore returns a store rooted at dir, creating it if needed.
func NewStore(dir string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("timer store: mkdir %s: %w", dir, err)
	}
	return &Store{dir: dir}, nil
}

// Path returns the timer file path for a given ID.
func (s *Store) Path(id string) string {
	return filepath.Join(s.dir, id+".json")
}

// Create persists a new timer record. rec.ID must be set (use GenerateName()
// if no explicit name was given). Returns an error if the ID already exists.
func (s *Store) Create(rec *TimerRecord) (string, error) {
	if rec.ID == "" {
		return "", fmt.Errorf("timer store: ID is required")
	}
	// Check for duplicate ID.
	if _, err := s.Get(rec.ID); err == nil {
		return "", fmt.Errorf("timer store: %s already exists", rec.ID)
	}
	if rec.CreatedAt == 0 {
		rec.CreatedAt = time.Now().Unix()
	}
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return "", fmt.Errorf("timer store: marshal: %w", err)
	}
	if err := os.WriteFile(s.Path(rec.ID), append(data, '\n'), 0o644); err != nil {
		return "", fmt.Errorf("timer store: write %s: %w", rec.ID, err)
	}
	return rec.ID, nil
}

// Get reads a timer record by ID.
func (s *Store) Get(id string) (*TimerRecord, error) {
	data, err := os.ReadFile(s.Path(id))
	if err != nil {
		return nil, fmt.Errorf("timer store: get %s: %w", id, err)
	}
	var rec TimerRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, fmt.Errorf("timer store: parse %s: %w", id, err)
	}
	return &rec, nil
}

// Update overwrites a timer record.
func (s *Store) Update(rec *TimerRecord) error {
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return fmt.Errorf("timer store: marshal: %w", err)
	}
	return os.WriteFile(s.Path(rec.ID), append(data, '\n'), 0o644)
}

// Delete removes a timer record.
func (s *Store) Delete(id string) error {
	return os.Remove(s.Path(id))
}

// List returns all timer records in the directory.
func (s *Store) List() ([]*TimerRecord, error) {
	entries, err := os.ReadDir(s.dir)
	if err != nil {
		return nil, fmt.Errorf("timer store: readdir: %w", err)
	}
	var out []*TimerRecord
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".json") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(s.dir, e.Name()))
		if err != nil {
			continue
		}
		var rec TimerRecord
		if err := json.Unmarshal(data, &rec); err != nil {
			continue
		}
		out = append(out, &rec)
	}
	return out, nil
}

// Dir returns the timer directory path (for status display).
func (s *Store) Dir() string {
	return s.dir
}
