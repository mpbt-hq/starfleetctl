# Agent Instruction Fragments

How starfleetctl manages per-topic instruction fragments — the content that
becomes the agent's system prompt.

## What Is a Fragment?

A **fragment** is a small Markdown file with a YAML-like frontmatter block.
Each fragment covers one topic (e.g. "inter-ship communication",
"licensing policy", "CI gotchas"). At reindex time, every fragment's body
(frontmatter stripped) is inlined into the auto-generated `CLAUDE.md` and
`.starfleet-ai/var/agents.d/index.md` — this is what the agent reads as its
system prompt.

Fragments are the **single source of truth** for agent instructions. You edit
fragments; `reindex` regenerates the derived files.

## File Format

Every fragment file starts with a frontmatter block delimited by `---`:

```
---
slug: project/licensing-policy
title: "Licensing policy"
order: 200
owner: "starfleetctl"
---

## Licensing policy

Two different scopes — don't conflate them:

- **New files that end up linked into the xserver binary itself** …
- **New files that are NOT part of the final delivery** …
```

### Frontmatter Fields

| Field | Required | Description |
|-------|----------|-------------|
| `slug` | no | Identifier — derived from file path if omitted (e.g. `project/licensing-policy` from `agents.d/project/licensing-policy.md`). Explicit slug is needed only when the path-based derivation would be wrong. |
| `title` | **yes** | Human-readable title. Appears in `agents list` output. |
| `order` | no | Integer controlling sort position in the generated index. Lower = earlier. Default: `500`. |
| `owner` | no | Which tool/component maintains this fragment (e.g. `"starfleetctl"`). Informational only. |

### Order Values (Conventions)

| Range | Use |
|-------|-----|
| 1–19 | Starfleet-wide coordination (comms, fleet rules) |
| 20–99 | Working practices, project basics |
| 100–199 | Project-specific knowledge (CI, coding style) |
| 200+ | Permissive catch-all (licensing, auto-commit, etc.) |
| 500 | Default for `agents new` |

## Directory Structure

```
workspace/
├── agents.d/                              ← user-maintained fragments
│   ├── local/
│   │   └── local-knowledge-dump.md        (slug: local/local-knowledge-dump)
│   └── project/
│       ├── architecture.md                (slug: project/architecture)
│       ├── key-commands.md                (slug: project/key-commands)
│       └── ...
├── .starfleet-ai/var/agents.d/
│   ├── index.md                           ← auto-generated (all bodies inlined)
│   └── starfleet-instructions/            ← auto-installed (tool-owned, gitignored)
│       ├── inter-ship-communication.md
│       ├── fleet-autonomous.md
│       ├── working-practices-*.md
│       └── comms-opencode-polling.md
└── CLAUDE.md                              ← auto-generated (same content as index.md)
```

### Two Fragment Sources

| Source | Location | Who maintains it | Git-tracked? |
|--------|----------|-----------------|-------------|
| **User-maintained** | `agents.d/**/*.md` | You / your team | Yes |
| **Starfleet-owned** | `.starfleet-ai/var/agents.d/starfleet-instructions/` | starfleetctl binary (embedded) | No (gitignored) |

**User-maintained fragments** are yours. Create, edit, and delete them freely.
They survive reindex and bootstrap.

**Starfleet-owned fragments** are installed from the binary's embedded FS
(`fragments/starfleet-instructions/`). They are overwritten on every
`starfleetctl agents install-starfleet` or `bootstrap --fix`. Do not edit
them directly — if you need to change one, edit the source in the starfleetctl
repo and rebuild.

### Taxonomy (Subdirectories)

Subdirectories under `agents.d/` organize fragments by scope:

| Directory | Purpose | Example |
|-----------|---------|---------|
| `agents.d/local/` | Scratch space for session discoveries (not yet sorted) | `local/local-knowledge-dump.md` |
| `agents.d/project/` | Project-specific knowledge (build system, CI, conventions) | `project/coding-style-*.md` |
| `agents.d/starfleet/` | Fleet coordination (legacy, now skipped during loading) | — |
| `agents.d/xlibre/` | X server, drivers, protocol (future) | — |

The slug is derived from the path: `agents.d/project/foo.md` → slug
`project/foo`. Starfleet-owned fragments use the prefix
`starfleet-instructions/` (e.g. `starfleet-instructions/inter-ship-communication`).

**Promotion path:** Start in `agents.d/local/`, move to `project/` or
`starfleet/` when the knowledge proves stable.

## Lifecycle

### Creating a New Fragment

```sh
starfleetctl agents new project/my-topic --title "My Topic" --order 150
```

This:
1. Creates `agents.d/project/my-topic.md` with frontmatter scaffold
2. Runs `reindex` (regenerates `CLAUDE.md` + `index.md`)

The new file contains `(fill in)` as placeholder body — edit it with
`agents write` or your editor.

### Editing a Fragment

**Option A: Direct edit + reindex**
```sh
$EDITOR agents.d/project/my-topic.md    # edit the file
starfleetctl agents reindex              # regenerate derived files
```

**Option B: Atomic write via CLI**
```sh
starfleetctl agents write project/my-topic my-changes.md   # from file
echo "new content" | starfleetctl agents write project/my-topic -  # from stdin
```

`agents write` replaces the fragment content and reindexes automatically.

### Viewing Fragments

```sh
starfleetctl agents list                 # all fragments: slug, title, order, owner
starfleetctl agents list --json          # JSON output
starfleetctl agents show project/foo     # print full file (frontmatter + body)
```

### Deleting a Fragment

```sh
rm agents.d/project/my-topic.md
starfleetctl agents reindex
```

There is no `agents delete` command — just remove the file and reindex.

### Committing Fragment Changes

```sh
starfleetctl agents commit project/my-topic -m "describe the change"
starfleetctl agents commit -m "update generated files"          # commit CLAUDE.md + index.md
starfleetctl agents commit project/my-topic -m "msg" --no-push  # local only
```

## Reindex

```sh
starfleetctl agents reindex
```

**What it does:** Reads every fragment file from both sources, strips
frontmatter, wraps each body in `<!-- begin/end inlined fragment: <slug> -->`
markers, sorts by `(order, slug)`, and writes:

1. `.starfleet-ai/var/agents.d/index.md` — the canonical fragment index
2. `CLAUDE.md` — same content with a CLAUDE.md header, for agents that
   don't resolve `@-imports` (e.g. opencode)

**Idempotent.** Two concurrent reindex operations converge to the same
byte-identical output. Safe to run from multiple sessions.

**When to run:**
- After editing any fragment file (unless you used `agents write`)
- After adding/removing fragment files
- After `agents new` (done automatically)
- After `agents write` (done automatically)
- After `agents install-starfleet` (done automatically)
- On bootstrap (`starfleet-bootstrap` / `starfleetctl bootstrap --fix`)

## Relationship to Skills

Fragments and skills serve different purposes:

| Aspect | Fragments (`agents.d/`) | Skills (`.claude/skills/`) |
|--------|------------------------|---------------------------|
| **Content** | Agent instructions (always active) | Reference docs (loaded on demand) |
| **Loading** | Inlined into system prompt at reindex | LLM calls `skill()` tool when needed |
| **Used by** | Claude Code + opencode (via `CLAUDE.md`) | Claude Code + opencode (native skill tool) |
| **Format** | `slug`/`title`/`order` frontmatter | `name`/`description` frontmatter |
| **Location** | `agents.d/` + `.starfleet-ai/var/agents.d/starfleet-instructions/` | `.claude/skills/<name>/SKILL.md` |

They overlap in content (e.g. the starfleet skill covers comms topics also
covered by fragments) but serve different loading models. Fragments are
**always present**; skills are **on-demand**.

## How Agents Receive Fragment Content

### Claude Code

Claude Code reads `CLAUDE.md` from the workspace root at session start.
The file contains all fragment bodies inlined. Alternatively, Claude Code
can use `@import` to reference individual fragments — but the generated
`CLAUDE.md` already contains everything.

### opencode

opencode reads the `instructions` path from `.opencode/opencode.json`:
```json
{ "instructions": [".starfleet-ai/var/agents.d/index.md"] }
```

This points to the auto-generated index containing all fragment bodies.
`starfleetctl bootstrap` merges (idempotent, preserving any existing keys) the
required opencode config into `.opencode/opencode.json`:

- `instructions` — registers `.starfleet-ai/var/agents.d/index.md` (dropping
  the legacy `.starfleet-ai/agents.d/index.md` path)
- `plugin` — registers the embedded starfleet plugins (see `fixOpencodePlugins`)
- `agent.plan.permission.bash` — explicit `allow` rules for the built-in
  `plan` agent to run the starfleetctl verbs `comms`, `dashboard`, `logs`,
  `reports`, `session`, `task` (read + write via the starfleetctl CLI)

opencode does **not** read `.claude/skills/` — skills are loaded via the
native `skill` tool instead.

## Embedded Fragments (starfleetctl Binary)

The starfleetctl binary carries its own instruction fragments compiled in
via Go's `//go:embed` directive. These are the source for auto-installed
starfleet-owned fragments.

```
fragments/
├── starfleet-instructions/       ← installed to .starfleet-ai/var/agents.d/starfleet-instructions/
│   ├── inter-ship-communication.md
│   ├── fleet-autonomous.md
│   ├── working-practices-*.md
│   └── comms-opencode-polling.md
├── starfleet-skills/             ← installed to .claude/skills/
│   └── starfleet/
│       ├── SKILL.md
│       └── reference.md
├── opencode-plugins/             ← installed to .opencode/plugins/
├── opencode-scripts/             ← installed to .starfleet-ai/bin/
├── claude-scripts/               ← installed to .starfleet-ai/bin/
└── claude-hooks/                 ← installed to .claude/hooks/
```

To modify an embedded fragment: edit the `.md` file under `fragments/` in
the starfleetctl repo, rebuild (`make all`), and run `starfleetctl agents
install-starfleet` in the workspace.

## CLI Reference

```
starfleetctl agents <command> [args…]

  list [--json]                              list all fragments (slug/title/order/owner)
  show <slug>                                print one fragment file
  write <slug> <file|->                      replace one fragment's content, then reindex
  new <slug> --title "<t>" [--order <n>] [--owner "<tool>"]
                                             scaffold a new fragment
  reindex                                    regenerate index.md + CLAUDE.md
  commit [<slug>] -m "<msg>" [--no-push]     commit+push one fragment (or generated files)
  install-self [--order <n>]                 install starfleet skill + clean legacy fragments
  install-starfleet [<subdir>]               install embedded starfleet instruction fragments
  install-starfleet-skills                   install embedded starfleet skill files
```
