package lifecycle

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func TestOperationErrorMessageBoundsDiagnosticWithoutLosingCause(t *testing.T) {
	for _, message := range []string{
		"short failure",
		"",
		"\x00invalid\xff text",
		"docker failed: " + strings.Repeat("진행 ", 2000) + "fatal: launcher flag not defined",
	} {
		got := operationErrorMessage(message)
		if got == "" || !validOperationError(got) {
			t.Fatalf("invalid bounded diagnostic len=%d: %q", len(got), got)
		}
		if len(message) > maxOperationErrorBytes &&
			(!strings.HasPrefix(got, "docker failed:") || !strings.HasSuffix(got, "fatal: launcher flag not defined")) {
			t.Fatalf("diagnostic lost operation or final cause: %q", got)
		}
	}
}

func TestOperationJournalRecordsRecoveryWithVerboseErrors(t *testing.T) {
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
	now := time.Now().UTC()
	record, err := journal.Begin(OperationApply, operationPlanFixture(t, now), now)
	if err != nil {
		t.Fatal(err)
	}
	record, err = journal.RecordSnapshot(record.ID, operationBackupID(), now.Add(time.Second))
	if err != nil {
		t.Fatal(err)
	}
	original := errors.New("docker failed: " + strings.Repeat("pulling layers\n", 6000) + "fatal: launcher cannot start")
	record, err = journal.BeginRecovery(record.ID, original, now.Add(2*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if record.State != OperationRecovering || !strings.HasSuffix(record.OriginalError, "fatal: launcher cannot start") {
		t.Fatalf("recovery record=%#v", record)
	}
	recovery := errors.New(strings.Repeat("restore progress\n", 6000) + "fatal: restore failed")
	record, err = journal.MarkRecoveryFailed(record.ID, recovery, now.Add(3*time.Second))
	if err != nil {
		t.Fatal(err)
	}
	if !record.Valid() || record.State != OperationRecoveryFailed || !strings.HasSuffix(record.RecoveryError, "fatal: restore failed") {
		t.Fatalf("recovery failure record=%#v", record)
	}
	if _, err = OpenOperationJournal(root, lock, OperationJournalOptions{}); err != nil {
		t.Fatalf("persisted diagnostics cannot be read: %v", err)
	}
}
