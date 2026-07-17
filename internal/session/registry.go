// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package session

import (
	"bufio"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// Registry is the caller-owned (starfleetctl) name → termctl pipe path map.
// It lives under .starfleet-ai/session-registry.txt and is managed entirely by
// starfleetctl — termctl knows nothing about it.
type Registry struct {
	root string
	mu   sync.Mutex
}

func NewRegistry(root string) *Registry {
	return &Registry{root: root}
}

func (r *Registry) path() string {
	return filepath.Join(r.root, ".starfleet-ai", "session-registry.txt")
}

// Get returns the pipe path for a ship ID, or ("", false) if not found.
func (r *Registry) Get(name string) (string, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.getLocked(name)
}

func (r *Registry) getLocked(name string) (string, bool) {
	f, err := os.Open(r.path())
	if err != nil {
		return "", false
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 && parts[0] == name {
			return parts[1], true
		}
	}
	return "", false
}

// Put records name → pipePath. Overwrites existing entry for name.
func (r *Registry) Put(name, pipePath string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	path := r.path()
	var b strings.Builder
	b.WriteString("# starfleetctl session registry: ship_id=pipe_path\n")
	if f, err := os.Open(path); err == nil {
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := sc.Text()
			if strings.HasPrefix(line, "#") || strings.TrimSpace(line) == "" {
				continue
			}
			if strings.HasPrefix(line, name+"=") {
				continue // overwrite
			}
			b.WriteString(line + "\n")
		}
		f.Close()
	}
	b.WriteString(fmt.Sprintf("%s=%s\n", name, pipePath))
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return err
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// Delete removes a name from the registry.
func (r *Registry) Delete(name string) error {
	r.mu.Lock()
	defer r.mu.Unlock()

	path := r.path()
	f, err := os.Open(path)
	if err != nil {
		return nil // already gone
	}
	var lines []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := sc.Text()
		if strings.HasPrefix(line, name+"=") {
			continue
		}
		lines = append(lines, line)
	}
	f.Close()
	return os.WriteFile(path, []byte(strings.Join(lines, "\n")+"\n"), 0o644)
}

// List returns all registered ship IDs.
func (r *Registry) List() []string {
	r.mu.Lock()
	defer r.mu.Unlock()

	f, err := os.Open(r.path())
	if err != nil {
		return nil
	}
	defer f.Close()
	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		parts := strings.SplitN(line, "=", 2)
		if len(parts) == 2 {
			out = append(out, parts[0])
		}
	}
	return out
}