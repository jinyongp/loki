package lifecycle

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"
	"unicode/utf8"

	"golang.org/x/sys/unix"

	"loki/internal/platform/safeio"
)

const (
	operationJournalVersion = 1
	maxOperationRecords     = 1024
	maxOperationRecordBytes = 128 << 10
	maxOperationErrorBytes  = 4096
)

type OperationKind string

const (
	OperationApply            OperationKind = "apply"
	OperationBackup           OperationKind = "backup"
	OperationInstall          OperationKind = "install"
	OperationRollback         OperationKind = "rollback"
	OperationRestore          OperationKind = "restore"
	OperationUninstall        OperationKind = "uninstall"
	OperationEnableComponent  OperationKind = "enable_component"
	OperationDisableComponent OperationKind = "disable_component"
)

func (k OperationKind) Valid() bool {
	switch k {
	case OperationApply, OperationBackup, OperationInstall, OperationRollback, OperationRestore, OperationUninstall,
		OperationEnableComponent, OperationDisableComponent:
		return true
	default:
		return false
	}
}

type OperationState string

const (
	OperationApplying       OperationState = "applying"
	OperationRecovering     OperationState = "recovering"
	OperationSucceeded      OperationState = "succeeded"
	OperationRolledBack     OperationState = "rolled_back"
	OperationRecoveryFailed OperationState = "recovery_failed"
)

func (s OperationState) Terminal() bool {
	return s == OperationSucceeded || s == OperationRolledBack || s == OperationRecoveryFailed
}

type OperationPhase string

const (
	PhaseAdmitted OperationPhase = "admitted"
	PhaseSnapshot OperationPhase = "snapshot"
	PhaseSwitch   OperationPhase = "switch"
	PhaseMigrate  OperationPhase = "migrate"
	PhaseRestart  OperationPhase = "restart"
	PhaseHealth   OperationPhase = "health"
	PhaseRollback OperationPhase = "rollback"
	PhaseComplete OperationPhase = "complete"
)

func nextApplyPhase(current OperationPhase) OperationPhase {
	switch current {
	case PhaseAdmitted:
		return PhaseSnapshot
	case PhaseSnapshot:
		return PhaseSwitch
	case PhaseSwitch:
		return PhaseMigrate
	case PhaseMigrate:
		return PhaseRestart
	case PhaseRestart:
		return PhaseHealth
	default:
		return ""
	}
}

type OperationRecord struct {
	ID                    string         `json:"id"`
	Kind                  OperationKind  `json:"kind"`
	PlanID                string         `json:"plan_id"`
	ObservedHostRevision  string         `json:"observed_host_revision"`
	ActiveGenerationID    string         `json:"active_generation_id,omitempty"`
	CandidateGenerationID string         `json:"candidate_generation_id"`
	RecoveryBackupID      string         `json:"recovery_backup_id,omitempty"`
	State                 OperationState `json:"state"`
	Phase                 OperationPhase `json:"phase"`
	OriginalError         string         `json:"original_error,omitempty"`
	RecoveryError         string         `json:"recovery_error,omitempty"`
	StartedAt             string         `json:"started_at"`
	UpdatedAt             string         `json:"updated_at"`
	CompletedAt           string         `json:"completed_at,omitempty"`
}

func (r OperationRecord) Valid() bool {
	if len(r.ID) != 32 {
		return false
	}
	if _, err := hex.DecodeString(r.ID); err != nil || !r.Kind.Valid() ||
		!digestPattern.MatchString(r.PlanID) || !digestPattern.MatchString(r.CandidateGenerationID) ||
		r.ObservedHostRevision == "" || len(r.ObservedHostRevision) > 128 ||
		strings.ContainsAny(r.ObservedHostRevision, "\r\n\x00") {
		return false
	}
	if r.ActiveGenerationID != "" && !digestPattern.MatchString(r.ActiveGenerationID) {
		return false
	}
	if r.RecoveryBackupID != "" && !digestPattern.MatchString(r.RecoveryBackupID) {
		return false
	}
	if !validOperationError(r.OriginalError) || !validOperationError(r.RecoveryError) {
		return false
	}
	started, startErr := time.Parse(time.RFC3339Nano, r.StartedAt)
	updated, updateErr := time.Parse(time.RFC3339Nano, r.UpdatedAt)
	if startErr != nil || updateErr != nil || started.Location() != time.UTC || updated.Location() != time.UTC ||
		updated.Before(started) {
		return false
	}
	switch r.State {
	case OperationApplying:
		if !validApplyPhase(r.Phase) || r.OriginalError != "" || r.RecoveryError != "" || r.CompletedAt != "" {
			return false
		}
		if r.Phase == PhaseAdmitted {
			return r.RecoveryBackupID == ""
		}
		return r.RecoveryBackupID != ""
	case OperationRecovering:
		return r.Phase == PhaseRollback && r.OriginalError != "" && r.RecoveryError == "" && r.CompletedAt == ""
	case OperationSucceeded:
		return r.Phase == PhaseComplete && r.RecoveryBackupID != "" && r.OriginalError == "" && r.RecoveryError == "" &&
			validOperationCompletion(r.CompletedAt, updated)
	case OperationRolledBack:
		return r.Phase == PhaseComplete && r.OriginalError != "" && r.RecoveryError == "" &&
			validOperationCompletion(r.CompletedAt, updated)
	case OperationRecoveryFailed:
		return r.Phase == PhaseComplete && r.OriginalError != "" && r.RecoveryError != "" &&
			validOperationCompletion(r.CompletedAt, updated)
	default:
		return false
	}
}

func validApplyPhase(phase OperationPhase) bool {
	switch phase {
	case PhaseAdmitted, PhaseSnapshot, PhaseSwitch, PhaseMigrate, PhaseRestart, PhaseHealth:
		return true
	default:
		return false
	}
}

func validOperationError(value string) bool {
	return len(value) <= maxOperationErrorBytes && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func validOperationCompletion(raw string, updated time.Time) bool {
	completed, err := time.Parse(time.RFC3339Nano, raw)
	return err == nil && completed.Location() == time.UTC && !completed.Before(updated)
}

type OperationLock struct {
	root   string
	file   *os.File
	closed bool
}

func AcquireOperationLock(root string) (*OperationLock, error) {
	if _, err := OpenFileStore(root); err != nil {
		return nil, err
	}
	path := filepath.Join(root, "operation.lock")
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "lifecycle-operation.lock")
	info, statErr := file.Stat()
	if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		_ = file.Close()
		return nil, errors.New("host lifecycle operation lock must be a private regular file")
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		_ = file.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, errors.New("another host lifecycle operation owns the lock")
		}
		return nil, err
	}
	return &OperationLock{root: root, file: file}, nil
}

func (l *OperationLock) Close() error {
	if l == nil || l.closed {
		return nil
	}
	l.closed = true
	if l.file == nil {
		return nil
	}
	err := unix.Flock(int(l.file.Fd()), unix.LOCK_UN)
	err = errors.Join(err, l.file.Close())
	l.file = nil
	return err
}

func (l *OperationLock) validFor(root string) bool {
	return l != nil && !l.closed && l.file != nil && l.root == root
}

type OperationJournalOptions struct {
	Failpoint func(string) error
}

type RecoveryHandler func(context.Context, OperationRecord) error

type RecoveryFailedError struct {
	Original string
	Recovery string
}

func (e *RecoveryFailedError) Error() string {
	if e == nil {
		return "host lifecycle recovery failed"
	}
	return fmt.Sprintf("host lifecycle recovery failed: original=%s; recovery=%s", e.Original, e.Recovery)
}

type operationEnvelope struct {
	Version int             `json:"version"`
	Record  json.RawMessage `json:"record"`
	SHA256  string          `json:"sha256"`
}

type OperationJournal struct {
	root      string
	dir       string
	lock      *OperationLock
	failpoint func(string) error
	records   map[string]OperationRecord
	poisoned  bool
}

func OpenOperationJournal(root string, lock *OperationLock, options OperationJournalOptions) (*OperationJournal, error) {
	if !lock.validFor(root) {
		return nil, errors.New("host lifecycle operation journal requires the active lifecycle lock")
	}
	dir := filepath.Join(root, "operations")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return nil, err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("host lifecycle operation journal directory must be private and real")
	}
	journal := &OperationJournal{
		root: root, dir: dir, lock: lock, failpoint: options.Failpoint, records: map[string]OperationRecord{},
	}
	if err = journal.load(); err != nil {
		return nil, err
	}
	return journal, nil
}

func (j *OperationJournal) Active() (OperationRecord, bool, error) {
	if err := j.ready(); err != nil {
		return OperationRecord{}, false, err
	}
	var active OperationRecord
	found := false
	for _, record := range j.records {
		if record.State.Terminal() {
			continue
		}
		if found {
			return OperationRecord{}, false, errors.New("host lifecycle journal contains multiple active operations")
		}
		active = record
		found = true
	}
	return active, found, nil
}

func (j *OperationJournal) List() ([]OperationRecord, error) {
	if err := j.ready(); err != nil {
		return nil, err
	}
	result := make([]OperationRecord, 0, len(j.records))
	for _, record := range j.records {
		result = append(result, record)
	}
	sort.Slice(result, func(i, k int) bool {
		if result[i].StartedAt == result[k].StartedAt {
			return result[i].ID < result[k].ID
		}
		return result[i].StartedAt < result[k].StartedAt
	})
	return result, nil
}

func (j *OperationJournal) Begin(kind OperationKind, plan PreparedPlan, now time.Time) (OperationRecord, error) {
	if err := j.ready(); err != nil {
		return OperationRecord{}, err
	}
	if !kind.Valid() || !plan.Valid() {
		return OperationRecord{}, errors.New("host lifecycle operation intent is invalid")
	}
	if _, found, err := j.Active(); err != nil {
		return OperationRecord{}, err
	} else if found {
		return OperationRecord{}, errors.New("another host lifecycle operation is still active")
	}
	id, err := newOperationID()
	if err != nil {
		return OperationRecord{}, err
	}
	now = now.UTC()
	record := OperationRecord{
		ID: id, Kind: kind, PlanID: plan.ID, ObservedHostRevision: plan.ObservedHostRevision,
		ActiveGenerationID: plan.ActiveGenerationID, CandidateGenerationID: plan.CandidateGenerationID,
		State: OperationApplying, Phase: PhaseAdmitted,
		StartedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano),
	}
	if err = j.persist(record, false); err != nil {
		return OperationRecord{}, err
	}
	j.records[id] = record
	return record, nil
}

func (j *OperationJournal) Advance(id string, phase OperationPhase, now time.Time) (OperationRecord, error) {
	record, err := j.require(id)
	if err != nil {
		return OperationRecord{}, err
	}
	if phase == PhaseSnapshot {
		return OperationRecord{}, errors.New("host lifecycle snapshot transition requires a recovery backup checkpoint")
	}
	if record.State != OperationApplying || nextApplyPhase(record.Phase) != phase {
		return OperationRecord{}, errors.New("host lifecycle operation phase transition is invalid")
	}
	updatedAt, err := nextOperationTime(record, now)
	if err != nil {
		return OperationRecord{}, err
	}
	record.Phase = phase
	record.UpdatedAt = updatedAt
	if !record.Valid() {
		return OperationRecord{}, errors.New("host lifecycle operation transition produced an invalid record")
	}
	if err = j.persist(record, true); err != nil {
		return OperationRecord{}, err
	}
	j.records[id] = record
	return record, nil
}

func (j *OperationJournal) RecordSnapshot(id, backupID string, now time.Time) (OperationRecord, error) {
	record, err := j.require(id)
	if err != nil {
		return OperationRecord{}, err
	}
	if record.State != OperationApplying || record.Phase != PhaseAdmitted || !digestPattern.MatchString(backupID) {
		return OperationRecord{}, errors.New("host lifecycle snapshot checkpoint is invalid")
	}
	updatedAt, err := nextOperationTime(record, now)
	if err != nil {
		return OperationRecord{}, err
	}
	record.Phase = PhaseSnapshot
	record.RecoveryBackupID = backupID
	record.UpdatedAt = updatedAt
	if !record.Valid() {
		return OperationRecord{}, errors.New("host lifecycle snapshot checkpoint produced an invalid record")
	}
	if err = j.persist(record, true); err != nil {
		return OperationRecord{}, err
	}
	j.records[id] = record
	return record, nil
}

func (j *OperationJournal) MarkSucceeded(id string, now time.Time) (OperationRecord, error) {
	record, err := j.require(id)
	if err != nil {
		return OperationRecord{}, err
	}
	ready := record.Phase == PhaseHealth || record.Kind == OperationBackup && record.Phase == PhaseSnapshot
	if record.State != OperationApplying || !ready {
		return OperationRecord{}, errors.New("host lifecycle operation cannot succeed before its terminal validation")
	}
	updatedAt, err := nextOperationTime(record, now)
	if err != nil {
		return OperationRecord{}, err
	}
	record.State = OperationSucceeded
	record.Phase = PhaseComplete
	record.UpdatedAt = updatedAt
	record.CompletedAt = record.UpdatedAt
	if err = j.persist(record, true); err != nil {
		return OperationRecord{}, err
	}
	j.records[id] = record
	return record, nil
}

func (j *OperationJournal) BeginRecovery(id string, original error, now time.Time) (OperationRecord, error) {
	record, err := j.require(id)
	if err != nil {
		return OperationRecord{}, err
	}
	if record.State != OperationApplying || original == nil {
		return OperationRecord{}, errors.New("host lifecycle recovery requires an applying operation and original error")
	}
	message := strings.TrimSpace(original.Error())
	if message == "" || !validOperationError(message) {
		return OperationRecord{}, errors.New("host lifecycle original error is invalid")
	}
	updatedAt, err := nextOperationTime(record, now)
	if err != nil {
		return OperationRecord{}, err
	}
	record.State = OperationRecovering
	record.Phase = PhaseRollback
	record.OriginalError = message
	record.UpdatedAt = updatedAt
	if err = j.persist(record, true); err != nil {
		return OperationRecord{}, err
	}
	j.records[id] = record
	return record, nil
}

func (j *OperationJournal) MarkRolledBack(id string, now time.Time) (OperationRecord, error) {
	record, err := j.require(id)
	if err != nil {
		return OperationRecord{}, err
	}
	if record.State != OperationRecovering || record.OriginalError == "" {
		return OperationRecord{}, errors.New("host lifecycle operation is not recovering")
	}
	updatedAt, err := nextOperationTime(record, now)
	if err != nil {
		return OperationRecord{}, err
	}
	record.State = OperationRolledBack
	record.Phase = PhaseComplete
	record.UpdatedAt = updatedAt
	record.CompletedAt = record.UpdatedAt
	if err = j.persist(record, true); err != nil {
		return OperationRecord{}, err
	}
	j.records[id] = record
	return record, nil
}

func (j *OperationJournal) MarkRecoveryFailed(id string, recovery error, now time.Time) (OperationRecord, error) {
	record, err := j.require(id)
	if err != nil {
		return OperationRecord{}, err
	}
	if record.State != OperationRecovering || record.OriginalError == "" || recovery == nil {
		return OperationRecord{}, errors.New("host lifecycle recovery failure requires a recovering operation and recovery error")
	}
	message := strings.TrimSpace(recovery.Error())
	if message == "" || !validOperationError(message) {
		return OperationRecord{}, errors.New("host lifecycle recovery error is invalid")
	}
	updatedAt, err := nextOperationTime(record, now)
	if err != nil {
		return OperationRecord{}, err
	}
	record.State = OperationRecoveryFailed
	record.Phase = PhaseComplete
	record.RecoveryError = message
	record.UpdatedAt = updatedAt
	record.CompletedAt = record.UpdatedAt
	if err = j.persist(record, true); err != nil {
		return OperationRecord{}, err
	}
	j.records[id] = record
	return record, nil
}

func (j *OperationJournal) RecoverInterrupted(
	ctx context.Context,
	recover RecoveryHandler,
	now time.Time,
) (OperationRecord, bool, error) {
	if err := j.ready(); err != nil {
		return OperationRecord{}, false, err
	}
	if err := ctx.Err(); err != nil {
		return OperationRecord{}, false, err
	}
	record, found, err := j.Active()
	if err != nil || !found {
		return record, found, err
	}
	if recover == nil {
		return OperationRecord{}, true, errors.New("host lifecycle recovery handler is not configured")
	}
	if record.State == OperationApplying {
		original := fmt.Errorf("host lifecycle operation interrupted during %s", record.Phase)
		record, err = j.BeginRecovery(record.ID, original, now)
		if err != nil {
			return OperationRecord{}, true, err
		}
	} else if record.State != OperationRecovering {
		return OperationRecord{}, true, errors.New("host lifecycle active operation state cannot be recovered")
	}
	if err = ctx.Err(); err != nil {
		return OperationRecord{}, true, err
	}
	if recoveryErr := recover(ctx, record); recoveryErr != nil {
		failed, persistErr := j.MarkRecoveryFailed(record.ID, recoveryErr, now.Add(time.Nanosecond))
		if persistErr != nil {
			return OperationRecord{}, true, errors.Join(recoveryErr, persistErr)
		}
		return failed, true, &RecoveryFailedError{
			Original: failed.OriginalError,
			Recovery: failed.RecoveryError,
		}
	}
	record, err = j.MarkRolledBack(record.ID, now.Add(time.Nanosecond))
	return record, true, err
}

func (j *OperationJournal) require(id string) (OperationRecord, error) {
	if err := j.ready(); err != nil {
		return OperationRecord{}, err
	}
	record, ok := j.records[id]
	if !ok {
		return OperationRecord{}, errors.New("host lifecycle operation does not exist")
	}
	return record, nil
}

func (j *OperationJournal) ready() error {
	if j == nil || j.poisoned || !j.lock.validFor(j.root) {
		if j != nil && j.poisoned {
			return errors.New("host lifecycle operation journal requires reopen after failpoint")
		}
		return errors.New("host lifecycle operation journal is not active")
	}
	return nil
}

func (j *OperationJournal) persist(record OperationRecord, overwrite bool) error {
	if !record.Valid() {
		return errors.New("host lifecycle operation record is invalid")
	}
	rawRecord, err := json.Marshal(record)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(rawRecord)
	envelope, err := json.Marshal(operationEnvelope{
		Version: operationJournalVersion, Record: rawRecord, SHA256: hex.EncodeToString(sum[:]),
	})
	if err != nil {
		return err
	}
	envelope = append(envelope, '\n')
	if len(envelope) > maxOperationRecordBytes {
		return errors.New("host lifecycle operation record exceeds size limit")
	}
	if err = j.runFailpoint("before_publish:" + string(record.State) + ":" + string(record.Phase)); err != nil {
		return err
	}
	if err = safeio.PublishPrivate(j.recordPath(record.ID), envelope, overwrite); err != nil {
		return err
	}
	if err = j.runFailpoint("after_publish:" + string(record.State) + ":" + string(record.Phase)); err != nil {
		return err
	}
	return nil
}

func (j *OperationJournal) runFailpoint(name string) error {
	if j.failpoint == nil {
		return nil
	}
	if err := j.failpoint(name); err != nil {
		j.poisoned = true
		return err
	}
	return nil
}

func nextOperationTime(record OperationRecord, now time.Time) (string, error) {
	previous, err := time.Parse(time.RFC3339Nano, record.UpdatedAt)
	if err != nil {
		return "", errors.New("host lifecycle operation has an invalid update timestamp")
	}
	now = now.UTC()
	if now.Before(previous) {
		now = previous
	}
	return now.Format(time.RFC3339Nano), nil
}

func (j *OperationJournal) load() error {
	entries, err := os.ReadDir(j.dir)
	if err != nil {
		return err
	}
	for _, entry := range entries {
		name := entry.Name()
		if strings.HasPrefix(name, ".loki-private-") {
			info, infoErr := entry.Info()
			if infoErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
				return errors.New("host lifecycle journal contains an unsafe interrupted publication")
			}
			if err = os.Remove(filepath.Join(j.dir, name)); err != nil {
				return err
			}
			continue
		}
		if entry.IsDir() || !strings.HasSuffix(name, ".json") {
			return fmt.Errorf("host lifecycle journal contains unexpected entry %q", name)
		}
		if len(j.records) >= maxOperationRecords {
			return errors.New("host lifecycle operation journal exceeds record capacity")
		}
		id := strings.TrimSuffix(name, ".json")
		if len(id) != 32 {
			return errors.New("host lifecycle operation record name is invalid")
		}
		if _, decodeErr := hex.DecodeString(id); decodeErr != nil {
			return errors.New("host lifecycle operation record name is invalid")
		}
		record, readErr := loadOperationRecord(filepath.Join(j.dir, name))
		if readErr != nil {
			return fmt.Errorf("load host lifecycle operation %q: %w", id, readErr)
		}
		if record.ID != id {
			return errors.New("host lifecycle operation identity does not match its file")
		}
		j.records[id] = record
	}
	_, _, err = j.Active()
	return err
}

func (j *OperationJournal) recordPath(id string) string {
	return filepath.Join(j.dir, id+".json")
}

func loadOperationRecord(path string) (OperationRecord, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return OperationRecord{}, err
	}
	file := os.NewFile(uintptr(fd), filepath.Base(path))
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return OperationRecord{}, errors.New("host lifecycle operation record must be a private regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, maxOperationRecordBytes+1))
	if err != nil {
		return OperationRecord{}, err
	}
	if len(raw) > maxOperationRecordBytes {
		return OperationRecord{}, errors.New("host lifecycle operation record exceeds size limit")
	}
	var envelope operationEnvelope
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&envelope); err != nil || envelope.Version != operationJournalVersion {
		return OperationRecord{}, errors.New("host lifecycle operation journal version is unsupported or invalid")
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return OperationRecord{}, errors.New("host lifecycle operation journal contains trailing data")
	}
	sum := sha256.Sum256(envelope.Record)
	if envelope.SHA256 != hex.EncodeToString(sum[:]) {
		return OperationRecord{}, errors.New("host lifecycle operation record integrity check failed")
	}
	var record OperationRecord
	recordDecoder := json.NewDecoder(bytes.NewReader(envelope.Record))
	recordDecoder.DisallowUnknownFields()
	if err = recordDecoder.Decode(&record); err != nil {
		return OperationRecord{}, errors.New("host lifecycle operation record is invalid")
	}
	if err = recordDecoder.Decode(&trailing); !errors.Is(err, io.EOF) || !record.Valid() {
		return OperationRecord{}, errors.New("host lifecycle operation record is invalid")
	}
	return record, nil
}

func newOperationID() (string, error) {
	random := make([]byte, 16)
	if _, err := rand.Read(random); err != nil {
		return "", err
	}
	return hex.EncodeToString(random), nil
}
