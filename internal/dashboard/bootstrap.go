// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult

package dashboard

import "os"

// minimalSkeleton is what DASHBOARD.md looks like on a checkout that has
// never had one — just enough structure (the two headings DoReindex looks
// for, plus the same footer note the hand-authored version carries) for
// `dashboard theme new`/`reindex` to work immediately, no themes yet.
const minimalSkeleton = `# DASHBOARD.md

Cross-session "what's in flight / what got parked" index. Thin index — each
row links to its own file under ` + "`dashboard/themes/`" + `. Edit the theme file
directly; this index itself is regenerated with
` + "`.bin/starfleetctl dashboard reindex`" + ` and should not normally be hand-edited.

## Aktive Themen

| Thema | Status | Datei |
|---|---|---|

## Parkplatz

Angefangen/aufgefallen, aber (noch) nicht weiterverfolgt — kurze Notiz statt Verlust.

| Thema | Notiert | Seit | Datei |
|---|---|---|---|

---

*Not tracked here on purpose (already covered elsewhere, would just go stale):*
individual open PRs, who's-online-now (` + "`agent-bus board`" + `), PR-branch locks
(` + "`pr-claim --list`" + `).
`

// EnsureBootstrapped creates a minimal DASHBOARD.md skeleton if the file is
// entirely missing — the "should be created fully automatically when
// needed" case (a truly from-scratch checkout). Idempotent: never
// overwrites an existing file, regardless of content.
func (d *Dashboard) EnsureBootstrapped() (created bool, err error) {
	if _, err := os.Stat(d.File); err == nil {
		return false, nil
	}
	if err := os.MkdirAll(d.ThemesDir(), 0o755); err != nil {
		return false, err
	}
	if err := os.WriteFile(d.File, []byte(minimalSkeleton), 0o644); err != nil {
		return false, err
	}
	return true, nil
}
