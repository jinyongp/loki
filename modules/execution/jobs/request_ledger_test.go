package jobs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loki/internal/platform/privateio"
)

func admitProtectedFixture(t *testing.T, j *Journal, now time.Time) Record {
	t.Helper()
	id, _ := JobIDForRequestID(testRequestID())
	r, replayed, err := j.AdmitProtectedRequest(id, "oci:"+strings.Repeat("c", 64)+":"+strings.Repeat("d", 64), testRequestID(), strings.Repeat("e", 64), NetworkNone, nil, nil, "deployment-"+strings.Repeat("b", 32), "fixture", "common", now.Add(time.Minute), now)
	if err != nil || replayed {
		t.Fatalf("protected admission: %v %v", replayed, err)
	}
	return r
}

func prepareProtectedFixture(t *testing.T, j *Journal, r Record, now time.Time) {
	t.Helper()
	if _, err := j.BindInstance(r.ID, testInstanceRef(), now); err != nil {
		t.Fatal(err)
	}
	p := privateio.PreparedJob{Stage: "prepared", ExecutionID: strings.Repeat("f", 32), InputID: strings.Repeat("0", 32), RunnerID: testRequestID(), RunnerInstanceID: testRequestID(), Revision: 1, PrepareRequestID: privateio.OperationID(r.Protected.DeploymentID, r.ID, "prepare"), ApproveRequestID: privateio.OperationID(r.Protected.DeploymentID, r.ID, "approve"), DeliverRequestID: privateio.OperationID(r.Protected.DeploymentID, r.ID, "deliver")}
	if _, err := j.BindProtectedInput(r.ID, p, now); err != nil {
		t.Fatal(err)
	}
}

func TestProtectedRequestNeverReexecutesAfterRecordExpiry(t *testing.T) {
	dir := privateJournalDir(t)
	j := testJournal(t, dir)
	now := time.Now().UTC()
	r := admitProtectedFixture(t, j, now)
	if _, err := j.MarkTerminal(r.ID, Result{Outcome: OutcomeLaunchFailed, Cleanup: CleanupNotRequired}, now); err != nil {
		t.Fatal(err)
	}
	if err := j.Prune(now.Add(31 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	if _, ok, err := j.Get(r.ID); err != nil || ok {
		t.Fatal("expired detail retained", err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j = testJournal(t, dir)
	_, replay, err := j.AdmitProtectedRequest(r.ID, r.BackendRef, r.RequestID, r.RequestSHA256, r.Network, nil, nil, r.Protected.DeploymentID, "fixture", "common", now.Add(32*24*time.Hour+time.Minute), now.Add(32*24*time.Hour))
	if !errors.Is(err, ErrRequestExpired) || replay {
		t.Fatalf("spent request reused: %v %v", replay, err)
	}
	if _, err := os.Stat(filepath.Join(dir, "requests", r.ID+".json")); err != nil {
		t.Fatal("tombstone removed", err)
	}
}

func TestUncertainProtectedAdmissionCannotCreateAnotherJob(t *testing.T) {
	j := testJournal(t, privateJournalDir(t))
	now := time.Now().UTC()
	r := admitProtectedFixture(t, j, now)
	// A reservation survives even if publication/response is lost. Remove the
	// detail as a crash boundary; the retained reservation must fence execution.
	if err := os.Remove(j.recordPath(r.ID)); err != nil {
		t.Fatal(err)
	}
	delete(j.records, r.ID)
	_, _, err := j.AdmitProtectedRequest(r.ID, r.BackendRef, r.RequestID, r.RequestSHA256, r.Network, nil, nil, r.Protected.DeploymentID, "fixture", "common", now.Add(time.Minute), now)
	if !errors.Is(err, ErrDeliveryUnknown) {
		t.Fatal("uncertain admission replayed", err)
	}
	j.spentBytes = protectedLedgerBudget
	otherRequest := "a7e465bb-4c61-4f86-acbe-cac830414f48"
	otherID, _ := JobIDForRequestID(otherRequest)
	_, _, err = j.AdmitProtectedRequest(otherID, r.BackendRef, otherRequest, r.RequestSHA256, r.Network, nil, nil, r.Protected.DeploymentID, "fixture", "common", now.Add(time.Minute), now)
	if !errors.Is(err, ErrJournalCapacity) {
		t.Fatal("spent budget exceeded", err)
	}
}

func TestProtectedOutputGapCancelAndIndependentRetention(t *testing.T) {
	j := testJournal(t, privateJournalDir(t))
	now := time.Now().UTC()
	r := admitProtectedFixture(t, j, now)
	prepareProtectedFixture(t, j, r, now)
	if err := j.AppendMaskedOutput(r.ID, []byte("durable safe prefix"), now); err != nil {
		t.Fatal(err)
	}
	if err := j.RequestProtectedCancel(r.ID, now); err != nil {
		t.Fatal(err)
	}
	if err := j.MarkOutputLost(r.ID, "collector_lost", now); err != nil {
		t.Fatal(err)
	}
	r, err := j.MarkTerminal(r.ID, Result{Outcome: OutcomeOutputLost, Cleanup: CleanupComplete}, now)
	if err != nil || r.Result.Outcome != OutcomeCanceled || r.Result.ExitCode != nil || r.Protected.Gap.Cursor != 19 || r.Protected.Complete {
		t.Fatal("lost output or durable cancel misreported", err)
	}
	if err := j.Prune(now.Add(2 * time.Minute)); err != nil {
		t.Fatal(err)
	}
	r, ok, err := j.Get(r.ID)
	if err != nil || !ok || !r.Protected.OutputExpired || len(r.Protected.Output) != 0 || r.Protected.Cursor != 19 {
		t.Fatal("output/detail retention coupled", err)
	}
	if _, err := OutputFromRecord(r); !errors.Is(err, ErrOutputExpired) {
		t.Fatal("expired output returned", err)
	}
	status, err := StatusFromRecord(r)
	if err != nil || status.OutputComplete || status.OutputGap == nil || status.OutputGap.Cursor != 19 {
		t.Fatal("gap unavailable after expiry", err)
	}
}

func TestExpiredProtectedCleanupCanFinishWithoutReusingRequest(t *testing.T) {
	j := testJournal(t, privateJournalDir(t))
	now := time.Now().UTC()
	r := admitProtectedFixture(t, j, now)
	prepareProtectedFixture(t, j, r, now)
	if _, err := j.MarkTerminal(r.ID, Result{Outcome: OutcomeOutputLost, Cleanup: CleanupFailed}, now); err != nil {
		t.Fatal(err)
	}
	future := now.Add(32 * 24 * time.Hour)
	if _, err := j.MarkCleanup(r.ID, CleanupComplete, future); err != nil {
		t.Fatal(err)
	}
	if err := j.Prune(future.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, ok, _ := j.Get(r.ID); ok {
		t.Fatal("expired detail revived by cleanup")
	}
	if _, ok := j.spent[r.ID]; !ok {
		t.Fatal("cleanup removed root-lifetime reservation")
	}
}
