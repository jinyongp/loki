package agentcontext

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"time"

	"golang.org/x/sys/unix"
	"loki/internal/platform/safeio"
)

const (
	contextRecordVersion          = 1
	retentionStateVersion         = 1
	defaultContextRecordBytes     = 256 << 10
	defaultContextRecordsPerScope = 64
	defaultContextScopeBytes      = 8 << 20
	defaultContextRecords         = 1024
	defaultContextBytes           = 64 << 20
	maxContextNarrativeBytes      = 16 << 10
	maxContextNarrativeItems      = 100
	maxContextSessionRefBytes     = 64
)

var contextSessionRefPattern = regexp.MustCompile(`^[0-9a-f]{16,64}$`)

type ContextJournalLimits struct {
	MaxRecordBytes     int64
	MaxRecordsPerScope int
	MaxScopeBytes      int64
	MaxRecords         int
	MaxBytes           int64
}

func (l ContextJournalLimits) normalized() ContextJournalLimits {
	if l.MaxRecordBytes <= 0 {
		l.MaxRecordBytes = defaultContextRecordBytes
	}
	if l.MaxRecordsPerScope <= 0 {
		l.MaxRecordsPerScope = defaultContextRecordsPerScope
	}
	if l.MaxScopeBytes <= 0 {
		l.MaxScopeBytes = defaultContextScopeBytes
	}
	if l.MaxRecords <= 0 {
		l.MaxRecords = defaultContextRecords
	}
	if l.MaxBytes <= 0 {
		l.MaxBytes = defaultContextBytes
	}
	return l
}

func (l ContextJournalLimits) validate() error {
	l = l.normalized()
	if l.MaxRecordBytes < 1024 || l.MaxRecordBytes > 2<<20 ||
		l.MaxRecordsPerScope < 1 || l.MaxRecordsPerScope > 1024 ||
		l.MaxScopeBytes < l.MaxRecordBytes || l.MaxScopeBytes > 128<<20 ||
		l.MaxRecords < 1 || l.MaxRecords > 16384 ||
		l.MaxBytes < l.MaxRecordBytes || l.MaxBytes > 512<<20 {
		return errors.New("invalid context journal limits")
	}
	return nil
}

type ContextDraft struct {
	Basis      ContextBasis `json:"basis"`
	Summary    string       `json:"summary"`
	Decisions  []string     `json:"decisions"`
	Remaining  []string     `json:"remaining"`
	Blockers   []string     `json:"blockers"`
	NextAction string       `json:"next_action"`
	SessionRef string       `json:"session_ref,omitempty"`
}

type ContextRecord struct {
	Version          int          `json:"version"`
	ID               string       `json:"id"`
	RequestID        string       `json:"request_id"`
	CreatedAt        string       `json:"created_at"`
	ScopeKey         string       `json:"scope_key"`
	Basis            ContextBasis `json:"basis"`
	BasisFingerprint string       `json:"basis_fingerprint"`
	DraftFingerprint string       `json:"draft_fingerprint"`
	Summary          string       `json:"summary"`
	Decisions        []string     `json:"decisions"`
	Remaining        []string     `json:"remaining"`
	Blockers         []string     `json:"blockers"`
	NextAction       string       `json:"next_action"`
	SessionRef       string       `json:"session_ref,omitempty"`
}

type contextRecordEnvelope struct {
	Version  int           `json:"version"`
	Record   ContextRecord `json:"record"`
	Checksum string        `json:"checksum"`
}

type ContextScope struct {
	RepositoryID string `json:"repository_id"`
	WorktreeID   string `json:"worktree_id"`
	WorkstreamID string `json:"workstream_id,omitempty"`
}

type ContextRetentionGap struct {
	PrunedRecords      int    `json:"pruned_records"`
	LastPrunedAt       string `json:"last_pruned_at,omitempty"`
	LastPrunedRecordID string `json:"last_pruned_record_id,omitempty"`
}

type contextPendingPrune struct {
	ScopeKey string `json:"scope_key"`
	RecordID string `json:"record_id"`
}

type contextRetentionState struct {
	Version int                            `json:"version"`
	Scopes  map[string]ContextRetentionGap `json:"scopes"`
	Pending []contextPendingPrune          `json:"pending"`
}

type contextRetentionEnvelope struct {
	Version  int                   `json:"version"`
	State    contextRetentionState `json:"state"`
	Checksum string                `json:"checksum"`
}

type ContextPutResult struct {
	Record        ContextRecord        `json:"record"`
	Replayed      bool                 `json:"replayed"`
	PrunedRecords int                  `json:"pruned_records"`
	RetentionGap  *ContextRetentionGap `json:"retention_gap,omitempty"`
}

type ContextLatestResult struct {
	Found        bool                 `json:"found"`
	Record       *ContextRecord       `json:"record,omitempty"`
	RetentionGap *ContextRetentionGap `json:"retention_gap,omitempty"`
}

type ContextListResult struct {
	Records      []ContextRecord      `json:"records"`
	RetentionGap *ContextRetentionGap `json:"retention_gap,omitempty"`
	Complete     bool                 `json:"complete"`
}

type ContextJournal struct {
	Dir    string
	Limits ContextJournalLimits
	now    func() time.Time
}

type storedContextRecord struct {
	record    ContextRecord
	bytes     int64
	createdAt time.Time
}

func NewContextJournal(dir string, limits ContextJournalLimits) (*ContextJournal, error) {
	if !filepath.IsAbs(dir) {
		return nil, errors.New("context journal directory must be absolute")
	}
	if err := limits.validate(); err != nil {
		return nil, err
	}
	for _, path := range []string{dir, filepath.Join(dir, "records")} {
		if err := os.MkdirAll(path, 0700); err != nil {
			return nil, err
		}
		info, err := os.Lstat(path)
		if err != nil {
			return nil, err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
			return nil, errors.New("context journal directories must be private directories")
		}
	}
	return &ContextJournal{Dir: dir, Limits: limits.normalized(), now: time.Now}, nil
}

func (j *ContextJournal) lock(ctx context.Context) (func(), error) {
	path := filepath.Join(j.Dir, "journal.lock")
	fd, err := unix.Open(path, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "context-journal.lock")
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		file.Close()
		return nil, errors.New("context journal lock must be a private regular file")
	}
	for {
		if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err == nil {
			return func() {
				_ = unix.Flock(fd, unix.LOCK_UN)
				_ = file.Close()
			}, nil
		}
		if !errors.Is(err, unix.EWOULDBLOCK) {
			file.Close()
			return nil, err
		}
		select {
		case <-ctx.Done():
			file.Close()
			return nil, ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func normalizeNarrative(items []string, field string) ([]string, error) {
	if len(items) > maxContextNarrativeItems {
		return nil, errors.New("too many context " + field)
	}
	out := append([]string{}, items...)
	for _, value := range out {
		if strings.TrimSpace(value) == "" {
			return nil, errors.New("empty context " + field)
		}
		if err := validateContextText(value, maxContextNarrativeBytes, field); err != nil {
			return nil, err
		}
	}
	return out, nil
}

func normalizeContextDraft(d ContextDraft) (ContextDraft, string, error) {
	basis, err := normalizeContextBasis(d.Basis)
	if err != nil {
		return ContextDraft{}, "", err
	}
	if strings.TrimSpace(d.Summary) == "" || strings.TrimSpace(d.NextAction) == "" {
		return ContextDraft{}, "", errors.New("context summary and next action are required")
	}
	if err := validateContextText(d.Summary, maxContextNarrativeBytes, "summary"); err != nil {
		return ContextDraft{}, "", err
	}
	if err := validateContextText(d.NextAction, maxContextNarrativeBytes, "next action"); err != nil {
		return ContextDraft{}, "", err
	}
	d.Decisions, err = normalizeNarrative(d.Decisions, "decisions")
	if err != nil {
		return ContextDraft{}, "", err
	}
	d.Remaining, err = normalizeNarrative(d.Remaining, "remaining items")
	if err != nil {
		return ContextDraft{}, "", err
	}
	d.Blockers, err = normalizeNarrative(d.Blockers, "blockers")
	if err != nil {
		return ContextDraft{}, "", err
	}
	if d.SessionRef != "" && (!contextSessionRefPattern.MatchString(d.SessionRef) || len(d.SessionRef) > maxContextSessionRefBytes) {
		return ContextDraft{}, "", errors.New("invalid context session reference")
	}
	d.Basis = basis
	encoded, err := json.Marshal(d)
	if err != nil {
		return ContextDraft{}, "", err
	}
	sum := sha256.Sum256(encoded)
	return d, hex.EncodeToString(sum[:]), nil
}

func contextRecordID(requestID string) (string, error) {
	if !contextUUIDPattern.MatchString(requestID) {
		return "", errors.New("context request id must be a UUID")
	}
	sum := sha256.Sum256([]byte("loki-context-record-v1\x00" + strings.ToLower(requestID)))
	return hex.EncodeToString(sum[:]), nil
}

func encodeRecord(record ContextRecord, maximum int64) ([]byte, error) {
	raw, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	sum := sha256.Sum256(raw)
	envelope, err := json.Marshal(contextRecordEnvelope{
		Version: contextRecordVersion, Record: record, Checksum: hex.EncodeToString(sum[:]),
	})
	if err != nil {
		return nil, err
	}
	envelope = append(envelope, '\n')
	if int64(len(envelope)) > maximum {
		return nil, errors.New("context record exceeds size limit")
	}
	return envelope, nil
}

func readPrivateContextFile(path string, maximum int64) ([]byte, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), filepath.Base(path))
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("context journal file must be a private regular file")
	}
	data, err := io.ReadAll(io.LimitReader(file, maximum+1))
	if err != nil {
		return nil, err
	}
	if int64(len(data)) > maximum {
		return nil, errors.New("context journal file exceeds size limit")
	}
	return data, nil
}

func decodeRecord(path string, maximum int64) (storedContextRecord, error) {
	data, err := readPrivateContextFile(path, maximum)
	if err != nil {
		return storedContextRecord{}, err
	}
	var envelope contextRecordEnvelope
	if json.Unmarshal(data, &envelope) != nil || envelope.Version != contextRecordVersion ||
		envelope.Record.Version != contextRecordVersion || !contextDigestPattern.MatchString(envelope.Checksum) {
		return storedContextRecord{}, errors.New("context journal record is invalid")
	}
	raw, err := json.Marshal(envelope.Record)
	if err != nil {
		return storedContextRecord{}, err
	}
	sum := sha256.Sum256(raw)
	if envelope.Checksum != hex.EncodeToString(sum[:]) ||
		envelope.Record.ID != strings.TrimSuffix(filepath.Base(path), ".json") ||
		!contextDigestPattern.MatchString(envelope.Record.ID) {
		return storedContextRecord{}, errors.New("context journal record integrity check failed")
	}
	basis, err := normalizeContextBasis(envelope.Record.Basis)
	if err != nil {
		return storedContextRecord{}, errors.New("context journal record basis is invalid")
	}
	fingerprint, err := ContextBasisFingerprint(basis)
	if err != nil || fingerprint != envelope.Record.BasisFingerprint ||
		contextScopeKey(basis) != envelope.Record.ScopeKey {
		return storedContextRecord{}, errors.New("context journal record basis integrity check failed")
	}
	expectedID, err := contextRecordID(envelope.Record.RequestID)
	if err != nil || expectedID != envelope.Record.ID ||
		!contextDigestPattern.MatchString(envelope.Record.DraftFingerprint) {
		return storedContextRecord{}, errors.New("context journal record request identity is invalid")
	}
	draft, draftFingerprint, err := normalizeContextDraft(ContextDraft{
		Basis: basis, Summary: envelope.Record.Summary, Decisions: envelope.Record.Decisions,
		Remaining: envelope.Record.Remaining, Blockers: envelope.Record.Blockers,
		NextAction: envelope.Record.NextAction, SessionRef: envelope.Record.SessionRef,
	})
	if err != nil || draftFingerprint != envelope.Record.DraftFingerprint {
		return storedContextRecord{}, errors.New("context journal record draft integrity check failed")
	}
	createdAt, err := time.Parse(time.RFC3339Nano, envelope.Record.CreatedAt)
	if err != nil {
		return storedContextRecord{}, errors.New("context journal record timestamp is invalid")
	}
	envelope.Record.Basis = draft.Basis
	envelope.Record.Decisions = draft.Decisions
	envelope.Record.Remaining = draft.Remaining
	envelope.Record.Blockers = draft.Blockers
	return storedContextRecord{record: envelope.Record, bytes: int64(len(data)), createdAt: createdAt}, nil
}

func (j *ContextJournal) loadRecords() ([]storedContextRecord, error) {
	entries, err := os.ReadDir(filepath.Join(j.Dir, "records"))
	if err != nil {
		return nil, err
	}
	records := make([]storedContextRecord, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		record, err := decodeRecord(filepath.Join(j.Dir, "records", entry.Name()), j.Limits.MaxRecordBytes)
		if err != nil {
			return nil, fmt.Errorf("load context journal record %q: %w", entry.Name(), err)
		}
		records = append(records, record)
	}
	sort.Slice(records, func(i, k int) bool {
		if records[i].createdAt.Equal(records[k].createdAt) {
			return records[i].record.ID < records[k].record.ID
		}
		return records[i].createdAt.Before(records[k].createdAt)
	})
	return records, nil
}

func scopeFromBasis(b ContextBasis) ContextScope {
	return ContextScope{RepositoryID: b.RepositoryID, WorktreeID: b.WorktreeID, WorkstreamID: b.WorkstreamID}
}

func normalizeScope(scope ContextScope) (ContextScope, error) {
	if !contextDigestPattern.MatchString(scope.RepositoryID) || !contextDigestPattern.MatchString(scope.WorktreeID) {
		return ContextScope{}, errors.New("context scope identities must be SHA-256 digests")
	}
	if err := validateOptionalUUID(scope.WorkstreamID, "workstream id"); err != nil {
		return ContextScope{}, err
	}
	return scope, nil
}

func scopeKey(scope ContextScope) string {
	return contextScopeKey(ContextBasis{
		RepositoryID: scope.RepositoryID, WorktreeID: scope.WorktreeID, WorkstreamID: scope.WorkstreamID,
	})
}

func latestForScope(records []storedContextRecord, key string) *storedContextRecord {
	for index := len(records) - 1; index >= 0; index-- {
		if records[index].record.ScopeKey == key {
			record := records[index]
			return &record
		}
	}
	return nil
}

func retentionChecksum(state contextRetentionState) (string, error) {
	raw, err := json.Marshal(state)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return hex.EncodeToString(sum[:]), nil
}

func (j *ContextJournal) loadRetention() (contextRetentionState, error) {
	path := filepath.Join(j.Dir, "retention.json")
	data, err := readPrivateContextFile(path, 2<<20)
	if errors.Is(err, os.ErrNotExist) {
		return contextRetentionState{Version: retentionStateVersion, Scopes: map[string]ContextRetentionGap{}, Pending: []contextPendingPrune{}}, nil
	}
	if err != nil {
		return contextRetentionState{}, err
	}
	var envelope contextRetentionEnvelope
	if json.Unmarshal(data, &envelope) != nil || envelope.Version != retentionStateVersion ||
		envelope.State.Version != retentionStateVersion || !contextDigestPattern.MatchString(envelope.Checksum) {
		return contextRetentionState{}, errors.New("context retention state is invalid")
	}
	checksum, err := retentionChecksum(envelope.State)
	if err != nil || checksum != envelope.Checksum {
		return contextRetentionState{}, errors.New("context retention state integrity check failed")
	}
	if envelope.State.Scopes == nil {
		envelope.State.Scopes = map[string]ContextRetentionGap{}
	}
	if envelope.State.Pending == nil {
		envelope.State.Pending = []contextPendingPrune{}
	}
	return envelope.State, nil
}

func (j *ContextJournal) publishRetention(state contextRetentionState) error {
	state.Version = retentionStateVersion
	if state.Scopes == nil {
		state.Scopes = map[string]ContextRetentionGap{}
	}
	if state.Pending == nil {
		state.Pending = []contextPendingPrune{}
	}
	checksum, err := retentionChecksum(state)
	if err != nil {
		return err
	}
	encoded, err := json.Marshal(contextRetentionEnvelope{
		Version: retentionStateVersion, State: state, Checksum: checksum,
	})
	if err != nil {
		return err
	}
	encoded = append(encoded, '\n')
	if len(encoded) > 2<<20 {
		return errors.New("context retention state exceeds size limit")
	}
	return safeio.PublishPrivate(filepath.Join(j.Dir, "retention.json"), encoded, true)
}

func syncDirectory(path string) error {
	dir, err := os.Open(path)
	if err != nil {
		return err
	}
	defer dir.Close()
	return dir.Sync()
}

func (j *ContextJournal) reconcilePending(state contextRetentionState) (contextRetentionState, int, error) {
	if len(state.Pending) == 0 {
		return state, 0, nil
	}
	completed := 0
	now := j.now().UTC().Format(time.RFC3339Nano)
	for _, pending := range state.Pending {
		if !contextDigestPattern.MatchString(pending.RecordID) || !contextDigestPattern.MatchString(pending.ScopeKey) {
			return state, completed, errors.New("context retention pending entry is invalid")
		}
		path := filepath.Join(j.Dir, "records", pending.RecordID+".json")
		if err := os.Remove(path); err != nil && !errors.Is(err, os.ErrNotExist) {
			return state, completed, err
		}
		gap := state.Scopes[pending.ScopeKey]
		gap.PrunedRecords++
		gap.LastPrunedAt = now
		gap.LastPrunedRecordID = pending.RecordID
		state.Scopes[pending.ScopeKey] = gap
		completed++
	}
	if err := syncDirectory(filepath.Join(j.Dir, "records")); err != nil {
		return state, completed, err
	}
	state.Pending = []contextPendingPrune{}
	if err := j.publishRetention(state); err != nil {
		return state, completed, err
	}
	return state, completed, nil
}

func retainedStats(records []storedContextRecord, removed map[string]bool, key string) (int, int64) {
	count := 0
	var bytes int64
	for _, record := range records {
		if removed[record.record.ID] || key != "" && record.record.ScopeKey != key {
			continue
		}
		count++
		bytes += record.bytes
	}
	return count, bytes
}

func planRetention(records []storedContextRecord, limits ContextJournalLimits) ([]contextPendingPrune, error) {
	removed := map[string]bool{}
	latest := map[string]string{}
	for _, record := range records {
		latest[record.record.ScopeKey] = record.record.ID
	}
	scopeKeys := make([]string, 0, len(latest))
	for key := range latest {
		scopeKeys = append(scopeKeys, key)
	}
	sort.Strings(scopeKeys)

	markOldest := func(key string) bool {
		for _, record := range records {
			if removed[record.record.ID] || record.record.ScopeKey != key || latest[key] == record.record.ID {
				continue
			}
			removed[record.record.ID] = true
			return true
		}
		return false
	}
	for _, key := range scopeKeys {
		for {
			count, bytes := retainedStats(records, removed, key)
			if count <= limits.MaxRecordsPerScope && bytes <= limits.MaxScopeBytes {
				break
			}
			if !markOldest(key) {
				return nil, errors.New("context journal per-scope quota cannot retain the latest record")
			}
		}
	}
	for {
		count, bytes := retainedStats(records, removed, "")
		if count <= limits.MaxRecords && bytes <= limits.MaxBytes {
			break
		}
		marked := false
		for _, record := range records {
			if removed[record.record.ID] || latest[record.record.ScopeKey] == record.record.ID {
				continue
			}
			removed[record.record.ID] = true
			marked = true
			break
		}
		if !marked {
			return nil, errors.New("context journal installation quota cannot retain each scope's latest record")
		}
	}
	pending := []contextPendingPrune{}
	for _, record := range records {
		if removed[record.record.ID] {
			pending = append(pending, contextPendingPrune{ScopeKey: record.record.ScopeKey, RecordID: record.record.ID})
		}
	}
	return pending, nil
}

func (j *ContextJournal) enforceRetention(state contextRetentionState, records []storedContextRecord) (contextRetentionState, []storedContextRecord, int, error) {
	pending, err := planRetention(records, j.Limits)
	if err != nil {
		return state, records, 0, err
	}
	if len(pending) == 0 {
		return state, records, 0, nil
	}
	removed := make(map[string]bool, len(pending))
	for _, item := range pending {
		removed[item.RecordID] = true
	}
	state.Pending = pending
	if err := j.publishRetention(state); err != nil {
		return state, records, 0, err
	}
	state, pruned, err := j.reconcilePending(state)
	if err != nil {
		return state, records, pruned, err
	}
	kept := make([]storedContextRecord, 0, len(records)-pruned)
	for _, record := range records {
		if !removed[record.record.ID] {
			kept = append(kept, record)
		}
	}
	return state, kept, pruned, nil
}

func (j *ContextJournal) withLock(ctx context.Context, fn func() error) error {
	release, err := j.lock(ctx)
	if err != nil {
		return err
	}
	defer release()
	return fn()
}

func (j *ContextJournal) Put(ctx context.Context, requestID, expectedPrevious string, draft ContextDraft) (ContextPutResult, error) {
	var result ContextPutResult
	err := j.withLock(ctx, func() error {
		state, err := j.loadRetention()
		if err != nil {
			return err
		}
		state, _, err = j.reconcilePending(state)
		if err != nil {
			return err
		}
		records, err := j.loadRecords()
		if err != nil {
			return err
		}
		state, records, result.PrunedRecords, err = j.enforceRetention(state, records)
		if err != nil {
			return err
		}
		normalized, draftFingerprint, err := normalizeContextDraft(draft)
		if err != nil {
			return err
		}
		recordID, err := contextRecordID(requestID)
		if err != nil {
			return err
		}
		for _, existing := range records {
			if existing.record.ID != recordID {
				continue
			}
			if existing.record.DraftFingerprint != draftFingerprint {
				return errors.New("context request id was reused with different input")
			}
			result.Record = existing.record
			result.Replayed = true
			if gap, ok := state.Scopes[existing.record.ScopeKey]; ok {
				copy := gap
				result.RetentionGap = &copy
			}
			return nil
		}

		scope := scopeFromBasis(normalized.Basis)
		key := scopeKey(scope)
		latest := latestForScope(records, key)
		switch {
		case expectedPrevious == "missing" && latest != nil:
			return errors.New("context journal changed; inspect current context and retry")
		case expectedPrevious == "missing":
		case !contextDigestPattern.MatchString(expectedPrevious):
			return errors.New("invalid expected context record")
		case latest == nil || latest.record.ID != expectedPrevious:
			return errors.New("context journal changed; inspect current context and retry")
		}

		basisFingerprint, err := ContextBasisFingerprint(normalized.Basis)
		if err != nil {
			return err
		}
		record := ContextRecord{
			Version: contextRecordVersion, ID: recordID, RequestID: strings.ToLower(requestID),
			CreatedAt: j.now().UTC().Format(time.RFC3339Nano), ScopeKey: key,
			Basis: normalized.Basis, BasisFingerprint: basisFingerprint, DraftFingerprint: draftFingerprint,
			Summary: normalized.Summary, Decisions: normalized.Decisions, Remaining: normalized.Remaining,
			Blockers: normalized.Blockers, NextAction: normalized.NextAction, SessionRef: normalized.SessionRef,
		}
		encoded, err := encodeRecord(record, j.Limits.MaxRecordBytes)
		if err != nil {
			return err
		}
		candidate := append(records, storedContextRecord{record: record, bytes: int64(len(encoded))})
		sort.Slice(candidate, func(i, k int) bool {
			if candidate[i].record.CreatedAt == candidate[k].record.CreatedAt {
				return candidate[i].record.ID < candidate[k].record.ID
			}
			return candidate[i].record.CreatedAt < candidate[k].record.CreatedAt
		})
		pending, err := planRetention(candidate, j.Limits)
		if err != nil {
			return err
		}
		path := filepath.Join(j.Dir, "records", record.ID+".json")
		if err := safeio.PublishPrivate(path, encoded, false); err != nil {
			return err
		}
		state.Pending = pending
		if len(pending) > 0 {
			if err := j.publishRetention(state); err != nil {
				return err
			}
			state, result.PrunedRecords, err = j.reconcilePending(state)
			if err != nil {
				return err
			}
		}
		result.Record = record
		if gap, ok := state.Scopes[key]; ok {
			copy := gap
			result.RetentionGap = &copy
		}
		return nil
	})
	return result, err
}

func (j *ContextJournal) latestLocked(scope ContextScope) (ContextLatestResult, error) {
	normalized, err := normalizeScope(scope)
	if err != nil {
		return ContextLatestResult{}, err
	}
	state, err := j.loadRetention()
	if err != nil {
		return ContextLatestResult{}, err
	}
	state, _, err = j.reconcilePending(state)
	if err != nil {
		return ContextLatestResult{}, err
	}
	records, err := j.loadRecords()
	if err != nil {
		return ContextLatestResult{}, err
	}
	state, records, _, err = j.enforceRetention(state, records)
	if err != nil {
		return ContextLatestResult{}, err
	}
	key := scopeKey(normalized)
	latest := latestForScope(records, key)
	result := ContextLatestResult{Found: latest != nil}
	if latest != nil {
		copy := latest.record
		result.Record = &copy
	}
	if gap, ok := state.Scopes[key]; ok {
		copy := gap
		result.RetentionGap = &copy
	}
	return result, nil
}

func (j *ContextJournal) Latest(ctx context.Context, scope ContextScope) (ContextLatestResult, error) {
	var result ContextLatestResult
	err := j.withLock(ctx, func() error {
		var err error
		result, err = j.latestLocked(scope)
		return err
	})
	return result, err
}

func (j *ContextJournal) Get(ctx context.Context, scope ContextScope, id string) (ContextLatestResult, error) {
	if !contextDigestPattern.MatchString(id) {
		return ContextLatestResult{}, errors.New("invalid context record id")
	}
	var result ContextLatestResult
	err := j.withLock(ctx, func() error {
		normalized, err := normalizeScope(scope)
		if err != nil {
			return err
		}
		state, err := j.loadRetention()
		if err != nil {
			return err
		}
		state, _, err = j.reconcilePending(state)
		if err != nil {
			return err
		}
		records, err := j.loadRecords()
		if err != nil {
			return err
		}
		state, _, _, err = j.enforceRetention(state, records)
		if err != nil {
			return err
		}
		path := filepath.Join(j.Dir, "records", id+".json")
		record, err := decodeRecord(path, j.Limits.MaxRecordBytes)
		if errors.Is(err, os.ErrNotExist) {
			result = ContextLatestResult{Found: false}
		} else if err != nil {
			return err
		} else if record.record.ScopeKey != scopeKey(normalized) {
			return errors.New("context record belongs to a different scope")
		} else {
			copy := record.record
			result = ContextLatestResult{Found: true, Record: &copy}
		}
		if gap, ok := state.Scopes[scopeKey(normalized)]; ok {
			copy := gap
			result.RetentionGap = &copy
		}
		return nil
	})
	return result, err
}

func (j *ContextJournal) List(ctx context.Context, scope ContextScope, limit int) (ContextListResult, error) {
	if limit < 1 || limit > 200 {
		return ContextListResult{}, errors.New("context list limit must be between 1 and 200")
	}
	var result ContextListResult
	err := j.withLock(ctx, func() error {
		normalized, err := normalizeScope(scope)
		if err != nil {
			return err
		}
		state, err := j.loadRetention()
		if err != nil {
			return err
		}
		state, _, err = j.reconcilePending(state)
		if err != nil {
			return err
		}
		records, err := j.loadRecords()
		if err != nil {
			return err
		}
		state, records, _, err = j.enforceRetention(state, records)
		if err != nil {
			return err
		}
		key := scopeKey(normalized)
		selected := []ContextRecord{}
		for index := len(records) - 1; index >= 0 && len(selected) < limit; index-- {
			if records[index].record.ScopeKey == key {
				selected = append(selected, records[index].record)
			}
		}
		total := 0
		for _, record := range records {
			if record.record.ScopeKey == key {
				total++
			}
		}
		result = ContextListResult{Records: selected, Complete: len(selected) == total}
		if gap, ok := state.Scopes[key]; ok {
			copy := gap
			result.RetentionGap = &copy
			if gap.PrunedRecords > 0 {
				result.Complete = false
			}
		}
		return nil
	})
	return result, err
}
