package lifecycle

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"
)

func operationPlanFixture(t *testing.T, now time.Time) PreparedPlan {
	t.Helper()
	active := generationFixture(t, "1.0.0", now.Add(-48*time.Hour), 1)
	candidate := generationFixture(t, "1.1.0", now.Add(-time.Hour), 1)
	plan, err := Prepare(&active, candidate, hostFixture(active, 1), now)
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func operationBackupID() string {
	return "sha256:" + strings.Repeat("f", 64)
}

func TestOperationLockIsExclusiveAndRecoverable(t *testing.T) {
	root := privateLifecycleRoot(t)
	first, err := AcquireOperationLock(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = AcquireOperationLock(root); err == nil || !strings.Contains(err.Error(), "owns the lock") {
		t.Fatalf("contended operation lock error = %v", err)
	}
	if err = first.Close(); err != nil {
		t.Fatal(err)
	}
	second, err := AcquireOperationLock(root)
	if err != nil {
		t.Fatal(err)
	}
	if err = second.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestOperationJournalEnforcesApplyTransitionOrder(t *testing.T) {
	root := privateLifecycleRoot(t)
	lock, err := AcquireOperationLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	journal, err := OpenOperationJournal(root, lock, OperationJournalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	record, err := journal.Begin(OperationApply, operationPlanFixture(t, now), now)
	if err != nil {
		t.Fatal(err)
	}
	if record.State != OperationApplying || record.Phase != PhaseAdmitted {
		t.Fatalf("begin = %#v", record)
	}
	if _, err = journal.Advance(record.ID, PhaseSwitch, now.Add(time.Second)); err == nil {
		t.Fatal("operation skipped snapshot phase")
	}
	if _, err = journal.Advance(record.ID, PhaseSnapshot, now.Add(time.Second)); err == nil {
		t.Fatal("snapshot phase advanced without a recovery backup")
	}
	record, err = journal.RecordSnapshot(record.ID, operationBackupID(), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	for index, phase := range []OperationPhase{PhaseSwitch, PhaseMigrate, PhaseRestart, PhaseHealth} {
		record, err = journal.Advance(record.ID, phase, now.Add(time.Duration(index+2)*time.Second))
		if err != nil {
			t.Fatal(err)
		}
	}
	record, err = journal.MarkSucceeded(record.ID, now.Add(10*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if record.State != OperationSucceeded || record.Phase != PhaseComplete || !record.Valid() {
		t.Fatalf("completed operation = %#v", record)
	}
	if _, found, err := journal.Active(); err != nil || found {
		t.Fatalf("active after success = %v, %v", found, err)
	}
}

func TestOperationJournalRecoversPublishedTransitionAfterFailpoint(t *testing.T) {
	root := privateLifecycleRoot(t)
	lock, err := AcquireOperationLock(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	syntheticCrash := errors.New("synthetic process loss")
	journal, err := OpenOperationJournal(root, lock, OperationJournalOptions{
		Failpoint: func(name string) error {
			if name == "after_publish:applying:switch" {
				return syntheticCrash
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err := journal.Begin(OperationApply, operationPlanFixture(t, now), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = journal.RecordSnapshot(record.ID, operationBackupID(), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.Advance(record.ID, PhaseSwitch, now.Add(2*time.Second)); !errors.Is(err, syntheticCrash) {
		t.Fatalf("failpoint error = %v", err)
	}
	if _, _, err = journal.Active(); err == nil || !strings.Contains(err.Error(), "requires reopen") {
		t.Fatalf("poisoned journal remained usable: %v", err)
	}
	if err = lock.Close(); err != nil {
		t.Fatal(err)
	}

	recoveredLock, err := AcquireOperationLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer recoveredLock.Close()
	recoveredJournal, err := OpenOperationJournal(root, recoveredLock, OperationJournalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	active, found, err := recoveredJournal.Active()
	if err != nil || !found {
		t.Fatalf("recovered active operation = %#v found=%v err=%v", active, found, err)
	}
	if active.ID != record.ID || active.State != OperationApplying || active.Phase != PhaseSwitch ||
		active.ActiveGenerationID != record.ActiveGenerationID ||
		active.CandidateGenerationID != record.CandidateGenerationID {
		t.Fatalf("recovered operation = %#v", active)
	}
	active, err = recoveredJournal.BeginRecovery(active.ID, errors.New("operation interrupted during switch"), now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if active.State != OperationRecovering || active.Phase != PhaseRollback ||
		!strings.Contains(active.OriginalError, "interrupted") {
		t.Fatalf("recovery record = %#v", active)
	}
	active, err = recoveredJournal.MarkRolledBack(active.ID, now.Add(4*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if active.State != OperationRolledBack || !active.State.Terminal() ||
		active.OriginalError == "" || active.RecoveryError != "" {
		t.Fatalf("rolled back operation = %#v", active)
	}
}

func TestOperationJournalPreservesOriginalAndRecoveryErrors(t *testing.T) {
	root := privateLifecycleRoot(t)
	lock, err := AcquireOperationLock(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	journal, err := OpenOperationJournal(root, lock, OperationJournalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	record, err := journal.Begin(OperationApply, operationPlanFixture(t, now), now)
	if err != nil {
		t.Fatal(err)
	}
	record, err = journal.BeginRecovery(record.ID, errors.New("candidate health validation failed"), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	record, err = journal.MarkRecoveryFailed(record.ID, errors.New("previous release failed to restore"), now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if record.State != OperationRecoveryFailed || record.OriginalError != "candidate health validation failed" ||
		record.RecoveryError != "previous release failed to restore" || !record.Valid() {
		t.Fatalf("recovery_failed record = %#v", record)
	}
	if err = lock.Close(); err != nil {
		t.Fatal(err)
	}

	lock, err = AcquireOperationLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	journal, err = OpenOperationJournal(root, lock, OperationJournalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	records, err := journal.List()
	if err != nil || len(records) != 1 || records[0].State != OperationRecoveryFailed ||
		records[0].OriginalError == "" || records[0].RecoveryError == "" {
		t.Fatalf("reopened recovery_failed records = %#v, %v", records, err)
	}
}

func TestOperationJournalRejectsConcurrentActiveOperation(t *testing.T) {
	root := privateLifecycleRoot(t)
	lock, err := AcquireOperationLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	journal, err := OpenOperationJournal(root, lock, OperationJournalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	if _, err = journal.Begin(OperationApply, operationPlanFixture(t, now), now); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.Begin(OperationApply, operationPlanFixture(t, now), now.Add(time.Second)); err == nil {
		t.Fatal("second active operation was admitted")
	}
}

func TestOperationJournalBeforePublishCrashKeepsPreviousCheckpoint(t *testing.T) {
	root := privateLifecycleRoot(t)
	lock, err := AcquireOperationLock(root)
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	syntheticCrash := errors.New("synthetic crash before publication")
	journal, err := OpenOperationJournal(root, lock, OperationJournalOptions{
		Failpoint: func(name string) error {
			if name == "before_publish:applying:switch" {
				return syntheticCrash
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	record, err := journal.Begin(OperationApply, operationPlanFixture(t, now), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = journal.RecordSnapshot(record.ID, operationBackupID(), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.Advance(record.ID, PhaseSwitch, now.Add(2*time.Second)); !errors.Is(err, syntheticCrash) {
		t.Fatalf("failpoint error = %v", err)
	}
	if err = lock.Close(); err != nil {
		t.Fatal(err)
	}

	lock, err = AcquireOperationLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	journal, err = OpenOperationJournal(root, lock, OperationJournalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	active, found, err := journal.Active()
	if err != nil || !found {
		t.Fatalf("active = %#v found=%v err=%v", active, found, err)
	}
	if active.Phase != PhaseSnapshot {
		t.Fatalf("phase after pre-publication crash = %q", active.Phase)
	}
}

func TestOperationJournalRecoversInterruptedOperation(t *testing.T) {
	root := privateLifecycleRoot(t)
	lock, err := AcquireOperationLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	journal, err := OpenOperationJournal(root, lock, OperationJournalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	record, err := journal.Begin(OperationApply, operationPlanFixture(t, now), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = journal.RecordSnapshot(record.ID, operationBackupID(), now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	var recovered OperationRecord
	record, found, err := journal.RecoverInterrupted(context.Background(), func(_ context.Context, active OperationRecord) error {
		recovered = active
		return nil
	}, now.Add(2*time.Second))
	if err != nil || !found {
		t.Fatalf("recover interrupted = %#v found=%v err=%v", record, found, err)
	}
	if recovered.State != OperationRecovering || recovered.Phase != PhaseRollback ||
		!strings.Contains(recovered.OriginalError, string(PhaseSnapshot)) {
		t.Fatalf("recovery handler record = %#v", recovered)
	}
	if record.State != OperationRolledBack || record.OriginalError == "" || record.RecoveryError != "" {
		t.Fatalf("recovered record = %#v", record)
	}
}

func TestOperationJournalReportsRecoveryFailureWithBothErrors(t *testing.T) {
	root := privateLifecycleRoot(t)
	lock, err := AcquireOperationLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	journal, err := OpenOperationJournal(root, lock, OperationJournalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	record, err := journal.Begin(OperationApply, operationPlanFixture(t, now), now)
	if err != nil {
		t.Fatal(err)
	}
	recoveryErr := errors.New("rollback restore failed")
	record, found, err := journal.RecoverInterrupted(context.Background(), func(context.Context, OperationRecord) error {
		return recoveryErr
	}, now.Add(time.Second))
	if !found {
		t.Fatal("interrupted operation was not found")
	}
	var failed *RecoveryFailedError
	if !errors.As(err, &failed) || failed.Original == "" || failed.Recovery != recoveryErr.Error() {
		t.Fatalf("recovery error = %#v", err)
	}
	if record.State != OperationRecoveryFailed || record.OriginalError == "" || record.RecoveryError != recoveryErr.Error() {
		t.Fatalf("recovery_failed record = %#v", record)
	}
}

func TestOperationJournalClampsBackwardWallClockTransition(t *testing.T) {
	root := privateLifecycleRoot(t)
	lock, err := AcquireOperationLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	journal, err := OpenOperationJournal(root, lock, OperationJournalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Date(2026, 9, 21, 8, 0, 0, 0, time.UTC)
	record, err := journal.Begin(OperationApply, operationPlanFixture(t, now), now)
	if err != nil {
		t.Fatal(err)
	}
	advanced, err := journal.RecordSnapshot(record.ID, operationBackupID(), now.Add(-time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if advanced.UpdatedAt != record.UpdatedAt {
		t.Fatalf("backward wall clock changed update time: %q != %q", advanced.UpdatedAt, record.UpdatedAt)
	}
}
