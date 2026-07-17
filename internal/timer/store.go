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
	"strconv"
	"strings"
	"time"
)

// Store provides file-based timer CRUD for a single timer directory.
type Store struct {
	dir    string
	prefix string // "e" for ephemeral, "p" for persistent
}

// NewStore returns a store rooted at dir, creating it if needed.
// prefix is used for auto-assigned IDs (e.g. "e" for ephemeral, "p" for persistent).
func NewStore(dir string, prefix string) (*Store, error) {
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("timer store: mkdir %s: %w", dir, err)
	}
	if prefix == "" {
		prefix = "t"
	}
	return &Store{dir: dir, prefix: prefix}, nil
}

// Path returns the timer file path for a given ID.
func (s *Store) Path(id string) string {
	return filepath.Join(s.dir, id+".json")
}

// nextID reads the .counter file and returns the next timer ID, incrementing
// the counter atomically.
func (s *Store) nextID() (string, error) {
	counterPath := filepath.Join(s.dir, ".counter")
	var next int64 = 1
	if data, err := os.ReadFile(counterPath); err == nil {
		if n, err := strconv.ParseInt(strings.TrimSpace(string(data)), 10, 64); err == nil {
			next = n
		}
	}
	id := fmt.Sprintf("%s%03d", s.prefix, next)
	if err := os.WriteFile(counterPath, []byte(strconv.FormatInt(next+1, 10)+"\n"), 0o644); err != nil {
		return "", fmt.Errorf("timer store: write counter: %w", err)
	}
	return id, nil
}

// Create persists a new timer record. If rec.ID is empty, a new ID is
// auto-assigned. Returns the assigned ID.
func (s *Store) Create(rec *TimerRecord) (string, error) {
	if rec.ID == "" {
		id, err := s.nextID()
		if err != nil {
			return "", err
		}
		rec.ID = id
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
