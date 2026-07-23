# Known Limitations

Current caveats and workarounds.

## comms monitor-loop / fleet-watch

**Broken under Claude Code's Monitor tool.** Both correctly detect a backlog match (message existing when started) but fail to notice messages arriving while running — only when spawned via `Monitor`. The same binary works fine backgrounded via plain `&`.

**Workaround:** Use Go commands (`starfleetctl comms monitor-loop`, `starfleetctl comms fleet-watch`), or use opencode's plugin-based polling instead.

### opencode plugin polling (agent-facing)

opencode has no `Monitor` tool, so the `starfleet-dispatch.ts` plugin polls the comms and delivers incoming tell/broadcast directives in two ways:

1. **Toast notification** — each new message appears as a TUI toast popup with title `[fleet] <id> von <sender>` and full message text (auto-dismisses after ~10s). Implemented via `client.tui.showToast()`.
2. **System prompt injection** — at each turn, unseen directives are injected into the system prompt via `experimental.chat.system.transform` (fallback if polling hasn't run yet).

The legacy `autoPong()` responder (hardcoded to answer only Enterprise pings) has been removed. Ships now handle all messages natively through their model.

## backport-commit path-remap

Uses `strings.ReplaceAll` (literal string replace) instead of regex, unlike the bash original which uses `sed` (basic regex where `.` matches any character). Behaviourally identical for all real paths in the source tree — plain `word/word/word.c` names with no regex metacharacters.

## xx-make-pr marker-leak (fixed 2026-07-07)

The old "rebase" mode leaked PR-number markers onto pushed upstream commits (PR #3162). Fixed in both Go and bash — now marks only the incubator branch via scripted `GIT_SEQUENCE_EDITOR`.

## Bash Interoperability

Go and bash implementations read/write the same file formats, but:
- Both must use the same `flock` domain (`_WORK_/comms/.lock`)
- Don't mix `STARFLEET_BUS_DIR` overrides between implementations
- Test interoperability before relying on it in production
