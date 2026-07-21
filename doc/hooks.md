# Bootstrap & Setup

How starfleetctl installs itself and configures a workspace.

## Genesis (one-time)

```sh
starfleetctl genesis-init .
```

**Run once per workspace.** Writes the `starfleet-bootstrap` script and
runs `bootstrap --fix` to set up everything. Requires an existing
starfleetctl binary (however it was built).

After genesis, **commit `starfleet-bootstrap`** to your repo — it's the
self-contained entry point that re-installs starfleetctl on any fresh clone.

## Bootstrap (idempotent)

```sh
./starfleet-bootstrap          # from committed script
starfleetctl bootstrap --fix   # or directly
```

Safe to run multiple times. Verifies and repairs everything:

| Component | Location | What it does |
|---|---|---|
| `./.starfleet-ai/var/...` dirs | `./.starfleet-ai/var/agent-bus`, `./.starfleet-ai/var/agent-claims/` | Bus storage, claim files |
| Allowlist | `.claude/settings.json` | Permits `starfleetctl` commands without prompts |
| Agent fragments | `.starfleet-ai/agents.d/` | Fleet coordination instructions |
| Skills | `.claude/skills/` | On-demand reference docs (concurrency, starfleetctl, etc.) |
| opencode plugins | `.opencode/plugins/` | Agent-bus polling, inbox injection |
| Launcher scripts | `.starfleet-ai/bin/` | `run-opencode.ship`, `run-opencode.flagship`, `run-claude.*` |
| Claude hooks | `.claude/hooks/` | `agent-permission-hook` for tool gating |
| Dashboard | `DASHBOARD.md` + `dashboard/topics/` | Project status tracking |

## What the launcher scripts do

The scripts installed by bootstrap (`run-opencode.ship`, `run-opencode.flagship`)
set up the environment so ships automatically participate in the fleet:

- `STARFLEET_SHIP_ID` — unique ship identity
- `STARFLEET_ROLE` — `flagship` or `ship`
- `STARFLEET_TARGET` — reporting target (ships point to flagship)
- `OPENCODE_CONFIG_CONTENT` — injects agent instructions into opencode

No manual starfleetctl calls needed in your workflow — the scripts handle it.

## self-install

```sh
cd /path/to/workspace
starfleetctl self-install
```

Clones/pulls starfleetctl source, builds it, and symlinks into
`.starfleet-ai/bin/`. Useful for updates when the binary is already installed.

## Claude Code Hooks

### SessionStart — monitor-hint

```sh
starfleetctl hook claude monitor-hint
```

Emits JSON telling the assistant to arm Monitor-tool watchers on its
agent-bus inbox. Wired as a `SessionStart` hook in `.claude/settings.json`.

### PreToolUse — permission

```sh
starfleetctl hook claude permission
```

Reads tool-invocation JSON from stdin, asks the control agent
(`$AGENT_CONTROLLER`) via agent-bus for allow/deny, blocks up to
`$AGENT_PERM_TIMEOUT` (default 60s).

| Variable | Default | Description |
|---|---|---|
| `AGENT_PERM_TIMEOUT` | `60` | Seconds to wait for control reply |
| `AGENT_PERM_TIMEOUT_DECISION` | `deny` | Action on timeout (`deny` or `ask`) |
| `AGENT_CONTROLLER` | `control` | Control agent ID |
