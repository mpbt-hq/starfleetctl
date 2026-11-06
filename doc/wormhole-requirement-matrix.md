# Wormhole Manager Requirement Matrix (W0-017/W0-018/W0-024/W0-026/W0-129/W0-130/W0-134)

## Requirement ID: W0-017 — Wormhole Manager Core

| # | Requirement | Implementation | File | Test | Status |
|---|-------------|---------------|------|------|--------|
| W0-017.1 | Config struct with all fields | `WormholesConfig` with `Enabled`, `Sync`, `Repositories` | `internal/config/config.go` | `TestLoadConfigValid` | ✅ IMPLEMENTED + TESTED |
| W0-017.2 | Sync config with timeouts | `SyncConfig` with `Interval`, `StartupSync`, `CloneTimeout`, `CommandTimeout`, `PushRetries` | `internal/config/config.go` | `TestLoadConfigValid` | ✅ IMPLEMENTED + TESTED |
| W0-017.3 | Repository config | `RepositoryConfig` with `ID`, `Remote`, `Branch`, `Enabled`, `Access`, `Incoming` | `internal/config/config.go` | `TestLoadConfigValid` | ✅ IMPLEMENTED + TESTED |
| W0-017.4 | Incoming config with initial_scan | `IncomingConfig` with `Enabled`, `InitialScan` | `internal/config/config.go` | `TestLoadConfigValid` | ✅ IMPLEMENTED + TESTED |
| W0-017.5 | Work directory | `WorkDir(root)` returns `<root>/.starfleet-ai/var/wormholes` | `internal/wormhole/wormhole.go` | `TestPathHelpers` | ✅ IMPLEMENTED + TESTED |
| W0-017.6 | Ensure directory creation | `EnsureDir(root)` creates wormholes dir (idempotent) | `internal/wormhole/wormhole.go` | `TestEnsureDir`, `TestDaemonLifecycle` | ✅ IMPLEMENTED + TESTED |
| W0-017.7 | PID file path | `PIDFile(root)` returns `<root>/.starfleet-ai/var/wormholes/pid` | `internal/wormhole/wormhole.go` | `TestPathHelpers`, `TestDaemonLifecycle` | ✅ IMPLEMENTED + TESTED |
| W0-017.8 | State file path | `StateFile(root)` returns `<root>/.starfleet-ai/var/wormholes/state` | `internal/wormhole/wormhole.go` | `TestPathHelpers`, `TestDaemonLifecycle` | ✅ IMPLEMENTED + TESTED |
| W0-017.9 | Config file path | `ConfigFile(root)` returns `<root>/.starfleet-ai/conf/wormholes.yaml` | `internal/wormhole/wormhole.go` | `TestPathHelpers`, `TestLoadConfigMissingFileReturnsDefaults` | ✅ IMPLEMENTED + TESTED |
| W0-017.10 | Load config from file | `LoadConfig(root)` reads YAML, falls back to defaults | `internal/wormhole/wormhole.go` | `TestLoadConfigMissingFileReturnsDefaults` | ✅ IMPLEMENTED + TESTED |
| W0-017.11 | YAML config parsing | Strict YAML decoding with known-fields validation | `internal/wormhole/wormhole.go` | `TestLoadConfigRejectsLegacyKeys` | ✅ IMPLEMENTED + TESTED |

## Requirement ID: W0-018 — Wormhole Daemon Lifecycle

| # | Requirement | Implementation | File | Test | Status |
|---|-------------|---------------|------|------|--------|
| W0-018.1 | Start daemon | `StartDaemon(root)` forks child running `wormhole run-internal`; worker output goes to `daemon.log` (never inherits parent pipes) | `internal/wormhole/wormhole.go` | `TestDaemonLifecycle` | ✅ IMPLEMENTED + TESTED |
| W0-018.2 | Prevent double start | `StartDaemon` returns error if daemon already running | `internal/wormhole/wormhole.go` | `TestDaemonLifecycle`, `TestParallelStart` | ✅ IMPLEMENTED + TESTED |
| W0-018.3 | Stale PID cleanup | Stale PID (recorded instance dead) is cleaned up before starting; PID/state disagreement is a hard refusal (files preserved) | `internal/wormhole/wormhole.go` | `TestDaemonStalePID`, `TestDaemonStalePIDWithStateMismatch`, `TestUnverifiedPIDFilesFailClosed` | ✅ IMPLEMENTED + TESTED |
| W0-018.4 | Child writes own PID | `runInternal()` writes `os.Getpid()` to PID file | `internal/wormhole/cli.go` | `TestDaemonLifecycle` | ✅ IMPLEMENTED + TESTED |
| W0-018.5 | Reliable start confirmation | Parent polls for PID file match + "ready" file (30s deadline); early child exit detected immediately via reaped child, keeping the error state file for `status` | `internal/wormhole/wormhole.go` | `TestDaemonLifecycle`, `TestWorkerCrashDuringStartup`, `TestStaleReadyMarkerDoesNotFakeStart` | ✅ IMPLEMENTED + TESTED |
| W0-018.6 | Stop daemon | `StopDaemon()` re-verifies worker identity, SIGTERM, SIGKILL fallback, cleans up; dead recorded PID → stale cleanup; live-but-unverifiable PID → refuses to signal | `internal/wormhole/wormhole.go` | `TestDaemonLifecycle`, `TestNoForeignProcessKill` | ✅ IMPLEMENTED + TESTED |
| W0-018.7 | PID reuse protection | PID/state cross-check via `resolveDaemon`; worker identity verified via `/proc/<pid>/cmdline` + `MPBT_WORKSPACE_ROOT` environ | `internal/wormhole/wormhole.go` | `TestDaemonStalePIDWithStateMismatch`, `TestNoForeignProcessKill`, `TestUnverifiedPIDFilesFailClosed` | ✅ IMPLEMENTED + TESTED |
| W0-018.8 | Prevent double stop | `StopDaemon` reports "not running" only when the PID file is genuinely absent; a present-but-unusable PID file is a hard refusal (files preserved, no signal) | `internal/wormhole/wormhole.go` | `TestDaemonLifecycle`, `TestUnverifiedPIDFilesFailClosed` | ✅ IMPLEMENTED + TESTED |
| W0-018.9 | Status query | `Status()` resolves shared identity (PID + state + liveness), surfaces recorded startup error from kept state file | `internal/wormhole/wormhole.go` | `TestDaemonLifecycle`, `TestWorkerCrashDuringStartup` | ✅ IMPLEMENTED + TESTED |
| W0-018.10 | Is running check | `IsRunning()` cross-checks PID file and state file | `internal/wormhole/wormhole.go` | `TestDaemonAutostartIdempotent` | ✅ IMPLEMENTED + TESTED |
| W0-018.11 | Run loop | `RunLoop()` polls on interval; failed startup sync is fatal (state `error` + non-zero exit), per-cycle failures logged non-fatally; with `startup_sync: false` the daemon starts without sync and keeps running through later sync failures; handles SIGINT/SIGTERM | `internal/wormhole/wormhole.go` | `TestDaemonLifecycle`, `TestWorkerCrashDuringStartup`, `TestStartupSyncDisabledStartsWithoutSync`, `TestSyncFailureKeepsDaemonRunning` | ✅ IMPLEMENTED + TESTED |
| W0-018.12 | Autostart idempotent | `Autostart()` returns false immediately if already running; concurrent autostarts converge (race loser re-checks and returns false,nil) | `internal/wormhole/wormhole.go` | `TestDaemonAutostartIdempotent`, `TestParallelAutostart` | ✅ IMPLEMENTED + TESTED |
| W0-018.13 | Start serialization | Start lock serializes concurrent start/autostart attempts | `internal/wormhole/wormhole.go` | `TestParallelStart`, `TestParallelAutostart` | ✅ IMPLEMENTED + TESTED |
| W0-018.14 | Worker crash handling | Early child exit detected (no 30s wait); pid/ready cleaned, state file kept with error; crashed worker detected as dead; after a mid-run crash (SIGKILL) a restart converges without re-importing already-processed messages | `internal/wormhole/wormhole.go` | `TestWorkerCrashDuringStartup`, `TestWorkerCrashAfterStart`, `TestDaemonRestartMultiCycleReadOnly` | ✅ IMPLEMENTED + TESTED |
| W0-018.15 | No foreign process kill | Stop verifies PID is this workspace's wormhole worker (cmdline + environ) before signaling | `internal/wormhole/wormhole.go` | `TestNoForeignProcessKill` | ✅ IMPLEMENTED + TESTED |
| W0-018.16 | Unusable PID file fail-closed | `readPIDFile` distinguishes an absent PID file ("no registered instance") from a present-but-unusable one (unreadable/unparseable/empty/non-positive): start and stop refuse to act (no second worker spawned, no signal, runtime files preserved for manual inspection), `status` surfaces the refusal | `internal/wormhole/wormhole.go` | `TestUnverifiedPIDFilesFailClosed` | ✅ IMPLEMENTED + TESTED |

## Requirement ID: W0-024 — Message Handling (Git-Maildir)

| # | Requirement | Implementation | File | Test | Status |
|---|-------------|---------------|------|------|--------|
| W0-024.1 | Message struct | `Message` with `ID`, `From`, `To`, `Subject`, `Content`, `Created`, `Type`, `ReplyTo` | `internal/wormhole/messages.go` | `TestNewMessageUniqueIDsAndRoundtrip` | ✅ IMPLEMENTED + TESTED |
| W0-024.2 | Message creation | `NewMessage(from, to, typ, content, replyTo)` | `internal/wormhole/messages.go` | `TestNewMessageUniqueIDsAndRoundtrip` | ✅ IMPLEMENTED + TESTED |
| W0-024.3 | Message validation | `validateMessage()` checks required fields and path safety | `internal/wormhole/messages.go` | `TestParseRejectsUnknownFieldsAndTraversal` | ✅ IMPLEMENTED + TESTED |
| W0-024.4 | YAML serialization | `buildMessageBytes()` with strict front-matter | `internal/wormhole/messages.go` | `TestNewMessageUniqueIDsAndRoundtrip` | ✅ IMPLEMENTED + TESTED |
| W0-024.5 | Message parsing | `parseMessage()` with strict YAML decoding | `internal/wormhole/messages.go` | `TestParseRejectsUnknownFieldsAndTraversal` | ✅ IMPLEMENTED + TESTED |
| W0-024.6 | Maildir layout | `mail/<target>/{new,cur,processed,failed}/` | `internal/wormhole/spool.go` | `TestPublishProcessLifecycle` | ✅ IMPLEMENTED + TESTED |
| W0-024.7 | Publish | `Publish()` writes to `new/`, commits exact path, pushes | `internal/wormhole/spool.go` | `TestPublishProcessLifecycle` | ✅ IMPLEMENTED + TESTED |
| W0-024.8 | Idempotent publish | Re-publishing identical bytes succeeds; different bytes fails | `internal/wormhole/spool.go` | `TestPublishProcessLifecycle` | ✅ IMPLEMENTED + TESTED |
| W0-024.9 | Claim (new→cur) | `processNewLocked()` renames, commits+pushes claim | `internal/wormhole/spool.go` | `TestPublishProcessLifecycle` | ✅ IMPLEMENTED + TESTED |
| W0-024.10 | Import via PostExternal | `importLocked()` calls `bus.PostExternal()` with original payload | `internal/wormhole/spool.go` | `TestPublishProcessLifecycle` | ✅ IMPLEMENTED + TESTED |
| W0-024.11 | Process (cur→processed) | On success: rename cur→processed, commit+push | `internal/wormhole/spool.go` | `TestPublishProcessLifecycle` | ✅ IMPLEMENTED + TESTED |
| W0-024.12 | Failed messages | Permanent rejection → move to `failed/` with `.reason` diagnostic | `internal/wormhole/spool.go` | `TestMalformedMessageMovesToFailed`, `TestRecipientMismatchMovesToFailed` | ✅ IMPLEMENTED + TESTED |
| W0-024.13 | Import failure recovery | Failed import leaves message in `cur/` for retry | `internal/wormhole/spool.go` | `TestImportFailureKeepsMessageInCur` | ✅ IMPLEMENTED + TESTED |
| W0-024.14 | Crash recovery (dirty) | `commitDirtyMailLocked()` recovers uncommitted Maildir moves | `internal/wormhole/git_sync.go` | `TestCrashRecoveryCommitsDirtyTransition` | ✅ IMPLEMENTED + TESTED |
| W0-024.15 | Concurrent cycles | Repository lock serializes; exactly-once import | `internal/wormhole/spool.go` | `TestConcurrentCyclesImportExactlyOnce` | ✅ IMPLEMENTED + TESTED |
| W0-024.16 | Two-instance E2E | Separate workspaces share git repo; import idempotent | `internal/wormhole/spool.go` | `TestTwoInstanceRemoteImportE2E` | ✅ IMPLEMENTED + TESTED |
| W0-024.17 | Type/ReplyTo roundtrip | Message `Type` and `ReplyTo` survive the full pipeline into the Comms record | `internal/wormhole/spool.go` | `TestTypeAndReplyToRoundtrip` | ✅ IMPLEMENTED + TESTED |

## Requirement ID: W0-129/W0-130/W0-134 — Comms Transport Integration

| # | Requirement | Implementation | File | Test | Status |
|---|-------------|---------------|------|------|--------|
| W0-130.1 | Transport registration | `InstallCommsTransport()` registers "wormhole" transport | `internal/wormhole/comms_transport.go` | `TestCommsRoutingPublishesThroughWormhole` | ✅ IMPLEMENTED + TESTED |
| W0-130.2 | Outbound publish | `PublishRemoteMessage()` creates spool, publishes via `NewMessage` | `internal/wormhole/comms_transport.go` | `TestCommsRoutingPublishesThroughWormhole` | ✅ IMPLEMENTED + TESTED |
| W0-130.3 | Metadata preservation | `OutboundMessage.Type`/`ReplyTo` → envelope → `PostExternal` → `msgRecord` | `internal/wormhole/comms_transport.go`, `spool.go`, `comms_external.go` | `TestCommsRoutingPublishesThroughWormhole` (routing → envelope), `TestTypeAndReplyToRoundtrip` (envelope → record) | ✅ IMPLEMENTED + TESTED |
| W0-130.4 | Attachment rejection | Messages with `[[attach:...]]` rejected with clear error | `internal/wormhole/comms_transport.go` | `TestAttachmentPointerRejected` | ✅ IMPLEMENTED + TESTED |
| W0-134.1 | Read-only fetch | `SyncLocked()` fetches for read-only repos | `internal/wormhole/git_sync.go` | `TestReadOnlyMultiCycleImport` | ✅ IMPLEMENTED + TESTED |
| W0-134.2 | Read-only local import | `runOnce()` calls `processLocked()` even for read-only | `internal/wormhole/spool.go` | `TestReadOnlyMultiCycleImport` | ✅ IMPLEMENTED + TESTED |
| W0-134.3 | Read-only no push | Lifecycle transitions are committed locally (clean tree), never pushed; remote never advanced by the RO client | `internal/wormhole/spool.go`, `git_sync.go` | `TestReadOnlyMultiCycleImport`, `TestPublishRejectsReadOnly`, `TestDaemonRestartMultiCycleReadOnly` | ✅ IMPLEMENTED + TESTED |
| W0-134.4 | Publish rejected | `Publish()` returns error for read-only repos | `internal/wormhole/spool.go` | `TestPublishRejectsReadOnly` | ✅ IMPLEMENTED + TESTED |
| W0-134.5 | Read-only multi-cycle rebase | Local-only lifecycle commits rebase onto new remote history without directory-rename conflicts (`-X no-renames` + `merge.directoryRenames=false`); no re-import, no loss across cycles — including across a real daemon crash + restart (no re-import of already-processed messages, remote HEAD unchanged) | `internal/wormhole/git_sync.go` | `TestReadOnlyMultiCycleImport`, `TestDaemonRestartMultiCycleReadOnly` | ✅ IMPLEMENTED + TESTED |

## Requirement ID: W0-129/W0-134 — Initial Scan

| # | Requirement | Implementation | File | Test | Status |
|---|-------------|---------------|------|------|--------|
| IS-1 | Baseline mode | `performInitialScanLocked(mode=baseline)` moves new→processed without import; only future messages imported | `internal/wormhole/spool.go` | `TestInitialScanBaselineExcludesExisting` | ✅ IMPLEMENTED + TESTED |
| IS-2 | Replay mode | `performInitialScanLocked(mode=replay)` returns nil, normal import | `internal/wormhole/spool.go` | `TestTwoInstanceRemoteImportE2E`, `TestTypeAndReplyToRoundtrip` | ✅ IMPLEMENTED + TESTED |
| IS-3 | Persistent state | initial-scan marker in the clone-local state dir tracks completion | `internal/wormhole/spool.go` | `TestInitialScanBaselineExcludesExisting`, `TestInitialScanCrashAfterMoveBeforeMarker` | ✅ IMPLEMENTED + TESTED |
| IS-4 | Crash-safe init | Marker written after scan; restart without marker converges without importing moved messages | `internal/wormhole/spool.go` | `TestInitialScanCrashAfterMoveBeforeMarker` | ✅ IMPLEMENTED + TESTED |
| IS-5 | Crash during scan | Crash mid baseline move (partial rename, nothing committed, no marker): restart commits the dirty move, finishes the scan, imports nothing, leaves a clean tree | `internal/wormhole/spool.go`, `git_sync.go` | `TestInitialScanCrashDuringBaselineMove` | ✅ IMPLEMENTED + TESTED |

## Git Timeouts (W0-130)

| # | Requirement | Implementation | File | Test | Status |
|---|-------------|---------------|------|------|--------|
| GT-1 | Clone timeout | `GitSyncConfig.CloneTimeout` used in `EnsureCloneLocked` via `context.WithTimeout` | `internal/wormhole/git_sync.go` | `TestPublishProcessLifecycle` (exercises clone with `CloneTimeout` set; asserts `CloneDir` exists — normal path, does not force a clone timeout) | ✅ IMPLEMENTED + PARTIALLY TESTED |
| GT-2 | Command timeout | `GitSyncConfig.CommandTimeout` used in all `gitIndexLocked` calls | `internal/wormhole/git_sync.go` | `TestPushRecoversFromConcurrentRemoteUpdate` (exercises `gitIndexLocked`/push/fetch/rebase on the normal path; does not force a command timeout) | ✅ IMPLEMENTED + PARTIALLY TESTED |
| GT-3 | Timeout kills process | `exec.CommandContext` terminates the direct git process at the configured deadline (call returns a timeout error within ~deadline instead of hanging). No process-group/child-process kill is guaranteed or asserted. | `internal/wormhole/git_sync.go` | `TestGitCommandTimeoutKillsBlockedProcess` (`//go:build unix`; `GIT_CONFIG_GLOBAL` FIFO blocks git; observes the concrete direct git process via `/proc` or `ps` — BSD/POSIX forms tried, enumeration failure is fatal, not silent success — asserts deadline-bounded timeout error, direct process gone, no lingering match — for locked command, clone, gitConfigIn/gitRevParseIn/validateCloneDir; `currentHEAD` verified by observed blocked process + deadline-bounded empty return). Actually executed on linux/amd64; wormhole test package cross-compiles (`go test -c`) on linux/darwin/freebsd/openbsd/netbsd/illumos (amd64). Not executed on non-linux targets. | ✅ IMPLEMENTED + TESTED (Unix; direct process) |
| GT-4 | Lock released on timeout | `Lock()` uses `flock` with timeout; defer `Close()` releases on error | `internal/wormhole/git_sync.go` | `TestRepositoryLockIsShared` | ✅ IMPLEMENTED + TESTED |
| GT-5 | Config propagation | Timeouts parsed in `processPendingMessages`/`syncAllRepos` from `SyncConfig` and passed to the spool | `internal/wormhole/wormhole.go` | `TestSyncUsesConfiguredPushRetries` (proves `processPendingMessages` reads `SyncConfig` values); timeout fields exercised indirectly via GT-3/GT-6 on configured values — no dedicated test that forces a timeout through the propagation path alone | ✅ IMPLEMENTED + PARTIALLY TESTED |
| GT-6 | All git commands timed out | `gitConfigIn`, `gitRevParseIn`, `validateCloneDir`, `currentHEAD` apply `CommandTimeout` and do not hang on a blocked git process (direct process terminated) | `internal/wormhole/git_sync.go` | `TestGitCommandTimeoutKillsBlockedProcess` (`//go:build unix`; FIFO-blocked, concrete direct git process observed via `/proc` or `ps` — enumeration failure fatal — deadline-bounded timeout/return, direct process gone, no lingering match; `currentHEAD` API swallows error → deadline-bounded empty return verified). Actually executed on linux/amd64; wormhole test package cross-compiles (`go test -c`) on linux/darwin/freebsd/openbsd/netbsd/illumos (amd64). Not executed on non-linux targets. | ✅ IMPLEMENTED + TESTED (Unix; direct process) |
| GT-7 | Locale-independent git output | All GitSync git invocations (clone, locked commands) run with `LC_ALL=C` (pre-existing `LC_ALL` filtered: duplicate env entries resolve to the first occurrence), so push-rejection classification (`isNonFastForward`) and error reporting are deterministic regardless of host locale | `internal/wormhole/git_sync.go` | `TestSyncUsesConfiguredPushRetries` (on a localized host — e.g. de_DE.UTF-8 — the hook-rejection path is not classified as retryable without it), `TestGitCommandEnvForcesCLocale` (env contains exactly one `LC_ALL=C` despite ambient `LC_ALL`; real gitLocked error emitted in English under de_DE) | ✅ IMPLEMENTED + TESTED |

## PostExternal Crash Recovery (W0-130)

| # | Requirement | Implementation | File | Test | Status |
|---|-------------|---------------|------|------|--------|
| CR-1 | ID before record | `nextID()` called before `createExternalRecordLocked()` | `internal/comms/comms_external.go` | `TestPostExternalCrashAfterIDReservation` | ✅ IMPLEMENTED + TESTED |
| CR-2 | Missing CommsID recovery | Existing record without CommsID gets ID on next call | `internal/comms/comms_external.go` | `TestPostExternalCrashAfterRecordCreationNoCommsID` | ✅ IMPLEMENTED + TESTED |
| CR-3 | Recipient resolution recovery | `externalPosting` + nil Recipients → resolve + publish | `internal/comms/comms_external.go` | `TestPostExternalCrashDuringRecipientResolution` | ✅ IMPLEMENTED + TESTED |
| CR-4 | Partial publish recovery | Published recipients skipped; pending published | `internal/comms/comms_external.go` | `TestPostExternalCrashDuringRecipientPublishResumed` | ✅ IMPLEMENTED + TESTED |
| CR-5 | Complete state recovery | All published → mark complete | `internal/comms/comms_external.go` | `TestPostExternalCrashAfterAllPublishedBeforeComplete` | ✅ IMPLEMENTED + TESTED |
| CR-6 | Full restart simulation | New Bus instance resumes and completes | `internal/comms/comms_external.go` | `TestPostExternalFullRestartSimulation`, `TestPostExternalFullRestartSimulationWithLostSeq` | ✅ IMPLEMENTED + TESTED |
| CR-7 | Concurrent calls | Multiple goroutines calling same source/externalID → single record/ID | `internal/comms/comms_external.go` | `TestPostExternalConcurrentCalls` | ✅ IMPLEMENTED + TESTED |
| CR-8 | ID reservation fsync | `.seq` file is fsynced after write | `internal/comms/commands.go` | `TestPostExternalIDReservationCrashAfterSeq` | ✅ IMPLEMENTED + TESTED |
| CR-9 | External records in highestMsgID | `highestMsgID()` considers external records to prevent ID reuse | `internal/comms/commands.go` | `TestPostExternalNoDuplicateIDWithExternalRecord` | ✅ IMPLEMENTED + TESTED |

## Requirement ID: W0-026 — CLI Integration

| # | Requirement | Implementation | File | Test | Status |
|---|-------------|---------------|------|------|--------|
| W0-026.1 | CLI command `status` | `wormhole status` shows PID, status, started time | `internal/wormhole/cli.go` | `TestDaemonLifecycle` | ✅ IMPLEMENTED + TESTED |
| W0-026.2 | CLI command `sync` | `wormhole sync` one-shot sync + import, honoring the configured `sync.push_retries` (single config load inside `processPendingMessages`, no hardcoded divergence from the daemon RunLoop) | `internal/wormhole/cli.go` | `TestSyncUsesConfiguredPushRetries`, `TestPublishProcessLifecycle` (indirect) | ✅ IMPLEMENTED + TESTED |
| W0-026.3 | CLI command `send` | `wormhole send --to ShipB --text "msg"` publishes one message into the durable Maildir spool of a configured repo and pushes it to the remote (real CLI entrypoint, local bare remote) | `internal/wormhole/cli.go` | `TestSendCLIPublishesMessage` | ✅ IMPLEMENTED + TESTED |
| W0-026.4 | CLI command `start` | `wormhole start` forks daemon via `run-internal` | `internal/wormhole/cli.go` | `TestDaemonLifecycle` | ✅ IMPLEMENTED + TESTED |
| W0-026.5 | CLI command `stop` | `wormhole stop` SIGTERM + cleanup | `internal/wormhole/cli.go` | `TestDaemonLifecycle` | ✅ IMPLEMENTED + TESTED |
| W0-026.6 | CLI command `restart` | `wormhole restart` stop + start | `internal/wormhole/cli.go` | `TestDaemonLifecycle` | ✅ IMPLEMENTED + TESTED |
| W0-026.7 | CLI command `run-internal` | `wormhole run-internal` worker entry point | `internal/wormhole/cli.go` | `TestDaemonLifecycle` | ✅ IMPLEMENTED + TESTED |
| W0-026.8 | Subcommand registration | `wormhole` added to `starfleetctl` main switch | `cmd/starfleetctl/main.go` | (build compiles; exercised by CLI-invoking tests such as `TestDaemonLifecycle`, `TestSendCLIPublishesMessage` — no dedicated registration-only assertion) | ✅ IMPLEMENTED + TESTED (build/CLI exercise) |

## Requirement ID: W0-026b — Configuration File

| # | Requirement | Implementation | File | Test | Status |
|---|-------------|---------------|------|------|--------|
| W0-026b.1 | Config file schema | `.starfleet-ai/conf/wormholes.yaml` with full schema | `internal/config/config.go` | `TestLoadConfigValid` | ✅ IMPLEMENTED + TESTED |

## Requirement ID: W0-026c — Gitignore

| # | Requirement | Implementation | File | Test | Status |
|---|-------------|---------------|------|------|--------|
| W0-026c.1 | Gitignore wormholes dir | `/.starfleet-ai/var/wormholes/` in the starfleetctl repo `.gitignore` (and workspace `.gitignore`) | `.gitignore` | `TestGitignoreWormholes` (`git check-ignore -v`) | ✅ IMPLEMENTED + TESTED |

## Requirement ID: W0-026d — Documentation

| # | Requirement | Implementation | File | Test | Status |
|---|-------------|---------------|------|------|--------|
| W0-026d.1 | Wormhole documentation | `doc/wormhole.md` with architecture, config, CLI, lifecycle | `doc/wormhole.md` | — (manual review) | ✅ IMPLEMENTED + MANUALLY REVIEWED |
| W0-026d.2 | Config documentation | `doc/config.md` updated with wormholes.yaml section | `doc/config.md` | — (manual review) | ✅ IMPLEMENTED + MANUALLY REVIEWED |

## Requirement ID: W0-026e — Unit Tests

| # | Requirement | Implementation | File | Test | Status |
|---|-------------|---------------|------|------|--------|
| W0-026e.1 | Config tests | `TestLoadConfigMissingFileReturnsDefaults`, `TestLoadConfigValid`, etc. | `internal/wormhole/wormhole_test.go` | PASS | ✅ IMPLEMENTED + TESTED |
| W0-026e.2 | Message envelope tests | `TestNewMessageUniqueIDsAndRoundtrip`, `TestParseRejectsUnknownFieldsAndTraversal` | `internal/wormhole/wormhole_test.go` | PASS | ✅ IMPLEMENTED + TESTED |
| W0-026e.3 | Lifecycle tests | `TestPublishProcessLifecycle`, `TestRecipientMismatchMovesToFailed`, etc. | `internal/wormhole/wormhole_test.go` | PASS | ✅ IMPLEMENTED + TESTED |
| W0-026e.4 | Concurrency tests | `TestConcurrentCyclesImportExactlyOnce`, `TestRepositoryLockIsShared` | `internal/wormhole/wormhole_test.go` | PASS | ✅ IMPLEMENTED + TESTED |
| W0-026e.5 | E2E tests | `TestTwoInstanceRemoteImportE2E`, `TestLargePayloadRoundtripPreservesBytes` | `internal/wormhole/wormhole_test.go` | PASS | ✅ IMPLEMENTED + TESTED |
| W0-026e.6 | Daemon lifecycle tests | `TestDaemonLifecycle`, `TestDaemonStalePID`, `TestParallelStart`, etc. | `internal/wormhole/wormhole_test.go` | PASS | ✅ IMPLEMENTED + TESTED |
| W0-026e.7 | Comms external tests | `TestPostExternal`, `TestPostExternalIdempotent`, `TestPostExternalCrashAfterRecordCreationNoCommsID`, etc. | `internal/comms/comms_external_test.go` | PASS | ✅ IMPLEMENTED + TESTED |
| W0-026e.8 | ID reservation crash tests | `TestPostExternalIDReservationCrashAfterSeq`, `TestPostExternalNoDuplicateIDWithExternalRecord`, etc. | `internal/comms/comms_external_test.go` | PASS | ✅ IMPLEMENTED + TESTED |

## Branch Information

- **Branch**: `work/wormhole-manager-w0-017`
- **Base**: `9891c16` (origin/master; the branch was rebuilt/rebased onto current master — `git merge-base HEAD origin/master`)
- **Worktree**: `/home/nekrad/src/xorg/mpbt-workspace/_WORK_/starfleetctl/sources/starfleetctl`
- **HEAD**: latest commit on `work/wormhole-manager-w0-017` (see `git log --oneline -1`)

## Status Summary

- **Total requirement rows**: 94 (each row assigned to exactly one status group; no double counting)
- **IMPLEMENTED + TESTED**: 87 (includes W0-026.8: build compiles and exercised by CLI-invoking tests, no dedicated registration-only assertion)
- **IMPLEMENTED + PARTIALLY TESTED**: 3 (GT-1, GT-2, GT-5: normal-path/indirect coverage, no forced-timeout/propagation-only proof)
- **IMPLEMENTED + TESTED (Unix; direct process)**: 2 (GT-3, GT-6: `TestGitCommandTimeoutKillsBlockedProcess`, `//go:build unix`, actually executed on linux/amd64)
- **IMPLEMENTED + MANUALLY REVIEWED**: 2 (W0-026d.1, W0-026d.2: documentation)
- **IMPLEMENTED + NOT TESTED**: 0
- **NOT IMPLEMENTED**: 0
- **BLOCKED**: 0
- **Sum check**: 87 + 3 + 2 + 2 = 94

- **Tests**: all unit tests pass (`go test ./internal/wormhole/... ./internal/comms/... -count=1`, wormhole package ~75s including daemon lifecycle, parallel start/autostart, crash-during-startup, read-only multi-cycle and real daemon restart/crash-resync tests)
- **Build**: `go build ./...` + `go vet ./...` clean; `make check-plugin` (esbuild bundle) clean
- **Documentation**: `doc/wormhole.md`, `doc/config.md`, `doc/wormhole-requirement-matrix.md` updated
- **Gitignore**: `/.starfleet-ai/var/wormholes/` in the repo `.gitignore`, verified by `TestGitignoreWormholes`
- **Review follow-up**: independent review (Enterprise) blocker list addressed — plugin restored to pre-branch state (`starfleet-dispatch.ts` byte-identical to base), review test gaps closed (daemon restart/crash resync, `startup_sync: false`, sync-failure resilience, stale `ready` marker, fail-closed identity on missing/invalid state files); matrix rows updated accordingly
- **Merge-readiness corrections (final pass)**: present-but-unusable PID files are now a hard refusal instead of "no daemon" (start/stop refuse, files preserved — W0-018.16); `wormhole sync` honors `sync.push_retries` instead of a hardcoded 3 (W0-026.2, `TestSyncUsesConfiguredPushRetries`); git invocations run with `LC_ALL=C` so push-rejection classification is locale-independent (GT-7 — found by `TestSyncUsesConfiguredPushRetries` on a de_DE.UTF-8 host)
- **Honesty corrections**: GT-1/GT-2/GT-5 PARTIALLY TESTED (no forced-timeout/propagation-only proof); GT-3/GT-6 direct-process-only, `//go:build unix` forced-timeout test (`/proc`/`ps`, FIFO, `syscall.Kill`, enumeration failure fatal) — executed on linux/amd64, wormhole test package cross-compiles (`go test -c`) on linux/darwin/freebsd/openbsd/netbsd/illumos (amd64), not executed on non-linux targets; W0-026d docs MANUALLY REVIEWED; totals counted by rows (94) with sum check
- **Next**: merge to master