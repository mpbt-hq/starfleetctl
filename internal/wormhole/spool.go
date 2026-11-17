// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// Repository-relative Maildir spool lifecycle.
//
// Layout (inside the configured wormhole checkout, i.e. <clone>/mail):
//
//	mail/<target-ship>/{new,cur,processed,failed}/
//
// A sender publishes one immutable file to mail/<target>/new/<id>. The
// recipient claims it (new -> cur) and durably commits+pushes that transition
// BEFORE importing it through Bus.PostExternal. Only after a confirmed import
// is it moved cur -> processed. Transient failures leave the message in cur/
// for recovery; permanently malformed messages move to failed/ with a durable
// diagnostic (the .reason file is made durable BEFORE the message is moved, so
// failed/ never contains a message without its diagnostic). Every transition is
// its own exact-path commit + push.
package wormhole

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"github.com/metux/starfleetctl/internal/comms"
	"github.com/metux/starfleetctl/internal/config"
)

const (
	mailDirName   = "mail"
	maildirNew    = "new"
	maildirCur    = "cur"
	maildirFailed = "failed"
	maildirDone   = "processed"
	mailTmpName   = ".tmp"
)

// MaildirRoot returns the Maildir spool root inside a repository checkout.
func MaildirRoot(checkout string) string {
	return filepath.Join(checkout, mailDirName)
}

// Spool is the Maildir spool of one configured repository for one local ship.
type Spool struct {
	root string
	repo config.RepositoryConfig
	git  *GitSync
	ship string
	// legacyOwner is the ship identity owning the legacy global initial-scan
	// marker (initial_scan_done). It is empty when no owner can be determined.
	legacyOwner string
}

// initialScanStateFile returns the path to the initial scan state file for a
// specific mailbox (ship). State is tracked per repository+ship so that a
// central multi-ship cycle establishes the baseline/replay independently for
// each mailbox.
func (s *Spool) initialScanStateFile(target string) string {
	if target == "" {
		target = s.ship
	}
	return filepath.Join(s.git.stateDir, "initial_scan_done."+target)
}

// legacyInitialScanStateFile returns the path of the legacy global initial
// scan marker, written by the single-ship implementation.
func (s *Spool) legacyInitialScanStateFile() string {
	return filepath.Join(s.git.stateDir, "initial_scan_done")
}

// initialScanMarkerExists reports whether the per-mailbox initial scan marker
// exists.
func (s *Spool) initialScanMarkerExists(target string) bool {
	_, err := os.Stat(s.initialScanStateFile(target))
	return err == nil
}

// hasInitialScanCompleted checks if initial scan has been done for this
// repo/ship.
//
// State is tracked per mailbox (initial_scan_done.<ship>). The legacy global
// marker (initial_scan_done) was written by the single-ship implementation and
// belongs to exactly one mailbox — the one of the ship that processed the
// repository back then. It is therefore only honored for that mailbox
// (legacyOwner); every other mailbox has to run its own baseline/replay scan.
func (s *Spool) hasInitialScanCompleted(target string) bool {
	if s.initialScanMarkerExists(target) {
		return true
	}
	if s.legacyOwner == "" || target != s.legacyOwner {
		return false
	}
	_, err := os.Stat(s.legacyInitialScanStateFile())
	return err == nil
}

// legacyScanOwner determines the mailbox owning the legacy global initial
// scan marker.
//
// The single-ship implementation wrote that marker after scanning exactly one
// mailbox: the mailbox of the ship identity the daemon was running under
// (STARFLEET_SHIP_ID). The marker carries no identity, so it is reconstructed
// from the state that is still available:
//
//   - the mailbox showing evidence of a previous scan (non-empty cur/ or
//     processed/) while not having a per-mailbox marker yet: only the scanned
//     mailbox ever had messages moved into those states. A single such
//     mailbox identifies the owner unambiguously.
//   - otherwise the daemon's explicitly configured ship identity, which is
//     exactly the identity the single-ship implementation used.
//
// If the owner cannot be determined, the legacy marker is inherited by no
// mailbox at all: every mailbox then performs its own baseline/replay scan.
func (s *Spool) legacyScanOwner(bus *comms.Bus) string {
	if owner := s.legacyScannedMailbox(); owner != "" {
		return owner
	}
	if bus != nil && bus.ShipIDSet && validToken(bus.ShipID) {
		return bus.ShipID
	}
	return ""
}

// legacyScannedMailbox returns the single mailbox showing evidence of a
// previous scan, or "" when there is none or more than one.
func (s *Spool) legacyScannedMailbox() string {
	entries, err := os.ReadDir(s.mailRoot())
	if err != nil {
		return ""
	}
	found := ""
	for _, e := range entries {
		if !e.IsDir() || !validToken(e.Name()) {
			continue
		}
		if s.initialScanMarkerExists(e.Name()) {
			continue // already initialized in the per-mailbox format
		}
		if !mailboxHasScanEvidence(filepath.Join(s.mailRoot(), e.Name())) {
			continue
		}
		if found != "" {
			return "" // ambiguous
		}
		found = e.Name()
	}
	return found
}

// mailboxHasScanEvidence reports whether the mailbox shows evidence of a
// previous scan, i.e. messages that were moved into cur/ or processed/.
func mailboxHasScanEvidence(dir string) bool {
	for _, state := range []string{maildirCur, maildirDone} {
		entries, err := os.ReadDir(filepath.Join(dir, state))
		if err != nil {
			continue
		}
		if len(entries) > 0 {
			return true
		}
	}
	return false
}

// markInitialScanCompleted records that initial scan is done.
func (s *Spool) markInitialScanCompleted(target string) error {
	if err := os.MkdirAll(s.git.stateDir, 0o755); err != nil {
		return err
	}
	return os.WriteFile(s.initialScanStateFile(target), []byte("done\n"), 0o644)
}

// newSpool creates a Spool bound to one repository checkout. ship may be empty
// for repo-sync-only use or when mailboxes are processed per-ship via
// RunCycleForShips.
func newSpool(root string, repo config.RepositoryConfig, pushRetries int, ship string, cloneTimeout, commandTimeout time.Duration) *Spool {
	return &Spool{
		root: root,
		repo: repo,
		git: NewGitSync(root, GitSyncConfig{
			ID:             repo.ID,
			Remote:         repo.Remote,
			Branch:         repo.Branch,
			Enabled:        true,
			PushRetries:    pushRetries,
			CloneTimeout:   cloneTimeout,
			CommandTimeout: commandTimeout,
		}),
		ship: ship,
		// A spool bound to one ship reproduces the single-ship situation: the
		// legacy global marker belongs to exactly that mailbox.
		legacyOwner: ship,
	}
}

func (s *Spool) mailRoot() string { return MaildirRoot(s.git.CloneDir()) }

func (s *Spool) targetDir(target string) string {
	return filepath.Join(s.mailRoot(), target)
}

func (s *Spool) abs(target, state, name string) string {
	return filepath.Join(s.targetDir(target), state, name)
}

// rel builds the repository-relative (slash-separated) path for a spool entry.
func (s *Spool) rel(target, state, name string) string {
	return filepath.ToSlash(filepath.Join(mailDirName, target, state, name))
}

// RunCycle syncs the repository and (when requested) processes the bound
// ship's mailbox under the single repository-scoped writer lock.
// Sync fetches remote changes and processes the mailbox; lifecycle transitions
// are committed locally and pushed to the remote.
func (s *Spool) runOnce(bus *comms.Bus, process bool) error {
	lock, err := s.git.Lock(60 * time.Second)
	if err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	defer lock.Close()

	if err := s.git.SyncLocked(); err != nil {
		return err
	}
	if !process {
		return nil
	}
	return s.processLocked(bus)
}

// RunCycleForShips performs one repository sync and then processes the mailbox
// of each ship in ships under the single repository-scoped writer lock. The
// repository is synced exactly once; duplicate ship entries are processed only
// once; mailbox failures are isolated so that an error in one ship's mailbox
// does not prevent processing of the remaining mailboxes. All errors are
// aggregated and returned.
func (s *Spool) RunCycleForShips(bus *comms.Bus, ships []string) error {
	lock, err := s.git.Lock(60 * time.Second)
	if err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	defer lock.Close()

	if err := s.git.SyncLocked(); err != nil {
		return err
	}

	// The legacy global initial-scan marker belongs to exactly one mailbox.
	// Determine its owner before any mailbox is touched, while the previous
	// state of the maildir is still intact.
	s.legacyOwner = s.legacyScanOwner(bus)

	var errs []error
	for _, ship := range uniqueShips(ships) {
		if !validToken(ship) {
			errs = append(errs, fmt.Errorf("invalid local ship identity %q", ship))
			continue
		}
		sp := *s
		sp.ship = ship
		if err := sp.processLocked(bus); err != nil {
			errs = append(errs, fmt.Errorf("ship %q: %w", ship, err))
		}
	}
	if len(errs) > 0 {
		return combineErrors(errs)
	}
	return nil
}

// ensureLayoutLocked creates the fixed lifecycle directories. Caller holds the
// repository lock.
func (s *Spool) ensureLayoutLocked(target string) error {
	for _, dir := range []string{
		filepath.Join(s.targetDir(target), maildirNew),
		filepath.Join(s.targetDir(target), maildirCur),
		filepath.Join(s.targetDir(target), maildirDone),
		filepath.Join(s.targetDir(target), maildirFailed),
		filepath.Join(s.mailRoot(), mailTmpName),
	} {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("mkdir %s: %w", dir, err)
		}
	}
	// Drop leftovers of a crashed publish/failed transition. The repository
	// lock is held by the caller, so the staging dir cannot be in use.
	return s.cleanupStagingLocked()
}

// Publish writes one immutable message into mail/<to>/new/<id>, commits exactly
// that path and pushes it. Re-publishing identical bytes is idempotent; the
// same id with different bytes fails closed.
func (s *Spool) Publish(msg *Message) error {
	if err := validateMessage(msg); err != nil {
		return err
	}
	s.ship = msg.From

	lock, err := s.git.Lock(60 * time.Second)
	if err != nil {
		return fmt.Errorf("lock: %w", err)
	}
	defer lock.Close()

	if err := s.git.SyncLocked(); err != nil {
		return fmt.Errorf("sync: %w", err)
	}

	data := buildMessageBytes(msg)
	name := msg.ID + ".md"
	rel := s.rel(msg.To, maildirNew, name)
	abs := s.abs(msg.To, maildirNew, name)

	if existing, err := os.ReadFile(abs); err == nil {
		if bytes.Equal(existing, data) {
			return s.git.CommitAndPushLocked([]string{rel}, "wormhole: publish "+msg.ID)
		}
		return fmt.Errorf("collision: %s already exists with different bytes", rel)
	}
	// A message id must be unique across the whole lifecycle of this mailbox.
	for _, state := range []string{maildirCur, maildirDone, maildirFailed} {
		if _, err := os.Stat(s.abs(msg.To, state, name)); err == nil {
			return fmt.Errorf("collision: message %s already present in %s/", msg.ID, state)
		}
	}

	if err := s.ensureLayoutLocked(msg.To); err != nil {
		return err
	}
	// Durable write: temp file in the git-excluded staging dir, fsync of the
	// file CONTENT before the rename, atomic rename, fsync of source and
	// destination directory. A crash can therefore never publish a partial
	// or zero-length message file.
	if err := s.writeFileDurableLocked(abs, data, 0o644); err != nil {
		return fmt.Errorf("atomic publish: %w", err)
	}
	return s.git.CommitAndPushLocked([]string{rel}, "wormhole: publish "+msg.ID)
}

// processLocked processes the local ship's mailbox. Caller holds the lock.
// Lifecycle transitions are always committed locally (so the working tree is
// never left dirty) and pushed to the remote.
func (s *Spool) processLocked(bus *comms.Bus) error {
	target := s.ship
	if !validToken(target) {
		return fmt.Errorf("invalid local ship identity %q", target)
	}
	if err := s.ensureLayoutLocked(target); err != nil {
		return err
	}

	// Handle initial scan if not yet completed.
	if !s.hasInitialScanCompleted(target) {
		if err := s.performInitialScanLocked(bus, target); err != nil {
			return fmt.Errorf("initial scan: %w", err)
		}
	}
	// Record the per-mailbox marker in any case: it migrates a legacy global
	// marker inherited by this mailbox and makes the per-mailbox state the
	// single source of truth from then on.
	if !s.initialScanMarkerExists(target) {
		if err := s.markInitialScanCompleted(target); err != nil {
			return fmt.Errorf("mark initial scan done: %w", err)
		}
	}

	var errs []error
	if err := s.processCurLocked(bus, target, &errs); err != nil {
		errs = append(errs, err)
	}
	if err := s.processNewLocked(bus, target, &errs); err != nil {
		errs = append(errs, err)
	}
	if len(errs) > 0 {
		return combineErrors(errs)
	}
	return nil
}

// performInitialScanLocked handles the initial scan of the mailbox on first run.
// Per W0-129/W0-134: initial_scan can be "baseline" or "replay".
// - baseline: mark all existing messages as processed without importing (establish baseline)
// - replay: import all existing messages normally
// The baseline transition is committed locally and pushed to the remote.
func (s *Spool) performInitialScanLocked(bus *comms.Bus, target string) error {
	mode := s.repo.Incoming.InitialScan
	if mode == "" {
		mode = "baseline" // default
	}

	newDir := filepath.Join(s.targetDir(target), maildirNew)
	entries, err := os.ReadDir(newDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil // no messages to scan
		}
		return fmt.Errorf("read new/: %w", err)
	}

	if len(entries) == 0 {
		return nil // no messages to scan
	}

	switch mode {
	case "baseline":
		// Move all messages from new/ to processed/ without importing.
		// This establishes a baseline: only future messages will be imported.
		var rels []string
		for _, e := range entries {
			if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
				continue
			}
			name := e.Name()
			newAbs := s.abs(target, maildirNew, name)
			procAbs := s.abs(target, maildirDone, name)
			if err := renameDurableLocked(newAbs, procAbs); err != nil {
				return fmt.Errorf("baseline move %s: %w", name, err)
			}
			rels = append(rels, s.rel(target, maildirNew, name), s.rel(target, maildirDone, name))
		}
		if len(rels) > 0 {
			return s.git.CommitAndPushLocked(rels, "wormhole: initial scan baseline")
		}
		return nil

	case "replay":
		// Import all existing messages normally.
		// processNewLocked will handle the import.
		return nil // let normal processing handle it

	default:
		return fmt.Errorf("unknown initial_scan mode %q", mode)
	}
}

func (s *Spool) processNewLocked(bus *comms.Bus, target string, errs *[]error) error {
	newDir := filepath.Join(s.targetDir(target), maildirNew)
	entries, err := os.ReadDir(newDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read new/: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		name := e.Name()
		newAbs := s.abs(target, maildirNew, name)
		data, err := os.ReadFile(newAbs)
		if err != nil {
			*errs = append(*errs, fmt.Errorf("read %s: %w", name, err))
			continue
		}
		msg, perr := s.parseForTarget(data, name, target)
		if perr != nil {
			// Permanent, local rejection: record it durably in failed/ with a
			// diagnostic instead of retrying forever.
			if ferr := s.failLocked(target, maildirNew, name, perr); ferr != nil {
				*errs = append(*errs, ferr)
			}
			continue
		}

		curAbs := s.abs(target, maildirCur, name)
		if _, err := os.Stat(curAbs); err == nil {
			if ferr := s.failLocked(target, maildirNew, name, fmt.Errorf("claim collision: %s/%s already exists", maildirCur, name)); ferr != nil {
				*errs = append(*errs, ferr)
			}
			continue
		}
		if err := renameDurableLocked(newAbs, curAbs); err != nil {
			*errs = append(*errs, fmt.Errorf("claim %s: %w", msg.ID, err))
			continue
		}
		// The claim must be durably committed before any import is attempted.
		// The commit is pushed to the remote.
		rels := []string{s.rel(target, maildirNew, name), s.rel(target, maildirCur, name)}
		if err := s.git.CommitAndPushLocked(rels, "wormhole: claim "+msg.ID); err != nil {
			*errs = append(*errs, fmt.Errorf("claim %s not committed (kept in cur/): %w", msg.ID, err))
			continue
		}
		s.importLocked(bus, target, msg, data, name, errs)
	}
	return nil
}

func (s *Spool) processCurLocked(bus *comms.Bus, target string, errs *[]error) error {
	curDir := filepath.Join(s.targetDir(target), maildirCur)
	entries, err := os.ReadDir(curDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read cur/: %w", err)
	}
	for _, e := range entries {
		if e.IsDir() || !strings.HasSuffix(e.Name(), ".md") {
			continue
		}
		name := e.Name()
		curAbs := s.abs(target, maildirCur, name)
		data, err := os.ReadFile(curAbs)
		if err != nil {
			*errs = append(*errs, fmt.Errorf("read cur/%s: %w", name, err))
			continue
		}
		msg, perr := s.parseForTarget(data, name, target)
		if perr != nil {
			if ferr := s.failLocked(target, maildirCur, name, perr); ferr != nil {
				*errs = append(*errs, ferr)
			}
			continue
		}
		// Re-assert the claim durably before importing: this also recovers a
		// crash between the local rename and the claim commit. The commit is pushed.
		rels := []string{s.rel(target, maildirNew, name), s.rel(target, maildirCur, name)}
		if err := s.git.CommitAndPushLocked(rels, "wormhole: claim "+msg.ID); err != nil {
			*errs = append(*errs, fmt.Errorf("resume claim %s not committed: %w", msg.ID, err))
			continue
		}
		s.importLocked(bus, target, msg, data, name, errs)
	}
	return nil
}

// parseForTarget parses and validates a message for a specific mailbox. The
// immutable id must match the file name, and the recipient must match the
// mailbox (recipient authorization).
func (s *Spool) parseForTarget(data []byte, name, target string) (*Message, error) {
	msg, err := parseMessage(data)
	if err != nil {
		return nil, err
	}
	if got := strings.TrimSuffix(name, ".md"); got != msg.ID {
		return nil, fmt.Errorf("message id %q does not match file name %q", msg.ID, name)
	}
	if msg.To != target {
		return nil, fmt.Errorf("recipient %q does not match mailbox %q", msg.To, target)
	}
	return msg, nil
}

// importLocked imports one confirmed-claimed message through Comms Core and
// advances cur -> processed only after a successful import. A transient import
// error leaves the message recoverable in cur/ (no arbitrary retry cap). A
// permanent import error is moved to failed/ with a durable, traceable reason;
// if moving to failed/ itself errors the message must not be lost (it remains
// in cur/ or failed/). The processed transition is always committed locally
// and pushed to the remote.
func (s *Spool) importLocked(bus *comms.Bus, target string, msg *Message, data []byte, name string, errs *[]error) {
	// Bus.PostExternal(source, externalID, from, target, text):
	//  - source is the stable configured repository identity,
	//  - externalID is the immutable message id,
	//  - from is the logical sender address used for replies/routing (falls back
	//    to the canonical envelope sender when no logical address is present),
	//  - text is the exact original payload bytes (never reconstructed) — it
	//    carries the message front matter (type, reply_to) unchanged.
	fromAddr := msg.From
	if msg.Sender != "" {
		fromAddr = msg.Sender
	}
	if _, err := bus.PostExternal(s.repo.ID, msg.ID, fromAddr, target, string(data)); err != nil {
		if isPermanentPostExternalError(err) {
			if ferr := s.failLocked(target, maildirCur, name, fmt.Errorf("permanent import rejection: %w", err)); ferr != nil {
				*errs = append(*errs, fmt.Errorf("import %s permanently rejected but failed transition incomplete (message not lost): %v; move: %w", msg.ID, err, ferr))
			}
			return
		}
		*errs = append(*errs, fmt.Errorf("import %s failed (kept in cur/): %w", msg.ID, err))
		return
	}
	curAbs := s.abs(target, maildirCur, name)
	procAbs := s.abs(target, maildirDone, name)
	if err := renameDurableLocked(curAbs, procAbs); err != nil {
		*errs = append(*errs, fmt.Errorf("processed move %s: %w", msg.ID, err))
		return
	}
	rels := []string{s.rel(target, maildirCur, name), s.rel(target, maildirDone, name)}
	if err := s.git.CommitAndPushLocked(rels, "wormhole: processed "+msg.ID); err != nil {
		*errs = append(*errs, fmt.Errorf("processed transition %s not committed: %w", msg.ID, err))
	}
}

// isPermanentPostExternalError reports whether a PostExternal failure is a
// permanent rejection that must not be retried (the message cannot ever become
// importable with the same identity/payload). Classification is type-based
// (errors.Is/As via comms.IsPermanentPostExternalError), never string matching.
func isPermanentPostExternalError(err error) bool {
	return comms.IsPermanentPostExternalError(err)
}

// isDirSyncUnsupported reports whether a directory-fsync failure means the
// platform or filesystem does not support fsync on directories at all. Such a
// failure must not fail a lifecycle transition: durability then degrades to
// the platform's guarantees, but the transition itself stays valid. Real I/O
// errors (EIO and friends) are still reported. EBADF is deliberately NOT
// treated as unsupported: an invalid descriptor is a bug in our own code and
// must stay a hard error instead of silently skipping the fsync.
func isDirSyncUnsupported(err error) bool {
	var errno syscall.Errno
	if !errors.As(err, &errno) {
		return false
	}
	switch errno {
	case syscall.EINVAL, syscall.ENOSYS, syscall.EPERM:
		// EINVAL: some platforms (e.g. macOS) reject fsync on a directory
		// fd; ENOSYS: not implemented; EPERM: some network filesystems
		// refuse directory fsync.
		return true
	}
	// EOPNOTSUPP and ENOTSUP denote the same capability but are distinct
	// errno values on some platforms (and identical on Linux, where a
	// duplicate case or map key would not compile). Compare them separately
	// so the classification stays portable.
	return errno == syscall.EOPNOTSUPP || errno == syscall.ENOTSUP
}

// syncDir fsyncs a directory so that renames/creations inside it become
// durable. Where directory fsync is unsupported, the call degrades to a no-op
// (see isDirSyncUnsupported).
func syncDir(dir string) error {
	d, err := os.Open(dir)
	if err != nil {
		return fmt.Errorf("open dir %s: %w", dir, err)
	}
	defer d.Close()
	if err := d.Sync(); err != nil {
		if isDirSyncUnsupported(err) {
			return nil
		}
		return fmt.Errorf("fsync dir %s: %w", dir, err)
	}
	return nil
}

// renameDurableLocked atomically renames src to dst and fsyncs BOTH the
// source and the destination directory, so the rename survives a crash on
// either side (the file can neither vanish nor resurrect in its old place).
// Where directory fsync is unsupported, durability degrades to the platform's
// guarantees instead of failing the transition.
func renameDurableLocked(src, dst string) error {
	if err := os.Rename(src, dst); err != nil {
		return err
	}
	if err := syncDir(filepath.Dir(dst)); err != nil {
		return fmt.Errorf("fsync destination dir of %s: %w", dst, err)
	}
	if err := syncDir(filepath.Dir(src)); err != nil {
		return fmt.Errorf("fsync source dir of %s: %w", src, err)
	}
	return nil
}

// writeFileDurableLocked writes data to a temp file inside the spool staging
// dir (mail/.tmp/, git-excluded via .git/info/exclude), fsyncs the file,
// atomically renames it to dst and fsyncs the destination directory. A crash
// can therefore never leave a partial or missing file at dst.
func (s *Spool) writeFileDurableLocked(dst string, data []byte, perm os.FileMode) error {
	tmpDir := filepath.Join(s.mailRoot(), mailTmpName)
	if err := os.MkdirAll(tmpDir, 0o755); err != nil {
		return fmt.Errorf("mkdir %s: %w", mailTmpName, err)
	}
	tmp := filepath.Join(tmpDir, "."+strconv.FormatInt(time.Now().UnixNano(), 10)+"."+strconv.Itoa(os.Getpid())+".tmp")
	f, err := os.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, perm)
	if err != nil {
		return fmt.Errorf("create temp file: %w", err)
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("write temp file: %w", err)
	}
	if err := f.Sync(); err != nil {
		f.Close()
		os.Remove(tmp)
		return fmt.Errorf("fsync temp file: %w", err)
	}
	if err := f.Close(); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("close temp file: %w", err)
	}
	if err := renameDurableLocked(tmp, dst); err != nil {
		os.Remove(tmp)
		return fmt.Errorf("atomic rename: %w", err)
	}
	return nil
}

// cleanupStagingLocked removes leftover staging files in mail/.tmp/, i.e.
// files of a publish or failed transition that crashed mid-write. The caller
// holds the repository lock, so no other writer can own the staging dir at
// this moment. The staging dir is git-excluded, so leftovers never enter a
// commit; this only bounds their disk usage.
func (s *Spool) cleanupStagingLocked() error {
	tmpDir := filepath.Join(s.mailRoot(), mailTmpName)
	entries, err := os.ReadDir(tmpDir)
	if err != nil {
		if os.IsNotExist(err) {
			return nil
		}
		return fmt.Errorf("read %s: %w", mailTmpName, err)
	}
	removed := false
	for _, e := range entries {
		if e.IsDir() {
			continue
		}
		if err := os.Remove(filepath.Join(tmpDir, e.Name())); err != nil && !os.IsNotExist(err) {
			return fmt.Errorf("remove staging file %s: %w", e.Name(), err)
		}
		removed = true
	}
	if !removed {
		return nil
	}
	return syncDir(tmpDir)
}

// failLocked moves a permanently malformed/rejected message to failed/ together
// with a durable diagnostic file, then commits+pushes all exact paths.
//
// Crash safety: the .reason diagnostic is written durably into failed/ BEFORE
// the message itself is moved there, so a crash can never leave a message in
// failed/ without its .reason. A crash after the reason write but before the
// rename leaves the message in its previous state; the next cycle repeats the
// transition deterministically. The rename itself is fsynced on BOTH sides
// (source and destination directory), so a crash can neither resurrect the
// message in its old state nor lose it from failed/; where directory fsync is
// unsupported by the platform/filesystem this degrades to a no-op instead of
// failing the transition. A crash after the rename but before the commit is
// recovered by commitDirtyMailLocked on the next sync, which commits .md and
// .reason together. If the reason write itself fails, the message stays in its
// current state and is never lost. Leftover staging files in the git-excluded
// mail/.tmp/ are removed at the start of every locked cycle.
func (s *Spool) failLocked(target, fromState, name string, reason error) error {
	srcAbs := s.abs(target, fromState, name)
	failedDir := filepath.Join(s.targetDir(target), maildirFailed)
	if err := os.MkdirAll(failedDir, 0o755); err != nil {
		return fmt.Errorf("mkdir failed/: %w", err)
	}

	dstName := name
	if _, err := os.Stat(filepath.Join(failedDir, dstName)); err == nil {
		base := strings.TrimSuffix(name, ".md")
		dstName = fmt.Sprintf("%s.failed-%d.md", base, time.Now().UnixNano())
	}
	dstAbs := filepath.Join(failedDir, dstName)

	diagName := dstName + ".reason"
	diagAbs := filepath.Join(failedDir, diagName)
	diag := fmt.Sprintf("message: %s\nmailbox: %s\nfrom: %s\nfailed_at: %s\nreason: %v\n",
		dstName, target, fromState, time.Now().UTC().Format(time.RFC3339), reason)
	// Durable diagnostic first: once the message lands in failed/ the reason is
	// guaranteed to be there as well.
	if err := s.writeFileDurableLocked(diagAbs, []byte(diag), 0o644); err != nil {
		return fmt.Errorf("write failure diagnostic: %w", err)
	}

	if err := renameDurableLocked(srcAbs, dstAbs); err != nil {
		return fmt.Errorf("move %s to failed/: %w", name, err)
	}

	rels := []string{
		s.rel(target, fromState, name),
		s.rel(target, maildirFailed, dstName),
		s.rel(target, maildirFailed, diagName),
	}
	if err := s.git.CommitAndPushLocked(rels, "wormhole: failed "+name); err != nil {
		return fmt.Errorf("failed transition %s not committed: %w", name, err)
	}
	return nil
}

// combineErrors joins multiple errors while preserving each one's text.
func combineErrors(errs []error) error {
	if len(errs) == 0 {
		return nil
	}
	if len(errs) == 1 {
		return errs[0]
	}
	parts := make([]string, 0, len(errs))
	for _, err := range errs {
		parts = append(parts, err.Error())
	}
	return fmt.Errorf("%s", strings.Join(parts, "; "))
}
