// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Package envkeys resolves API keys (and other env-referenced values) for
// daemons that are spawned outside a normal login shell. A daemon started by
// cron, systemd, or `run-opencode.flagship` does not inherit the user's
// login-shell environment, so keys exported in ~/.profile (e.g.
// NIM_API_KEY, OPENCODE_API_KEY) would silently be missing. This package
// back-fills such values from the user's shell profile as a last resort.
package envkeys

import (
	"bufio"
	"os"
	"path/filepath"
	"strings"
	"sync"
)

// missing is the in-memory cache of profile-parsed values. It is filled once
// (lazily) and never invalidated within a process — matching the lifetime of
// a daemon (env cannot change while it runs anyway).
var (
	profileOnce sync.Once
	profileVals map[string]string
)

// Resolve returns the value for an env var name by checking, in order:
//  1. the current process environment (os.Getenv),
//  2. the user's shell profile (~/.profile or ~/.bash_profile).
//
// The empty-string check mirrors os.Setenv semantics: a variable exported
// with an empty value in the profile is still "set". Resolve returns whether
// the variable was found at all (regardless of value emptiness).
func Resolve(name string) (string, bool) {
	if v, ok := os.LookupEnv(name); ok {
		return v, true
	}
	loadProfile()
	if v, ok := profileVals[name]; ok {
		return v, true
	}
	return "", false
}

// ResolveMissing returns the resolved value only when the variable is NOT
// already set in the process environment. Used by daemon env builders that
// want to back-fill missing keys without overriding inherited ones.
func ResolveMissing(name string) (string, bool) {
	if _, ok := os.LookupEnv(name); ok {
		return "", false
	}
	return Resolve(name)
}

// loadProfile parses the user's login shell profile(s) for `export NAME=value`
// assignments. It never executes anything — only simple assignments are
// recognized. Values are taken literally (no $VAR / `${VAR}` expansion).
func loadProfile() {
	profileOnce.Do(func() {
		profileVals = map[string]string{}
		home, err := os.UserHomeDir()
		if err != nil {
			return
		}
		for _, name := range []string{".profile", ".bash_profile"} {
			path := filepath.Join(home, name)
			f, err := os.Open(path)
			if err != nil {
				continue
			}
			scanProfile(f, profileVals)
			f.Close()
			// .bash_profile shadows ~/.profile for login shells, but we merge
			// both: process both, later files win.
		}
	})
}

// scanProfile reads assignments of the form `export NAME=value` or
// `NAME=value` from r. The value may be single- or double-quoted. Lines that
// are clearly not simple assignments (contain command substitution or a
// leading unquoted token with spaces) are skipped.
func scanProfile(r interface{ Read([]byte) (int, error) }, out map[string]string) {
	sc := bufio.NewScanner(r)
	sc.Buffer(make([]byte, 0, 64*1024), 1024*1024)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		if strings.HasPrefix(line, "export ") {
			line = strings.TrimSpace(strings.TrimPrefix(line, "export "))
		}
		idx := strings.IndexByte(line, '=')
		if idx <= 0 {
			continue
		}
		name := strings.TrimSpace(line[:idx])
		if name == "" || strings.ContainsAny(name, " \t'\"") {
			continue
		}
		val := strings.TrimSpace(line[idx+1:])
		// Skip anything that would need shell evaluation.
		if strings.ContainsAny(val, "$`\\") {
			continue
		}
		val = unquote(val)
		out[name] = val
	}
}

// unquote strips one level of matching single or double quotes.
func unquote(v string) string {
	if len(v) >= 2 && v[0] == v[len(v)-1] && (v[0] == '\'' || v[0] == '"') {
		return v[1 : len(v)-1]
	}
	return v
}