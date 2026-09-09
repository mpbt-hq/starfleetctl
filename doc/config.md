# Config files reference

starfleetctl reads its configuration from YAML/JSON files under
`.starfleet-ai/conf/` in the workspace root. All files are **optional**: a
missing file simply means defaults apply. The files and their layout:

| File | Purpose |
|---|---|
| [`project.yaml`](#projectyaml) | project identity, worktree layout, backport release lines |
| [`fleet.yaml`](#fleetyaml) | flagship name, worker ship-name pool, provider mode |
| [`web.yaml`](#webyaml) | web UI / console server |
| [`services.yaml`](#servicesyaml) | daemons auto-started with a flagship session |
| [`comms.yaml`](#commsyaml) | comms / opencode plugin tuning knobs |
| [`model-proxy.yaml`](#model-proxyyaml) | local model API proxy (upstream backends, providers) |
| [`models.yaml`](#modelsyaml) | model registry for the web UI (generated) |
| [`timers/*.json`](#timersjson) | persistent timers |
| [`ship-names.yaml`](#ship-namesyaml) | legacy ship-name pool override |

All paths that are relative (pid/log files) resolve against the workspace
root; runtime state lives under `.starfleet-ai/var/`. The workspace root
itself is discovered by walking up from the current directory (landmark:
`.starfleet-ai/` + `scripts/`), or via `MPBT_WORKSPACE_ROOT`.

The `web`, `comms`, `fleet`, `model_proxy` and `services` blocks are loaded
through the same loader (`internal/config/config.go`), each file declaring
its top-level key (`web:`, `comms:`, …). `project.yaml` has its own loader
(`internal/projectconfig/projectconfig.go`). Missing keys fall back to the
defaults summarized per section below.

---

## project.yaml

Project-specific paths and tooling configuration for the workspace.

```yaml
name: "xlibre-xserver"          # project name (drives {project} in path templates)

# SOP fragments directory name. Defaults to "sop.d"; the legacy "agents.d/"
# is still read but "sop.d" wins on collisions.
fragments_dir: "sop.d"

# Directory layout of release worktrees under _WORK_/
worktree_layout:
  base_dir: "xserver-{rel}"          # "_WORK_/<base_dir>", {rel}-expanded
  sources_subdir: "sources/xlibre/xserver"   # source tree inside the worktree
  agent_subdir: "agent/{name}/xserver"       # per-agent clones

# Path mapping toggled when a file lives under a different directory between
# branches (e.g. "Xext/glx/" on master vs "glx/" on a release line).
path_remapping:
  prefix: "Xext/"
  enabled: true

# Release lines maintained for backporting.
release_lines:
  - "25.2"
  - "25.1"
  - "25.0"

# Per-release-line solution references (config.sh, env var, name prefixes).
solutions:
  "xserver-25.2":
    config_path: "cf/xserver-25.2/config.sh"
    release_prefix: "xserver-"
    env_var: "XLIBRE_RELEASE"
```

**Fields:**

| Field | Type | Default | Description |
|---|---|---|---|
| `name` | string | `generic` | project name; `xserver`/`xlibre-xserver` normalize to `xserver` in path templates |
| `fragments_dir` | string | `sop.d` | directory name holding the SOP fragments |
| `worktree_layout.base_dir` | string | `{project}-{rel}` | worktree directory pattern under `_WORK_/` |
| `worktree_layout.sources_subdir` | string | `sources/{project}` | path of the source tree inside the worktree |
| `worktree_layout.agent_subdir` | string | `agent/{name}/{project}` | path of per-agent clones inside the worktree |
| `path_remapping.prefix` | string | `Xext/` | directory prefix to toggle between branches |
| `path_remapping.enabled` | bool | `false` | whether path remapping is active |
| `release_lines` | list | `["25.2","25.1","25.0"]` | maintained release lines |
| `solutions.<name>.config_path` | string | — | path to the solution's `config.sh` |
| `solutions.<name>.release_prefix` | string | — | prefix for release names (e.g. `xserver-`) |
| `solutions.<name>.env_var` | string | — | env var holding the release id |

---

## fleet.yaml

Fleet-wide identity and worker pool. Defaults: flagship `Enterprise`,
compiled-in Star Trek ship-name roster, provider mode `all`.

```yaml
fleet:
  flagship: "Enterprise"     # canonical control-agent identity (default "Enterprise")

  # Worker pool. When absent, the compiled-in roster is used.
  # Order matters: the first unused name is assigned. The flagship name is
  # always excluded from the pool.
  # ship_names:
  #   - Defiant
  #   - Voyager

  # provider_mode: "model-proxy-only"
```

**Fields:**

| Field | Type | Default | Description |
|---|---|---|---|
| `flagship` | string | `Enterprise` | name of the flagship / control session; excluded from the worker pool |
| `ship_names` | list | compiled-in roster | worker ship-name pool (first unused name wins) |
| `provider_mode` | string | `all` | `all` = user providers + model-proxy providers in ship configs; `model-proxy-only` = skip user providers and pin an `enabled_providers` allowlist of just the model-proxy backends |

---

## web.yaml

Web UI / console server.

```yaml
web:
  listen_addr: "0.0.0.0:8080"
  autostart_enabled: false
  pid_file: ".starfleet-ai/var/web.pid"
  log_file: ".starfleet-ai/var/log/web.log"

  # Bus identity (ship name) the web frontend registers under. Falls back to
  # STARFLEET_SHIP_ID / user@host when empty. Env override wins:
  # STARFLEET_WEB_SHIP_ID.
  ship_id: "McKinley"
  ship_handle: "McKinley"

  # termctl terminals spawned by this server.
  terminal_rows: 60
  terminal_cols: 120
  terminal_scrollback: 10000
```

**Fields:**

| Field | Type | Default | Description |
|---|---|---|---|
| `listen_addr` | string | `0.0.0.0:8080` | HTTP listen address (`STARFLEET_WEB_ADDR` overrides) |
| `autostart_enabled` | bool | `false` | `web autostart` keeps the daemon running |
| `pid_file` | string | `.starfleet-ai/var/web.pid` | daemon PID file |
| `log_file` | string | `.starfleet-ai/var/log/web.log` | daemon log |
| `ship_id` | string | — | bus identity of the web console (`STARFLEET_WEB_SHIP_ID` overrides) |
| `ship_handle` | string | — | human-readable handle shown alongside `ship_id` |
| `terminal_rows` | int | 60 | termctl terminal rows |
| `terminal_cols` | int | 120 | termctl terminal columns |
| `terminal_scrollback` | int | 10000 | termctl scrollback lines |

---

## services.yaml

Companion daemons pulled up automatically when a flagship session starts
(`starfleetctl run --flagship`).

```yaml
services:
  autostart:
    - web
    - model-proxy
    - timer
```

| Field | Type | Default | Description |
|---|---|---|---|
| `autostart` | list | `[]` (everything on-demand) | ordered service names to start: `web`, `model-proxy`, `timer` |

---

## comms.yaml

Optional tuning knobs for the comms bus heartbeat and the opencode plugin
polling. All defaults apply when the file (or key) is absent.

```yaml
comms:
  heartbeat_ms: 300000        # status heartbeat interval
  poll_ms: 3000               # inbox poll interval (plugin)
  fallback_model: "..."       # model used on generic failures
  retry_poll_ms: 2000         # poll interval while a retry is pending
  retry_cooldown_ms: 10000    # cooldown before re-attempting a failed action
  log_poll_ms: 10000          # log-spam suppression poll interval
  log_cooldown_ms: 10000      # log-spam suppression cooldown
```

| Field | Type | Default | Description |
|---|---|---|---|
| `heartbeat_ms` | int | 300000 | heartbeat interval for the status board |
| `poll_ms` | int | 3000 | comms inbox polling interval |
| `fallback_model` | string | — | fallback model used when the primary fails |
| `retry_poll_ms` | int | 2000 | polling interval during a retry |
| `retry_cooldown_ms` | int | 10000 | cooldown between retries |
| `log_poll_ms` | int | 10000 | log-spam suppression poll interval |
| `log_cooldown_ms` | int | 10000 | log-spam suppression cooldown |

---

## model-proxy.yaml

The local OpenAI-compatible proxy that fronts the real model API backends
(NVIDIA NIM, OpenCode Zen, Ollama, …). Ships talk to this single local
endpoint; the proxy retries transient upstream errors, catches streaming
failures, and tracks per-ship usage.

```yaml
model_proxy:
  listen_addr: "127.0.0.1:8443"        # bound to localhost only (holds real keys)
  # pid_file: ".starfleet-ai/var/model-proxy.pid"
  # log_file: ".starfleet-ai/var/log/model-proxy.log"

  providers:
    - id: nim-proxy                     # opencode provider name (namespace exposed to ships)
      name: "NVIDIA NIM (local proxy)"  # human label
      base_url: "https://integrate.api.nvidia.com/v1"
      api_key: "{env:NIM_API_KEY}"      # env-referenced; ships never see the real key
      # direct: false                   # true = expose upstream directly, bypass proxy
      # type: opencode-zen              # provider class (see Types below)
      # model_filter: "free-only"       # "", "all", "free-only", or id allowlist
      # max_retries: 3                  # default 3
      # retry_delay_ms: 1000            # default 1000
      # capabilities: [toolcall, temperature]  # forces caps on all models
```

The provider's `id` becomes the opencode provider name in every generated
per-ship config (here `nim-proxy`, `zen-proxy`, `ollama`, …). Model catalogs
are queried automatically from each upstream's `/models` endpoint — no manual
model list needed.

**Top-level fields:**

| Field | Type | Default | Description |
|---|---|---|---|
| `listen_addr` | string | `127.0.0.1:8443` | proxy listen address (localhost only!) |
| `pid_file` | string | `.starfleet-ai/var/model-proxy.pid` | daemon PID file |
| `log_file` | string | `.starfleet-ai/var/log/model-proxy.log` | daemon log |
| `providers` | list | — | ordered upstream backends |

**Provider fields:**

| Field | Type | Default | Description |
|---|---|---|---|
| `id` | string | (required) | opencode provider namespace exposed to ships |
| `name` | string | = `id` | human label |
| `base_url` | string | (required) | upstream OpenAI-compatible endpoint (env-expandable) |
| `api_key` | string | — | upstream API key; supports `{env:VAR}` / `${VAR}` / `$VAR` |
| `direct` | bool | `false` | expose the provider directly to ships (upstream URL + key) instead of proxying |
| `type` | string | `` | provider class driving upstream-specific handling; see Types |
| `user_agent` | string | — | upstream `User-Agent` override (mainly for `opencode-zen`) |
| `model_filter` | string | `all` | `all`/`""` = no filter; `free-only` = auto-filter free models; comma-separated ids = explicit allowlist |
| `max_retries` | int | 3 | retries on transient errors (429/5xx/conn-reset/ResourceExhausted) |
| `retry_delay_ms` | int | 1000 | delay between retries |
| `capabilities` | list | type-specific | force capability flags (`toolcall`, `temperature`, `reasoning`, `attachment`, …) on EVERY model entry of this provider in the generated ship config |

**Provider types:**

| `type` | Behavior |
|---|---|
| *(empty)* | generic OpenAI-compatible proxy |
| `opencode-zen` | OpenCode Zen. Stamps an `opencode/<version>` User-Agent, forwards the client's real session id (mapped from `X-Session-Id` → `x-opencode-session` so Zen's per-session prefix cache stays warm) and stamps the `x-opencode-client`/`x-opencode-project`/`x-opencode-request` identity headers. |
| `ollama` | Ollama (local). Its `/v1/models` catalog advertises no capabilities, so the type force-enables `toolcall` + `temperature` on every model entry in the generated config unless a `capabilities:` list overrides the set. |

**Env expansion:** `base_url` and `api_key` accept `{env:VAR}`, `${VAR}` and
`$VAR` references; they are expanded at load time. Referenced variable names
are also collected so the daemon can pick up missing keys from the user's
opencode config.

---

## models.yaml

Model registry used by the web UI (web console model dropdown / `models`
display). **Generated** by `starfleetctl models sync` from opencode's model
catalog — the auto fields (`id`, `provider`, `label`, `context`, `caps`) are
rewritten on rescan. Three fields are **hand-edited and preserved across
rescans**: `fallback`, `note`, `disabled`.

```yaml
# Auto-generated by models sync — do not hand-edit the auto fields.
# Regenerate: starfleetctl models sync
---
models:
  - id: "opencode/big-pickle"
    provider: opencode
    label: "big-pickle"
    context: 1048576
    caps:
      - reasoning
    # hand-edited, preserved on rescan:
    fallback: "nvidia/deepseek-ai/deepseek-v4-flash"   # used when this model is unavailable
    note: "free tier via zen-proxy"
    disabled: false
```

**Model fields:**

| Field | Type | Preserved | Description |
|---|---|---|---|
| `id` | string | no | model id (provider-prefixed) |
| `provider` | string | no | provider namespace |
| `label` | string | no | display name |
| `context` | int | no | context window size |
| `caps` | list | no | capability flags (`reasoning`, `attachment`, `temperature`, `toolcall`, …) |
| `fallback` | string | **yes** | fallback model used when this one is unavailable |
| `note` | string | **yes** | free-form note |
| `disabled` | bool | **yes** | exclude the model |

---

## timers/*.json

Persistent timers live under `.starfleet-ai/conf/timers/` (`--persistent` /
`--cron`); ephemeral ones under `.starfleet-ai/var/timers/` (lost on reset).
Each timer is one JSON file named `<id>.json`. Managed via `starfleetctl
timer` subcommands; hand-editing is possible but the CLI is preferred.

```json
{
  "id": "sweep-stale",
  "description": "Sweep stale tasks from dead ships",
  "owner": "Stargazer",
  "target": { "type": "system", "value": "" },
  "type": "system",
  "cmd": ["sweep-stale"],
  "schedule": { "type": "cron", "cron_expr": "*/10 * * * *" },
  "persistent": true,
  "enabled": true,
  "created_at": 1785706864,
  "next_fire": 1788946200
}
```

**Fields:**

| Field | Type | Description |
|---|---|---|
| `id` | string | unique name; also the filename (`<id>.json`) |
| `description` | string | human-readable description |
| `owner` | string | creator ship |
| `target` | object | `type`: `ship` / `fleet` / `fleet-all` / `system`; `value`: ship id for `ship` |
| `type` | string | `ship` / `command` (fire comms directives) or `system` (run workspace commands in the worker) |
| `text` | string | message body for ship/command timers |
| `cmd` | list | command line for system timers |
| `schedule` | object | `type`: `once` / `interval` / `cron`; plus `cron_expr`, `interval_sec`, `fire_at` |
| `timezone` | string | display timezone |
| `persistent` | bool | stored under `conf/timers/` (survives reset) vs `var/timers/` |
| `enabled` | bool | whether the timer is armed |
| `created_at` | int | unix timestamp |
| `next_fire` | int | unix timestamp of the next fire (managed by the worker) |

---

## ship-names.yaml

Legacy ship-name pool override (superseded by `fleet.yaml` `ship_names`).
`fleet.yaml` is consulted first; only when it defines no `ship_names` is this
file read, and only then do the compiled-in defaults apply.

```yaml
names:
  - Defiant
  - Voyager
```

---

## Environment variable reference

| Variable | Effect |
|---|---|
| `MPBT_WORKSPACE_ROOT` | absolute workspace root (overrides cwd discovery) |
| `MPBT_WORK_DIR` | runtime-state root; default `.starfleet-ai/var/` |
| `STARFLEET_WEB_ADDR` | overrides `web.listen_addr` |
| `STARFLEET_WEB_SHIP_ID` | overrides `web.ship_id` |
| `STARFLEET_SHIP_ID` | bus identity fallback for comms / web when no ship id is configured |
| `NIM_API_KEY`, `OPENCODE_API_KEY`, … | referenced from `model-proxy.yaml` via `{env:VAR}` |

---

## Related

- [USER.md](USER.md) — start here: installation, concepts, workflows
- [architecture.md](architecture.md) — data flow, file formats, locking
- [web-ui.md](web-ui.md) — web console