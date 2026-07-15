# Known Limitations

Current caveats and workarounds.

## agent-bus monitor-loop / fleet-watch

**Broken under Claude Code's Monitor tool.** Both correctly detect a backlog match (message existing when started) but fail to notice messages arriving while running — only when spawned via `Monitor`. The same binary works fine backgrounded via plain `&`.

**Workaround:** Use the bash originals (`scripts/agent-bus-monitor-loop`, `scripts/agent-bus-fleet-watch`), or use opencode's plugin-based polling instead.

## backport-commit path-remap

Uses `strings.ReplaceAll` (literal string replace) instead of regex, unlike the bash original which uses `sed` (basic regex where `.` matches any character). Behaviourally identical for all real paths in the source tree — plain `word/word/word.c` names with no regex metacharacters.

## xx-make-pr marker-leak (fixed 2026-07-07)

The old "rebase" mode leaked PR-number markers onto pushed upstream commits (PR #3162). Fixed in both Go and bash — now marks only the incubator branch via scripted `GIT_SEQUENCE_EDITOR`.

## Bash Interoperability

Go and bash implementations read/write the same file formats, but:
- Both must use the same `flock` domain (`_WORK_/agent-bus/.lock`)
- Don't mix `BUS_DIR` overrides between implementations
- Test interoperability before relying on it in production
