package jobs

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func privateJournalDir(t *testing.T) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), "jobs")
	if err := os.Mkdir(dir, 0700); err != nil {
		t.Fatal(err)
	}
	return dir
}

func testJournal(t *testing.T, dir string) *Journal {
	t.Helper()
	journal, err := OpenJournal(dir, JournalLimits{MaxRecords: 4, MaxRecordBytes: 128 << 10, MaxOutputBytes: 4096, Retention: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := journal.Close(); err != nil {
			t.Errorf("close journal: %v", err)
		}
	})
	return journal
}

func waitForJournalLock(ctx context.Context, dir string) error {
	for {
		journal, err := OpenJournal(dir, JournalLimits{})
		if err == nil {
			return journal.Close()
		}
		if !strings.Contains(err.Error(), "already owned") {
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(10 * time.Millisecond):
		}
	}
}

func testInstanceRef() string {
	return "oci-instance-sha256:" + strings.Repeat("1", 64)
}

func TestNormalizeOutputBoundsAndRepairsUTF8(t *testing.T) {
	output, err := NormalizeOutput([]byte{'a', 0xff, 'b', 'c', 'd'}, 5, false)
	if err != nil {
		t.Fatal(err)
	}
	if output.Text != "a�b" || !output.Truncated {
		t.Fatalf("output = %#v", output)
	}
	if _, err = NormalizeOutput([]byte("ok"), 0, false); err == nil {
		t.Fatal("invalid output limit accepted")
	}
}

func TestReadJournalSnapshotDoesNotAcquireWriterLock(t *testing.T) {
	dir := privateJournalDir(t)
	now := time.Now().UTC()
	journal := testJournal(t, dir)
	id := strings.Repeat("f", 32)
	if _, err := journal.Admit(id, "oci:"+strings.Repeat("e", 64), now.Add(time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.BindInstance(id, testInstanceRef(), now.Add(time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	if _, err := journal.MarkRunning(id, now.Add(2*time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".loki-private-snapshot"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}

	records, err := ReadJournalSnapshot(dir, JournalLimits{})
	if err != nil {
		t.Fatal(err)
	}
	if len(records) != 1 || records[0].ID != id || records[0].State != StateRunning {
		t.Fatalf("snapshot = %#v", records)
	}
	if _, err = OpenJournal(dir, JournalLimits{}); err == nil || !strings.Contains(err.Error(), "already owned") {
		t.Fatalf("snapshot disturbed writer lock: %v", err)
	}
}

func TestJournalPersistsTransitionsAcrossReopenWithoutConsumingResult(t *testing.T) {
	dir := privateJournalDir(t)
	now := time.Now().UTC()
	journal := testJournal(t, dir)
	id := strings.Repeat("a", 32)
	admitted, err := journal.Admit(id, "oci:"+strings.Repeat("b", 64), now.Add(time.Minute), now)
	if err != nil || admitted.State != StateAdmitted {
		t.Fatalf("admit = %#v, %v", admitted, err)
	}
	if _, err = journal.BindInstance(id, testInstanceRef(), now.Add(time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.MarkRunning(id, now.Add(2*time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	exitCode := int64(7)
	result := Result{
		ExitCode: &exitCode,
		Outcome:  OutcomeExited,
		Output:   Output{Text: "hello", Truncated: false},
		Cleanup:  CleanupPending,
	}
	terminal, err := journal.MarkTerminal(id, result, now.Add(3*time.Nanosecond))
	if err != nil || terminal.State != StateTerminal || terminal.Result == nil {
		t.Fatalf("terminal = %#v, %v", terminal, err)
	}
	if _, err = journal.MarkCleanup(id, CleanupComplete, now.Add(4*time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	first, ok, err := journal.Get(id)
	if err != nil || !ok || first.Result == nil || first.Result.Output.Text != "hello" {
		t.Fatalf("first get = %#v, %v, %v", first, ok, err)
	}
	second, ok, err := journal.Get(id)
	if err != nil || !ok || second.Result == nil || second.Result.Output.Text != "hello" {
		t.Fatalf("second get = %#v, %v, %v", second, ok, err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := OpenJournal(dir, JournalLimits{MaxRecords: 4, MaxRecordBytes: 128 << 10, MaxOutputBytes: 4096, Retention: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	got, ok, err := reopened.Get(id)
	if err != nil || !ok || got.InstanceRef != testInstanceRef() ||
		got.Result == nil || got.Result.Cleanup != CleanupComplete || got.Result.Output.Text != "hello" {
		t.Fatalf("reopened = %#v, %v, %v", got, ok, err)
	}
}

func TestJournalReplayAdmissionReturnsExistingRecordAndConflictsChangedInput(t *testing.T) {
	dir := privateJournalDir(t)
	journal := testJournal(t, dir)
	now := time.Now().UTC()
	requestID := testRequestID()
	id, err := JobIDForRequestID(requestID)
	if err != nil {
		t.Fatal(err)
	}
	normalized, fingerprint, err := normalizeStartRequest(StartRequest{
		RequestID: requestID, CWD: ".", Argv: []string{"/bin/true"}, TimeoutSeconds: 30,
		Network:   NetworkDependencyInstall,
		Endpoints: []EndpointRequest{{Name: "web", Port: 5173}, {Name: "api", Port: 3000}},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, replayed, err := journal.AdmitRequestWithIntent(
		id, "oci:"+strings.Repeat("b", 64), requestID, fingerprint,
		normalized.Network, normalized.Endpoints, now.Add(time.Minute), now,
	)
	if err != nil || replayed {
		t.Fatalf("first admission = %#v, replayed=%v, err=%v", first, replayed, err)
	}
	if first.RequestID != requestID || first.RequestSHA256 != fingerprint || first.State != StateAdmitted ||
		first.Network != NetworkDependencyInstall || !sameEndpointRequests(first.EndpointRequests, normalized.Endpoints) {
		t.Fatalf("first record = %#v", first)
	}

	second, replayed, err := journal.AdmitRequestWithIntent(
		id, "oci:"+strings.Repeat("c", 64), requestID, fingerprint,
		normalized.Network, []EndpointRequest{{Name: "api", Port: 3000}, {Name: "web", Port: 5173}},
		now.Add(2*time.Minute), now.Add(time.Second),
	)
	if err != nil || !replayed {
		t.Fatalf("replay admission = %#v, replayed=%v, err=%v", second, replayed, err)
	}
	if second.BackendRef != first.BackendRef || second.DeadlineAt != first.DeadlineAt {
		t.Fatalf("replay changed retained authority: first=%#v second=%#v", first, second)
	}

	_, changedFingerprint, err := normalizeStartRequest(StartRequest{
		RequestID: requestID, CWD: ".", Argv: []string{"/bin/true"}, TimeoutSeconds: 31,
		Network: normalized.Network, Endpoints: normalized.Endpoints,
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, replayed, err = journal.AdmitRequestWithIntent(
		id, "oci:"+strings.Repeat("b", 64), requestID, changedFingerprint,
		normalized.Network, normalized.Endpoints, now.Add(time.Minute), now,
	); !errors.Is(err, ErrReplayConflict) || replayed {
		t.Fatalf("changed-input replay = replayed=%v err=%v", replayed, err)
	}

	if _, replayed, err = journal.AdmitRequestWithIntent(
		id, "oci:"+strings.Repeat("b", 64), requestID, fingerprint,
		NetworkNone, nil, now.Add(time.Minute), now,
	); !errors.Is(err, ErrReplayConflict) || replayed {
		t.Fatalf("changed-intent replay = replayed=%v err=%v", replayed, err)
	}

	if _, _, err = journal.AdmitRequestWithIntent(
		strings.Repeat("f", 32), "oci:"+strings.Repeat("b", 64), requestID, fingerprint,
		normalized.Network, normalized.Endpoints, now.Add(time.Minute), now,
	); err == nil {
		t.Fatal("request identity was admitted with a caller-chosen Job ID")
	}
}

func TestJournalBindsAndReleasesEndpointLeases(t *testing.T) {
	dir := privateJournalDir(t)
	journal := testJournal(t, dir)
	now := time.Now().UTC()
	requestID := testRequestID()
	id, err := JobIDForRequestID(requestID)
	if err != nil {
		t.Fatal(err)
	}
	normalized, fingerprint, err := normalizeStartRequest(StartRequest{
		RequestID: requestID, CWD: ".", Argv: []string{"/bin/sleep", "10"},
		Network:   NetworkDependencyInstall,
		Endpoints: []EndpointRequest{{Name: "web", Port: 5173}, {Name: "api", Port: 3000}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = journal.AdmitRequestWithIntent(
		id, "oci:"+strings.Repeat("b", 64), requestID, fingerprint,
		normalized.Network, normalized.Endpoints, now.Add(time.Minute), now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.BindInstance(id, testInstanceRef(), now.Add(time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.MarkRunning(id, now.Add(2*time.Nanosecond)); err == nil {
		t.Fatal("running transition accepted before endpoint leases")
	}
	bound, err := journal.BindEndpointLeases(id, []EndpointLease{
		{Name: "api", Port: 3000, HostPort: 43001},
		{Name: "web", Port: 5173, HostPort: 43002},
	}, now.Add(3*time.Nanosecond))
	if err != nil {
		t.Fatal(err)
	}
	if len(bound.EndpointLeases) != 2 ||
		bound.EndpointLeases[0].State != EndpointLeaseActive ||
		bound.EndpointLeases[1].State != EndpointLeaseActive {
		t.Fatalf("bound leases = %#v", bound.EndpointLeases)
	}
	for _, lease := range bound.EndpointLeases {
		expectedID, err := EndpointLeaseID(id, lease.Name, testInstanceRef())
		if err != nil || lease.ID != expectedID || lease.JobID != id {
			t.Fatalf("lease identity = %#v, %v", lease, err)
		}
	}
	if _, err = journal.MarkRunning(id, now.Add(4*time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	exitCode := int64(0)
	terminal, err := journal.MarkTerminal(id, Result{
		ExitCode: &exitCode, Outcome: OutcomeExited, Cleanup: CleanupPending,
	}, now.Add(5*time.Nanosecond))
	if err != nil {
		t.Fatal(err)
	}
	for _, lease := range terminal.EndpointLeases {
		if lease.State != EndpointLeaseReleased ||
			lease.UpdatedAt != now.Add(5*time.Nanosecond).Format(time.RFC3339Nano) {
			t.Fatalf("terminal lease = %#v", lease)
		}
	}
	if _, err = journal.BindEndpointLeases(id, []EndpointLease{
		{Name: "api", Port: 3000, HostPort: 43003},
		{Name: "web", Port: 5173, HostPort: 43002},
	}, now.Add(6*time.Nanosecond)); err == nil {
		t.Fatal("terminal endpoint lease rebinding was accepted")
	}
}

func TestJournalRejectsChangedOrReusedEndpointHostPorts(t *testing.T) {
	dir := privateJournalDir(t)
	journal := testJournal(t, dir)
	now := time.Now().UTC()
	requestID := "123e4567-e89b-12d3-a456-426614174010"
	id, err := JobIDForRequestID(requestID)
	if err != nil {
		t.Fatal(err)
	}
	normalized, fingerprint, err := normalizeStartRequest(StartRequest{
		RequestID: requestID, CWD: ".", Argv: []string{"/bin/true"},
		Endpoints: []EndpointRequest{{Name: "api", Port: 3000}, {Name: "web", Port: 5173}},
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = journal.AdmitRequestWithIntent(
		id, "oci:"+strings.Repeat("b", 64), requestID, fingerprint,
		normalized.Network, normalized.Endpoints, now.Add(time.Minute), now,
	); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.BindInstance(id, testInstanceRef(), now); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.BindEndpointLeases(id, []EndpointLease{
		{Name: "api", Port: 3000, HostPort: 43001},
		{Name: "web", Port: 5173, HostPort: 43001},
	}, now); err == nil {
		t.Fatal("reused host port was accepted")
	}
	if _, err = journal.BindEndpointLeases(id, []EndpointLease{
		{Name: "api", Port: 3001, HostPort: 43001},
		{Name: "web", Port: 5173, HostPort: 43002},
	}, now); err == nil {
		t.Fatal("mismatched endpoint port was accepted")
	}
}

func TestJournalReplayIdentitySurvivesReopen(t *testing.T) {
	dir := privateJournalDir(t)
	limits := JournalLimits{MaxRecords: 4, MaxRecordBytes: 128 << 10, MaxOutputBytes: 4096, Retention: time.Minute}
	journal, err := OpenJournal(dir, limits)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	requestID := testRequestID()
	id, err := JobIDForRequestID(requestID)
	if err != nil {
		t.Fatal(err)
	}
	fingerprint, err := StartFingerprint(".", []string{"/bin/true"}, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, replayed, err := journal.AdmitRequest(
		id, "oci:"+strings.Repeat("b", 64), requestID, fingerprint,
		now.Add(time.Minute), now,
	); err != nil || replayed {
		t.Fatalf("admit = replayed=%v, %v", replayed, err)
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := OpenJournal(dir, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer reopened.Close()
	record, ok, err := reopened.Get(id)
	if err != nil || !ok || record.RequestID != requestID || record.RequestSHA256 != fingerprint {
		t.Fatalf("reopened record = %#v, ok=%v, err=%v", record, ok, err)
	}
}

func TestJournalFailsClosedOnCorruptionVersionAndConcurrentOwner(t *testing.T) {
	dir := privateJournalDir(t)
	journal := testJournal(t, dir)
	if _, err := OpenJournal(dir, JournalLimits{}); err == nil || !strings.Contains(err.Error(), "already owned") {
		t.Fatalf("second owner error = %v", err)
	}
	if err := journal.Close(); err != nil {
		t.Fatal(err)
	}

	id := strings.Repeat("c", 32)
	recordPath := filepath.Join(dir, id+".json")
	if err := os.WriteFile(recordPath, []byte("{\"version\":999,\"record\":{},\"sha256\":\"bad\"}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJournal(dir, JournalLimits{}); err == nil || !strings.Contains(err.Error(), "unsupported or invalid") {
		t.Fatalf("version error = %v", err)
	}

	if err := os.Remove(recordPath); err != nil {
		t.Fatal(err)
	}
	recordNow := time.Now().UTC()
	record := Record{
		ID: id, BackendRef: "oci:" + strings.Repeat("d", 64), Network: NetworkNone, State: StateAdmitted,
		CreatedAt:  recordNow.Format(time.RFC3339Nano),
		UpdatedAt:  recordNow.Format(time.RFC3339Nano),
		DeadlineAt: recordNow.Add(time.Minute).Format(time.RFC3339Nano),
	}
	rawRecord, _ := json.Marshal(record)
	envelope, _ := json.Marshal(journalEnvelope{Version: journalVersion, Record: rawRecord, SHA256: strings.Repeat("0", 64)})
	if err := os.WriteFile(recordPath, append(envelope, '\n'), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := OpenJournal(dir, JournalLimits{}); err == nil || !strings.Contains(err.Error(), "integrity") {
		t.Fatalf("integrity error = %v", err)
	}

	if err := os.Remove(recordPath); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, ".loki-private-interrupted"), []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	recovered, err := OpenJournal(dir, JournalLimits{})
	if err != nil {
		t.Fatalf("interrupted publication recovery: %v", err)
	}
	if err = recovered.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(filepath.Join(dir, ".loki-private-interrupted")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("interrupted publication was not removed: %v", err)
	}
}

func TestJournalPrunesExpiredTerminalRecordsButKeepsNonterminal(t *testing.T) {
	dir := privateJournalDir(t)
	limits := JournalLimits{MaxRecords: 4, MaxRecordBytes: 128 << 10, MaxOutputBytes: 4096, Retention: time.Second}
	journal, err := OpenJournal(dir, limits)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Unix(1_700_000_000, 0).UTC()
	terminalID := strings.Repeat("e", 32)
	runningID := strings.Repeat("f", 32)
	debtID := strings.Repeat("d", 32)
	if _, err = journal.Admit(terminalID, "oci:"+strings.Repeat("1", 64), now.Add(time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.MarkTerminal(terminalID, Result{Outcome: OutcomeLaunchFailed, Cleanup: CleanupNotRequired}, now); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.Admit(runningID, "oci:"+strings.Repeat("2", 64), now.Add(time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.BindInstance(runningID, testInstanceRef(), now); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.MarkRunning(runningID, now); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.Admit(debtID, "oci:"+strings.Repeat("3", 64), now.Add(time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.BindInstance(debtID, testInstanceRef(), now); err != nil {
		t.Fatal(err)
	}
	debtExit := int64(1)
	if _, err = journal.MarkTerminal(debtID, Result{
		ExitCode: &debtExit, Outcome: OutcomeExited, Cleanup: CleanupFailed,
	}, now); err != nil {
		t.Fatal(err)
	}
	if err = journal.Prune(now.Add(2 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := journal.Get(terminalID); ok {
		t.Fatal("expired cleaned terminal record was retained")
	}
	if record, ok, err := journal.Get(runningID); err != nil || !ok || record.State != StateRunning {
		t.Fatalf("running record = %#v, %v, %v", record, ok, err)
	}
	if record, ok, err := journal.Get(debtID); err != nil || !ok || record.Result == nil || record.Result.Cleanup != CleanupFailed {
		t.Fatalf("cleanup debt record = %#v, %v, %v", record, ok, err)
	}
	if _, err = journal.MarkCleanup(debtID, CleanupComplete, now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	if err = journal.Prune(now.Add(3500 * time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := journal.Get(debtID); !ok {
		t.Fatal("cleanup completion retention was not restarted")
	}
	if err = journal.Prune(now.Add(5 * time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := journal.Get(debtID); ok {
		t.Fatal("completed cleanup debt record did not expire")
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	if err = waitForJournalLock(ctx, dir); err != nil {
		t.Fatalf("lock did not release: %v", err)
	}
}

func TestJournalRejectsInvalidTransitionsAndCapacity(t *testing.T) {
	dir := privateJournalDir(t)
	journal, err := OpenJournal(dir, JournalLimits{MaxRecords: 1, MaxRecordBytes: 128 << 10, MaxOutputBytes: 4096, Retention: time.Second})
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	now := time.Now().UTC()
	id := strings.Repeat("a", 32)
	if _, err = journal.Admit(id, "oci:"+strings.Repeat("b", 64), now.Add(time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.Admit(strings.Repeat("c", 32), "oci:"+strings.Repeat("d", 64), now.Add(time.Minute), now); !errors.Is(err, ErrJournalCapacity) {
		t.Fatalf("journal capacity error = %v", err)
	}
	if _, err = journal.MarkCleanup(id, CleanupComplete, now); err == nil {
		t.Fatal("cleanup before terminal accepted")
	}
	if _, err = journal.MarkRunning(id, now); err == nil {
		t.Fatal("running transition without exact instance binding accepted")
	}
	if _, err = journal.BindInstance(id, testInstanceRef(), now); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.BindInstance(id, "oci-instance-sha256:"+strings.Repeat("2", 64), now); err == nil {
		t.Fatal("job instance binding changed after being established")
	}
	exitCode := int64(0)
	if _, err = journal.MarkTerminal(id, Result{ExitCode: &exitCode, Outcome: OutcomeExited, Cleanup: CleanupPending, Output: Output{Text: strings.Repeat("x", 4097)}}, now); err == nil {
		t.Fatal("oversized terminal output accepted")
	}
}
