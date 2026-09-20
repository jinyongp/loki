package jobs

import (
	"bytes"
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
	"sync"
	"time"

	"golang.org/x/sys/unix"

	"loki/internal/platform/safeio"
)

const journalVersion = 2

type JournalLimits struct {
	MaxRecords     int
	MaxRecordBytes int64
	MaxOutputBytes int
	Retention      time.Duration
}

type Journal struct {
	dir      string
	limits   JournalLimits
	lockFile *os.File
	mu       sync.Mutex
	records  map[string]Record
	closed   bool
}

type journalEnvelope struct {
	Version int             `json:"version"`
	Record  json.RawMessage `json:"record"`
	SHA256  string          `json:"sha256"`
}

func OpenJournal(dir string, limits JournalLimits) (*Journal, error) {
	if !filepath.IsAbs(dir) || filepath.Clean(dir) != dir || dir == string(filepath.Separator) || strings.ContainsRune(dir, 0) {
		return nil, errors.New("job journal directory must be an absolute clean non-root path")
	}
	if limits.MaxRecords == 0 {
		limits.MaxRecords = 1024
	}
	if limits.MaxRecordBytes == 0 {
		limits.MaxRecordBytes = 2 << 20
	}
	if limits.MaxOutputBytes == 0 {
		limits.MaxOutputBytes = 256 << 10
	}
	if limits.Retention == 0 {
		limits.Retention = time.Hour
	}
	if limits.MaxRecords < 1 || limits.MaxRecords > 4096 ||
		limits.MaxRecordBytes < 4096 || limits.MaxRecordBytes > 32<<20 ||
		limits.MaxOutputBytes < 1 || limits.MaxOutputBytes > MaxOutputBytes ||
		int64(limits.MaxOutputBytes)*6+(64<<10) > limits.MaxRecordBytes ||
		limits.Retention < 10*time.Millisecond || limits.Retention > 24*time.Hour {
		return nil, errors.New("job journal limits are outside the supported range")
	}
	info, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return nil, errors.New("job journal directory must be a private real directory")
	}

	lockPath := filepath.Join(dir, "journal.lock")
	fd, err := unix.Open(lockPath, unix.O_CREAT|unix.O_RDWR|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0600)
	if err != nil {
		return nil, err
	}
	lockFile := os.NewFile(uintptr(fd), "job-journal.lock")
	lockInfo, statErr := lockFile.Stat()
	if statErr != nil || !lockInfo.Mode().IsRegular() || lockInfo.Mode().Perm()&0077 != 0 {
		lockFile.Close()
		return nil, errors.New("job journal lock must be a private regular file")
	}
	if err = unix.Flock(fd, unix.LOCK_EX|unix.LOCK_NB); err != nil {
		lockFile.Close()
		if errors.Is(err, unix.EWOULDBLOCK) {
			return nil, errors.New("job journal is already owned by another process")
		}
		return nil, err
	}

	journal := &Journal{dir: dir, limits: limits, lockFile: lockFile, records: map[string]Record{}}
	if err = journal.load(); err != nil {
		journal.Close()
		return nil, err
	}
	if err = journal.pruneExpired(time.Now().UTC()); err != nil {
		journal.Close()
		return nil, err
	}
	return journal, nil
}

func (j *Journal) Close() error {
	if j == nil {
		return nil
	}
	j.mu.Lock()
	defer j.mu.Unlock()
	if j.closed {
		return nil
	}
	j.closed = true
	if j.lockFile == nil {
		return nil
	}
	fd := int(j.lockFile.Fd())
	unlockErr := unix.Flock(fd, unix.LOCK_UN)
	closeErr := j.lockFile.Close()
	j.lockFile = nil
	return errors.Join(unlockErr, closeErr)
}

func (j *Journal) MaxOutputBytes() int {
	if j == nil {
		return 0
	}
	return j.limits.MaxOutputBytes
}

func (j *Journal) Admit(id, backendRef string, deadline, now time.Time) (Record, error) {
	record, replayed, err := j.admit(
		id, backendRef, "", "", NetworkNone, nil, deadline, now, false,
	)
	if replayed {
		return Record{}, errors.New("legacy job admission cannot replay")
	}
	return record, err
}

func (j *Journal) AdmitRequest(id, backendRef, requestID, requestSHA256 string, deadline, now time.Time) (Record, bool, error) {
	return j.AdmitRequestWithIntent(
		id, backendRef, requestID, requestSHA256, NetworkNone, nil, deadline, now,
	)
}

func (j *Journal) AdmitRequestWithIntent(
	id, backendRef, requestID, requestSHA256 string,
	network NetworkProfile, endpoints []EndpointRequest,
	deadline, now time.Time,
) (Record, bool, error) {
	requestID, err := NormalizeRequestID(requestID)
	if err != nil {
		return Record{}, false, err
	}
	expectedID, err := JobIDForRequestID(requestID)
	if err != nil || id != expectedID {
		return Record{}, false, errors.New("job ID does not match request identity")
	}
	requestSHA256 = strings.ToLower(strings.TrimSpace(requestSHA256))
	if !validReplayIdentity(requestID, requestSHA256) {
		return Record{}, false, errors.New("job request fingerprint is invalid")
	}
	network, err = NormalizeNetworkProfile(network)
	if err != nil {
		return Record{}, false, err
	}
	endpoints, err = normalizeEndpointRequests(endpoints)
	if err != nil {
		return Record{}, false, err
	}
	return j.admit(
		id, backendRef, requestID, requestSHA256, network, endpoints, deadline, now, true,
	)
}

func (j *Journal) admit(
	id, backendRef, requestID, requestSHA256 string,
	network NetworkProfile, endpoints []EndpointRequest,
	deadline, now time.Time, allowReplay bool,
) (Record, bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.ready(); err != nil {
		return Record{}, false, err
	}
	if !jobIDPattern.MatchString(id) || !validBackendRef(backendRef) ||
		!validReplayIdentity(requestID, requestSHA256) || !network.Valid() ||
		!validEndpointRequests(endpoints) {
		return Record{}, false, errors.New("job admission record is invalid")
	}
	now = now.UTC()
	deadline = deadline.UTC()
	if !deadline.After(now) || deadline.Sub(now) > 24*time.Hour {
		return Record{}, false, errors.New("job admission deadline is invalid")
	}
	if err := j.pruneExpired(now); err != nil {
		return Record{}, false, err
	}
	if existing, exists := j.records[id]; exists {
		if allowReplay && existing.RequestID == requestID && existing.RequestSHA256 == requestSHA256 &&
			existing.Network == network && sameEndpointRequests(existing.EndpointRequests, endpoints) {
			return cloneRecord(existing), true, nil
		}
		if allowReplay {
			return Record{}, false, ErrReplayConflict
		}
		return Record{}, false, errors.New("job is already recorded")
	}
	if len(j.records) >= j.limits.MaxRecords {
		return Record{}, false, errors.New("job journal capacity is exhausted")
	}
	record := Record{
		ID: id, BackendRef: backendRef, RequestID: requestID, RequestSHA256: requestSHA256,
		Network: network, EndpointRequests: append([]EndpointRequest(nil), endpoints...),
		State:     StateAdmitted,
		CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano),
		DeadlineAt: deadline.Format(time.RFC3339Nano),
	}
	if err := j.publish(record, false); err != nil {
		return Record{}, false, err
	}
	j.records[id] = record
	return cloneRecord(record), false, nil
}

func (j *Journal) BindInstance(id, instanceRef string, now time.Time) (Record, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	record, ok := j.records[id]
	if err := j.ready(); err != nil {
		return Record{}, err
	}
	if !ok || record.State != StateAdmitted || !validRequiredInstanceRef(instanceRef) {
		return Record{}, errors.New("job instance binding is invalid")
	}
	if record.InstanceRef != "" && record.InstanceRef != instanceRef {
		return Record{}, errors.New("job instance binding cannot change")
	}
	if record.InstanceRef == instanceRef {
		return cloneRecord(record), nil
	}
	record.InstanceRef = instanceRef
	record.UpdatedAt = now.UTC().Format(time.RFC3339Nano)
	if err := j.publish(record, true); err != nil {
		return Record{}, err
	}
	j.records[id] = record
	return cloneRecord(record), nil
}

func (j *Journal) BindEndpointLeases(id string, leases []EndpointLease, now time.Time) (Record, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	record, ok := j.records[id]
	if err := j.ready(); err != nil {
		return Record{}, err
	}
	if !ok || (record.State != StateAdmitted && record.State != StateRunning) ||
		!validRequiredInstanceRef(record.InstanceRef) {
		return Record{}, errors.New("job endpoint lease binding is invalid")
	}
	if len(record.EndpointRequests) == 0 {
		if len(leases) != 0 {
			return Record{}, errors.New("job has no endpoint requests")
		}
		return cloneRecord(record), nil
	}
	if len(record.EndpointLeases) != 0 {
		if len(record.EndpointLeases) != len(leases) {
			return Record{}, errors.New("job endpoint lease binding cannot change")
		}
		for index := range record.EndpointLeases {
			if record.EndpointLeases[index].Name != leases[index].Name ||
				record.EndpointLeases[index].Port != leases[index].Port ||
				record.EndpointLeases[index].HostPort != leases[index].HostPort {
				return Record{}, errors.New("job endpoint lease binding cannot change")
			}
		}
		return cloneRecord(record), nil
	}
	if record.State != StateAdmitted {
		return Record{}, errors.New("running job cannot create endpoint leases")
	}

	now = now.UTC()
	byName := make(map[string]EndpointLease, len(leases))
	for _, lease := range leases {
		if lease.Name == "" || lease.Port < 1 || lease.HostPort < 1024 || lease.HostPort > 65535 {
			return Record{}, errors.New("job endpoint lease is invalid")
		}
		if _, exists := byName[lease.Name]; exists {
			return Record{}, errors.New("job endpoint lease is duplicated")
		}
		byName[lease.Name] = lease
	}
	bound := make([]EndpointLease, 0, len(record.EndpointRequests))
	seenHostPorts := map[int]bool{}
	for _, request := range record.EndpointRequests {
		lease, exists := byName[request.Name]
		if !exists || lease.Port != request.Port || seenHostPorts[lease.HostPort] {
			return Record{}, errors.New("job endpoint lease does not match request")
		}
		leaseID, err := EndpointLeaseID(record.ID, request.Name, record.InstanceRef)
		if err != nil {
			return Record{}, err
		}
		seenHostPorts[lease.HostPort] = true
		bound = append(bound, EndpointLease{
			ID: leaseID, JobID: record.ID, Name: request.Name, Port: request.Port,
			HostPort: lease.HostPort, State: EndpointLeaseActive,
			CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano),
		})
	}
	record.EndpointLeases = bound
	record.UpdatedAt = now.Format(time.RFC3339Nano)
	if !record.valid(j.limits.MaxOutputBytes) {
		return Record{}, errors.New("job endpoint lease record is invalid")
	}
	if err := j.publish(record, true); err != nil {
		return Record{}, err
	}
	j.records[id] = record
	return cloneRecord(record), nil
}

func releaseEndpointLeases(record *Record, now time.Time) {
	for index := range record.EndpointLeases {
		record.EndpointLeases[index].State = EndpointLeaseReleased
		record.EndpointLeases[index].UpdatedAt = now.Format(time.RFC3339Nano)
	}
}

func (j *Journal) MarkRunning(id string, now time.Time) (Record, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	record, ok := j.records[id]
	if err := j.ready(); err != nil {
		return Record{}, err
	}
	if !ok || record.State != StateAdmitted || !validRequiredInstanceRef(record.InstanceRef) ||
		!endpointLeasesExact(record.EndpointRequests, record.EndpointLeases, EndpointLeaseActive) {
		return Record{}, errors.New("job cannot transition to running")
	}
	record.State = StateRunning
	record.UpdatedAt = now.UTC().Format(time.RFC3339Nano)
	if err := j.publish(record, true); err != nil {
		return Record{}, err
	}
	j.records[id] = record
	return cloneRecord(record), nil
}

func (j *Journal) MarkTerminal(id string, result Result, now time.Time) (Record, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	record, ok := j.records[id]
	if err := j.ready(); err != nil {
		return Record{}, err
	}
	if !ok {
		return Record{}, errors.New("job terminal transition requires an existing record")
	}
	if record.State != StateAdmitted && record.State != StateRunning {
		return Record{}, errors.New("job terminal transition requires an active record")
	}
	if !result.Valid(j.limits.MaxOutputBytes) {
		return Record{}, errors.New("job terminal result is invalid")
	}
	if result.Cleanup != CleanupPending && result.Cleanup != CleanupNotRequired &&
		result.Cleanup != CleanupComplete && result.Cleanup != CleanupFailed {
		return Record{}, errors.New("job terminal cleanup state is invalid")
	}
	now = now.UTC()
	record.State = StateTerminal
	record.Result = &result
	releaseEndpointLeases(&record, now)
	record.UpdatedAt = now.Format(time.RFC3339Nano)
	record.ExpiresAt = now.Add(j.limits.Retention).Format(time.RFC3339Nano)
	if err := j.publish(record, true); err != nil {
		return Record{}, err
	}
	j.records[id] = record
	return cloneRecord(record), nil
}

func (j *Journal) MarkCleanup(id string, status CleanupStatus, now time.Time) (Record, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	record, ok := j.records[id]
	if err := j.ready(); err != nil {
		return Record{}, err
	}
	if !ok || record.State != StateTerminal || record.Result == nil ||
		(status != CleanupComplete && status != CleanupFailed) {
		return Record{}, errors.New("job cleanup transition is invalid")
	}
	if record.Result.Cleanup == CleanupNotRequired {
		return Record{}, errors.New("job cleanup is not required")
	}
	if record.Result.Cleanup == CleanupComplete {
		if status != CleanupComplete {
			return Record{}, errors.New("completed job cleanup cannot regress")
		}
		return cloneRecord(record), nil
	}
	if record.Result.Cleanup == CleanupFailed && status == CleanupFailed {
		return cloneRecord(record), nil
	}
	now = now.UTC()
	record.Result.Cleanup = status
	if status == CleanupComplete {
		releaseEndpointLeases(&record, now)
	}
	record.UpdatedAt = now.Format(time.RFC3339Nano)
	if status == CleanupComplete {
		record.ExpiresAt = now.Add(j.limits.Retention).Format(time.RFC3339Nano)
	}
	if err := j.publish(record, true); err != nil {
		return Record{}, err
	}
	j.records[id] = record
	return cloneRecord(record), nil
}

func (j *Journal) Get(id string) (Record, bool, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.ready(); err != nil {
		return Record{}, false, err
	}
	record, ok := j.records[id]
	if !ok {
		return Record{}, false, nil
	}
	return cloneRecord(record), true, nil
}

func (j *Journal) List() ([]Record, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.ready(); err != nil {
		return nil, err
	}
	result := make([]Record, 0, len(j.records))
	for _, record := range j.records {
		result = append(result, cloneRecord(record))
	}
	sort.Slice(result, func(i, k int) bool {
		if result[i].CreatedAt == result[k].CreatedAt {
			return result[i].ID < result[k].ID
		}
		return result[i].CreatedAt < result[k].CreatedAt
	})
	return result, nil
}

func (j *Journal) Prune(now time.Time) error {
	j.mu.Lock()
	defer j.mu.Unlock()
	if err := j.ready(); err != nil {
		return err
	}
	return j.pruneExpired(now.UTC())
}

func (j *Journal) pruneExpired(now time.Time) error {
	for id, record := range j.records {
		if record.State != StateTerminal || record.Result == nil ||
			(record.Result.Cleanup != CleanupComplete && record.Result.Cleanup != CleanupNotRequired) {
			continue
		}
		expiresAt, err := time.Parse(time.RFC3339Nano, record.ExpiresAt)
		if err != nil {
			return errors.New("job journal contains an invalid expiry")
		}
		if now.Before(expiresAt) {
			continue
		}
		if err = os.Remove(j.recordPath(id)); err != nil && !errors.Is(err, os.ErrNotExist) {
			return err
		}
		delete(j.records, id)
	}
	if directory, err := os.Open(j.dir); err == nil {
		defer directory.Close()
		return directory.Sync()
	} else {
		return err
	}
}

func (j *Journal) ready() error {
	if j == nil || j.closed || j.lockFile == nil {
		return errors.New("job journal is closed")
	}
	return nil
}

func (j *Journal) recordPath(id string) string {
	return filepath.Join(j.dir, id+".json")
}

func (j *Journal) publish(record Record, overwrite bool) error {
	if !record.valid(j.limits.MaxOutputBytes) {
		return errors.New("job journal record is invalid")
	}
	rawRecord, err := json.Marshal(record)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(rawRecord)
	envelope, err := json.Marshal(journalEnvelope{Version: journalVersion, Record: rawRecord, SHA256: hex.EncodeToString(sum[:])})
	if err != nil {
		return err
	}
	envelope = append(envelope, '\n')
	if int64(len(envelope)) > j.limits.MaxRecordBytes {
		return errors.New("job journal record exceeds size limit")
	}
	return safeio.PublishPrivate(j.recordPath(record.ID), envelope, overwrite)
}

func (j *Journal) load() error {
	entries, err := os.ReadDir(j.dir)
	if err != nil {
		return err
	}
	count := 0
	for _, entry := range entries {
		if entry.Name() == "journal.lock" {
			continue
		}
		if strings.HasPrefix(entry.Name(), ".loki-private-") {
			info, infoErr := entry.Info()
			if infoErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
				return errors.New("job journal contains an unsafe interrupted publication")
			}
			if err = os.Remove(filepath.Join(j.dir, entry.Name())); err != nil {
				return err
			}
			continue
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			return fmt.Errorf("job journal contains unexpected entry %q", entry.Name())
		}
		id := strings.TrimSuffix(entry.Name(), ".json")
		if !jobIDPattern.MatchString(id) {
			return errors.New("job journal contains an invalid record name")
		}
		count++
		if count > j.limits.MaxRecords {
			return errors.New("job journal exceeds record capacity")
		}
		record, err := j.loadRecord(j.recordPath(id))
		if err != nil {
			return fmt.Errorf("load job record %q: %w", id, err)
		}
		if record.ID != id {
			return errors.New("job journal record identity does not match its file")
		}
		j.records[id] = record
	}
	return nil
}

func (j *Journal) loadRecord(path string) (Record, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return Record{}, err
	}
	file := os.NewFile(uintptr(fd), filepath.Base(path))
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
		return Record{}, errors.New("job journal record must be a private regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, j.limits.MaxRecordBytes+1))
	if err != nil {
		return Record{}, err
	}
	if int64(len(raw)) > j.limits.MaxRecordBytes {
		return Record{}, errors.New("job journal record exceeds size limit")
	}
	var envelope journalEnvelope
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err = decoder.Decode(&envelope); err != nil || envelope.Version != journalVersion {
		return Record{}, errors.New("job journal version is unsupported or invalid")
	}
	var trailing any
	if err = decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return Record{}, errors.New("job journal contains trailing data")
	}
	sum := sha256.Sum256(envelope.Record)
	if envelope.SHA256 != hex.EncodeToString(sum[:]) {
		return Record{}, errors.New("job journal record integrity check failed")
	}
	var record Record
	recordDecoder := json.NewDecoder(bytes.NewReader(envelope.Record))
	recordDecoder.DisallowUnknownFields()
	if err = recordDecoder.Decode(&record); err != nil {
		return Record{}, errors.New("job journal record is invalid")
	}
	if err = recordDecoder.Decode(&trailing); !errors.Is(err, io.EOF) || !record.valid(j.limits.MaxOutputBytes) {
		return Record{}, errors.New("job journal record is invalid")
	}
	return record, nil
}

func cloneRecord(record Record) Record {
	copy := record
	copy.EndpointRequests = append([]EndpointRequest(nil), record.EndpointRequests...)
	copy.EndpointLeases = append([]EndpointLease(nil), record.EndpointLeases...)
	if record.Result != nil {
		result := *record.Result
		copy.Result = &result
	}
	return copy
}
