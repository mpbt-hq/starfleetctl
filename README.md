# starfleetctl

**One binary to coordinate AI-agent sessions working on the same codebase.**

starfleetctl manages concurrent agent sessions ("ships") — status heartbeats, cross-session directives, PR-branch locking, and GitHub PR access — so multiple agents can work in parallel without stepping on each other.

## Why?

When several AI agents share one repository, you need:

- **A status board** — who's working on what, who's idle, who's blocked
- **A message bus** — agents can send tasks and questions to each other
- **PR locking** — prevent two agents from editing the same branch
- **Safe commits** — concurrent sessions don't race the same working tree
- **GitHub integration** — view, comment, and label PRs without `jq`/`sed`

starfleetctl provides all of this as a single Go binary with no third-party dependencies.

## Quick Start

```sh
# Build
git clone https://github.com/metux/starfleetctl
cd starfleetctl
make build

# Bootstrap a workspace (one-time)
cd /path/to/your/workspace
starfleetctl genesis-init .

# Re-bootstrap (updates)
./starfleet-bootstrap
```

## Documentation

| Document | Description |
|---|---|
| **[User Guide](doc/USER.md)** | **Start here** — installation, concepts, workflows, troubleshooting |
| [Architecture](doc/architecture.md) | How it works — data flow, file formats, locking |
| [Agent Bus](doc/agent-bus.md) | Cross-session messaging reference |
| [Dashboard](doc/dashboard.md) | Project status tracking |
| [PR Claim](doc/pr-claim.md) | PR-branch locking |
| [Session](doc/session.md) | Agent session management (termctl) |
| [GitHub](doc/github.md) | PR viewing, commenting, labeling |
| [Hooks](doc/hooks.md) | Claude Code / opencode integration |
| [Web UI](doc/web-ui.md) | Browser-based fleet dashboard |
| [Known Limitations](doc/known-limitations.md) | Current caveats and workarounds |

## License

AGPL-3.0-or-later. See [`LICENSE`](LICENSE).
