// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// File store: temporary file storage for fleet ships, with web visibility.
// Files are stored under .starfleet-ai/var/files/<name> with a TTL (default
// 60 min). Ships upload via `starfleetctl file put <path>` and retrieve via
// the web URL printed on upload. The web server serves /api/files/<name>.
package filestore

import (
	"fmt"
	"os"
	"path/filepath"
	"time"
)

const (
	dirName    = "files"
	defaultTTL = 60 * time.Minute
)

// Store is a file store rooted at a workspace's .starfleet-ai/var/files/.
type Store struct {
	Root string // workspace root
	Dir  string // .starfleet-ai/var/files/
}

// New creates a Store rooted at the given workspace root, ensuring the
// files directory exists.
func New(root string) (*Store, error) {
	dir := filepath.Join(root, ".starfleet-ai", "var", dirName)
	if err := os.MkdirAll(dir, 0755); err != nil {
		return nil, fmt.Errorf("filestore: create dir: %w", err)
	}
	return &Store{Root: root, Dir: dir}, nil
}

// Put copies a file into the store, returning the web URL.
// The file is stored as <name> (basename of src). If name already exists,
// it is overwritten. The TTL is stored in a companion .meta file.
func (s *Store) Put(src string, ttl time.Duration) (string, error) {
	data, err := os.ReadFile(src)
	if err != nil {
		return "", fmt.Errorf("filestore: read: %w", err)
	}
	name := filepath.Base(src)
	dst := filepath.Join(s.Dir, name)
	if err := os.WriteFile(dst, data, 0644); err != nil {
		return "", fmt.Errorf("filestore: write: %w", err)
	}
	if err := writeMeta(dst, ttl); err != nil {
		return "", err
	}
	url := fmt.Sprintf("/api/store/%s", name)
	return url, nil
}

// List returns all stored file entries with their remaining TTL.
type Entry struct {
	Name    string `json:"name"`
	Size    int64  `json:"size"`
	TTL     string `json:"ttl"`
	Expired bool   `json:"expired"`
}

// List returns all files currently in the store.
func (s *Store) List() ([]Entry, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return nil, fmt.Errorf("filestore: list: %w", err)
	}
	var out []Entry
	now := time.Now()
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) == ".meta" {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		exp := expired(s.Dir, e.Name(), now)
		out = append(out, Entry{
			Name:    e.Name(),
			Size:    fi.Size(),
			TTL:     ttlStr(s.Dir, e.Name(), now),
			Expired: exp,
		})
	}
	if out == nil {
		out = []Entry{}
	}
	return out, nil
}

// Remove deletes a stored file and its meta file.
func (s *Store) Remove(name string) error {
	path := filepath.Join(s.Dir, name)
	if err := os.Remove(path); err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("filestore: remove: %w", err)
	}
	_ = os.Remove(path + ".meta")
	return nil
}

// Prune removes all expired files.
func (s *Store) Prune() (int, error) {
	entries, err := os.ReadDir(s.Dir)
	if err != nil {
		return 0, fmt.Errorf("filestore: prune: %w", err)
	}
	now := time.Now()
	count := 0
	for _, e := range entries {
		if e.IsDir() || filepath.Ext(e.Name()) == ".meta" {
			continue
		}
		if expired(s.Dir, e.Name(), now) {
			path := filepath.Join(s.Dir, e.Name())
			os.Remove(path)
			os.Remove(path + ".meta")
			count++
		}
	}
	return count, nil
}

// Path returns the filesystem path for a stored file.
func (s *Store) Path(name string) string {
	return filepath.Join(s.Dir, name)
}

// Exists reports whether a stored file exists and is not expired.
func (s *Store) Exists(name string) bool {
	path := filepath.Join(s.Dir, name)
	fi, err := os.Stat(path)
	if err != nil || fi.IsDir() || filepath.Ext(name) == ".meta" {
		return false
	}
	if expired(s.Dir, name, time.Now()) {
		return false
	}
	return true
}

// --- meta helpers ---

func writeMeta(path string, ttl time.Duration) error {
	if ttl <= 0 {
		ttl = defaultTTL
	}
	expires := time.Now().Add(ttl)
	meta := expires.Format(time.RFC3339) + "\n"
	return os.WriteFile(path+".meta", []byte(meta), 0644)
}

func expired(dir, name string, now time.Time) bool {
	data, err := os.ReadFile(filepath.Join(dir, name+".meta"))
	if err != nil {
		return false // no meta → never expires
	}
	expires, err := time.Parse(time.RFC3339, string(data))
	if err != nil {
		return false
	}
	return now.After(expires)
}

func ttlStr(dir, name string, now time.Time) string {
	data, err := os.ReadFile(filepath.Join(dir, name+".meta"))
	if err != nil {
		return "never"
	}
	expires, err := time.Parse(time.RFC3339, string(data))
	if err != nil {
		return "unknown"
	}
	remaining := time.Until(expires)
	if remaining <= 0 {
		return "expired"
	}
	return fmt.Sprintf("%dm", int(remaining.Minutes()))
}
