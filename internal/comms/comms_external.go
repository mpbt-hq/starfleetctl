// SPDX-License-Identifier: AGPL-3.0-or-later
// Copyright © 2026 Enrico Weigelt, metux IT consult
//
// External message delivery via the comms bus.
// Implements crash-idempotent message publishing through the Comms bus,
// using a durable external journal under <BusDir>/external/<key>.json.
// Same source+externalID always yields the same CommsID; a changed payload
// is a hard integrity error.
package comms

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"time"
)

// PermanentPostExternalError marks a PostExternal failure as permanent: the
// same external message identity/payload cannot become importable through
// retry. Wormhole treats such failures as non-retryable (move to failed/).
type PermanentPostExternalError struct {
	Reason string
}

func (e *PermanentPostExternalError) Error() string { return e.Reason }

func permanentPostExternalError(format string, args ...any) error {
	return &PermanentPostExternalError{Reason: fmt.Sprintf(format, args...)}
}

// IsPermanentPostExternalError reports whether err is a permanent PostExternal
// rejection.
func IsPermanentPostExternalError(err error) bool {
	var perr *PermanentPostExternalError
	return errors.As(err, &perr)
}

// externalRecord is the canonical durable record for an externally sourced
// message. It lives under <BusDir>/external/<key>.json and serves as the
// idempotency tombstone: same source+externalID always yields the same
// CommsID, and the same payload hash means "resume this publication."
type externalRecord struct {
	Version    int    `json:"version"`
	Source     string `json:"source"`
	ExternalID string `json:"external_id"`
	PayloadSHA string `json:"payload_sha256"`
	Payload    string `json:"payload"`
	From       string `json:"from"`
	Target     string `json:"target"`
	// Comms metadata preserved through transport.
	Type       string              `json:"type,omitempty"`
	ReplyTo    string              `json:"reply_to,omitempty"`
	CommsID    string              `json:"comms_id,omitempty"`
	CreatedAt  string              `json:"created_at"`
	State      externalPostState   `json:"state"`
	Recipients []externalRecipient `json:"recipients,omitempty"`
}

type externalPostState string

const (
	externalPendingRecipients externalPostState = "pending_recipients"
	externalPosting           externalPostState = "posting"
	externalComplete          externalPostState = "complete"
)

type externalRecipientState string

const (
	externalRecipientPending   externalRecipientState = "pending"
	externalRecipientPublished externalRecipientState = "published"
)

type externalRecipient struct {
	Ship  string                 `json:"ship"`
	State externalRecipientState `json:"state"`
}

// externalRecordPath returns the path for an external record.
func externalRecordPath(b *Bus, source, externalID string) (string, error) {
	if !validExternalKey(source) || !validExternalKey(externalID) {
		return "", permanentPostExternalError("comms: invalid source/externalID for external record")
	}
	key := sha256.Sum256([]byte(source + "\x00" + externalID))
	return filepath.Join(b.BusDir, "external", hex.EncodeToString(key[:])+".json"), nil
}

// validExternalKey checks that source/externalID contain only safe path-safe
// characters (no slashes, no traversal).
func validExternalKey(s string) bool {
	if s == "" || s == "." || s == ".." {
		return false
	}
	for _, c := range s {
		switch c {
		case 'a', 'b', 'c', 'd', 'e', 'f', 'g', 'h', 'i', 'j', 'k', 'l', 'm',
			'n', 'o', 'p', 'q', 'r', 's', 't', 'u', 'v', 'w', 'x', 'y', 'z',
			'A', 'B', 'C', 'D', 'E', 'F', 'G', 'H', 'I', 'J', 'K', 'L', 'M',
			'N', 'O', 'P', 'Q', 'R', 'S', 'T', 'U', 'V', 'W', 'X', 'Y', 'Z',
			'0', '1', '2', '3', '4', '5', '6', '7', '8', '9',
			'-', '_', '.':
			continue
		default:
			return false
		}
	}
	return true
}

// loadExternalRecordLocked loads an external record. Returns nil if not found.
// Caller must hold the bus lock.
func loadExternalRecordLocked(b *Bus, source, externalID string) (*externalRecord, error) {
	path, err := externalRecordPath(b, source, externalID)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var rec externalRecord
	if err := json.Unmarshal(data, &rec); err != nil {
		return nil, permanentPostExternalError("comms: external record corrupt: %v", err)
	}
	if rec.Source != source || rec.ExternalID != externalID {
		return nil, permanentPostExternalError("comms: external record key mismatch")
	}
	return &rec, nil
}

// createExternalRecordLocked creates an external record with create-if-absent
// semantics (never overwrites an existing record). Caller must hold the bus lock.
func createExternalRecordLocked(b *Bus, rec *externalRecord) error {
	path, err := externalRecordPath(b, rec.Source, rec.ExternalID)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	tmp := path + ".tmp." + strconv.FormatInt(time.Now().UnixNano(), 10)
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := syncFile(tmp); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := syncDir(dir); err != nil {
		return err
	}
	return nil
}

// saveExternalRecordLocked saves an existing external record atomically.
// Caller must hold the bus lock.
func saveExternalRecordLocked(b *Bus, rec *externalRecord) error {
	path, err := externalRecordPath(b, rec.Source, rec.ExternalID)
	if err != nil {
		return err
	}
	dir := filepath.Dir(path)
	data, err := json.MarshalIndent(rec, "", "  ")
	if err != nil {
		return err
	}
	tmp := path + ".tmp." + strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := syncFile(tmp); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := syncDir(dir); err != nil {
		return err
	}
	return nil
}

// publishExternalRecipientsLocked publishes all pending recipients of an
// external record. It must be called with the bus lock held.
// The CommsID must already be reserved in rec.CommsID before calling.
func publishExternalRecipientsLocked(b *Bus, rec *externalRecord) error {
	var shipIDs []string
	if len(rec.Recipients) == 0 {
		// Resolve recipients now.
		if rec.Target == "all" {
			shipIDs = b.broadcastRecipients()
		} else {
			shipIDs = []string{rec.Target}
		}
		if len(shipIDs) == 0 {
			return fmt.Errorf("comms: PostExternal: no recipients for target %q", rec.Target)
		}
		// Deterministic, deduplicated snapshot.
		shipIDs = dedupSorted(shipIDs)
		rec.Recipients = make([]externalRecipient, len(shipIDs))
		for i, ship := range shipIDs {
			rec.Recipients[i] = externalRecipient{Ship: ship, State: externalRecipientPending}
		}
		if err := saveExternalRecordLocked(b, rec); err != nil {
			return err
		}
	}
	// Publish recipients (CommsID already reserved by caller).
	for _, r := range rec.Recipients {
		if r.State == externalRecipientPublished {
			continue
		}
		if err := publishExternalRecipientLocked(b, rec, r.Ship); err != nil {
			return err
		}
		for j := range rec.Recipients {
			if rec.Recipients[j].Ship == r.Ship {
				rec.Recipients[j].State = externalRecipientPublished
			}
		}
		if err := saveExternalRecordLocked(b, rec); err != nil {
			return err
		}
	}
	rec.State = externalComplete
	if err := saveExternalRecordLocked(b, rec); err != nil {
		return err
	}
	return nil
}

// publishExternalRecipientLocked publishes a single recipient copy of an
// external record, idempotently.
func publishExternalRecipientLocked(b *Bus, rec *externalRecord, ship string) error {
	msgType := rec.Type
	if msgType == "" {
		msgType = "ship"
	}
	msg := msgRecord{
		ID:             rec.CommsID,
		Epoch:          now(),
		ISO:            isots(),
		From:           rec.From,
		Target:         ship,
		Text:           rec.Payload,
		ReplyTo:        rec.ReplyTo,
		Type:           msgType,
		ExternalSource: rec.Source,
		ExternalID:     rec.ExternalID,
		ExternalHash:   rec.PayloadSHA,
	}
	data, err := json.Marshal(msg)
	if err != nil {
		return err
	}
	path, err := b.mfile(msg.ID, ship)
	if err != nil {
		return err
	}
	// Check if file already exists with matching content (idempotent).
	if existing, err := os.ReadFile(path); err == nil {
		var existingRec msgRecord
		if json.Unmarshal(existing, &existingRec) == nil &&
			existingRec.ID == rec.CommsID &&
			existingRec.ExternalSource == rec.Source &&
			existingRec.ExternalID == rec.ExternalID &&
			existingRec.ExternalHash == rec.PayloadSHA &&
			existingRec.Text == rec.Payload {
			return nil // already published
		}
		return permanentPostExternalError("comms: PostExternal: existing recipient copy mismatch for %s/%s", ship, rec.CommsID)
	}
	// Atomic publish: temp file, fsync, rename, dir sync.
	tmp := path + ".tmp." + strconv.FormatInt(time.Now().UnixNano(), 10)
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := syncFile(tmp); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := os.Rename(tmp, path); err != nil {
		os.Remove(tmp)
		return err
	}
	if err := syncDir(filepath.Dir(path)); err != nil {
		return err
	}
	return nil
}

// syncFile fsyncs a file after closing.
func syncFile(path string) error {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// syncDir fsyncs the parent directory.
func syncDir(path string) error {
	f, err := os.OpenFile(path, os.O_RDONLY, 0)
	if err != nil {
		return err
	}
	defer f.Close()
	return f.Sync()
}

// dedupSorted returns a sorted, deduplicated copy of recipients.
func dedupSorted(recipients []string) []string {
	sort.Strings(recipients)
	out := recipients[:0]
	for _, r := range recipients {
		if len(out) == 0 || out[len(out)-1] != r {
			out = append(out, r)
		}
	}
	return out
}

// PostExternal publishes one externally identified message exactly once.
// Repeated calls with the same source/externalID and identical text resume
// incomplete publication and return the same Comms ID. A changed payload
// for an existing key is an integrity error.
//
// The ID reservation (nextID) happens BEFORE the external record is created,
// so no persisted record can exist without a CommsID. This eliminates the
// crash window where a record would be stuck without a durable ID.
//
// source identifies the wormhole (e.g. fleet-comms), externalID is the
// wormhole front-matter ID, from/target are canonical ship IDs, msgType is
// the message type (ship/user/control), replyTo is the optional reply target,
// and text is the full message content including front matter (never truncated).
func (b *Bus) PostExternal(source, externalID, from, target, msgType, replyTo, text string) (string, error) {
	if source == "" || externalID == "" || from == "" || target == "" {
		return "", permanentPostExternalError("comms: PostExternal: source, externalID, from, target all required")
	}
	if len(text) == 0 {
		return "", permanentPostExternalError("comms: PostExternal: empty text")
	}

	lock, err := b.lockBus()
	if err != nil {
		return "", err
	}
	defer lock.Close()

	rec, err := loadExternalRecordLocked(b, source, externalID)
	if err != nil {
		return "", err
	}
	if rec != nil {
		// Existing record — verify hash integrity.
		hash := sha256.Sum256([]byte(text))
		if hex.EncodeToString(hash[:]) != rec.PayloadSHA {
			return "", permanentPostExternalError("comms: PostExternal: hash mismatch for %s/%s: existing payload differs", source, externalID)
		}
		// Record exists but may lack CommsID (crash after record creation,
		// before ID reservation). Reserve ID now.
		if rec.CommsID == "" {
			id, err := b.nextID()
			if err != nil {
				return "", err
			}
			rec.CommsID = id
			rec.State = externalPosting
			if err := saveExternalRecordLocked(b, rec); err != nil {
				return "", err
			}
		}
		// Same source+externalID+payload: resume incomplete publication,
		// return the same CommsID.
		if rec.State == externalComplete {
			return rec.CommsID, nil
		}
		// Resume fan-out for pending recipients.
		if err := publishExternalRecipientsLocked(b, rec); err != nil {
			return "", err
		}
		return rec.CommsID, nil
	}

	// New record — reserve CommsID FIRST, then create record with it.
	// This ensures no persisted record exists without a CommsID.
	id, err := b.nextID()
	if err != nil {
		return "", err
	}

	hash := sha256.Sum256([]byte(text))
	rec = &externalRecord{
		Version:    1,
		Source:     source,
		ExternalID: externalID,
		PayloadSHA: hex.EncodeToString(hash[:]),
		Payload:    text,
		From:       from,
		Target:     target,
		Type:       msgType,
		ReplyTo:    replyTo,
		CommsID:    id,
		State:      externalPosting,
		CreatedAt:  isots(),
	}
	if err := createExternalRecordLocked(b, rec); err != nil {
		return "", err
	}
	// Publish recipients.
	if err := publishExternalRecipientsLocked(b, rec); err != nil {
		return "", err
	}
	// Mark complete.
	rec.State = externalComplete
	if err := saveExternalRecordLocked(b, rec); err != nil {
		return "", err
	}
	return rec.CommsID, nil
}
