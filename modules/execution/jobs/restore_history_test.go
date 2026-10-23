package jobs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRestorePreservesExpiredJobAndCompletedMaintenanceHistory(t *testing.T) {
	current := testJournal(t, privateJournalDir(t))
	stageRoot := privateJournalDir(t)
	stage := testJournal(t, stageRoot)
	now := time.Now().UTC()
	r := admitProtectedFixture(t, current, now)
	if _, err := current.MarkTerminal(r.ID, Result{Outcome: OutcomeLaunchFailed, Cleanup: CleanupNotRequired}, now); err != nil {
		t.Fatal(err)
	}
	if err := current.Prune(now.Add(31 * 24 * time.Hour)); err != nil {
		t.Fatal(err)
	}
	operation := "11111111-1111-4111-8111-111111111111"
	for _, journal := range []*Journal{current, stage} {
		if _, err := journal.BeginMaintenance(operation, false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := current.EndMaintenance(operation); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := stage.PreserveRequestHistory(current); err != nil {
			t.Fatal("preserve/retry latest history", err)
		}
	}
	if err := stage.Close(); err != nil {
		t.Fatal(err)
	}
	stage = testJournal(t, stageRoot)
	if _, err := os.Lstat(stage.recordPath(r.ID)); !os.IsNotExist(err) {
		t.Fatal("history preservation restored a Job execution record", err)
	}
	retry := now.Add(32 * 24 * time.Hour)
	if _, _, err := stage.AdmitProtectedRequest(r.ID, r.BackendRef, r.RequestID, r.RequestSHA256, r.Network, nil, nil, r.Protected.DeploymentID, "fixture", "common", retry.Add(time.Minute), retry); !errors.Is(err, ErrRequestExpired) {
		t.Fatal("restore forgot an expired one-attempt request", err)
	}
	if result, err := stage.BeginMaintenance(operation, false); err != nil || result.Active {
		t.Fatal("restore reopened an older active host operation", err)
	}
	if err := stage.AdmissionAllowed(); err != nil {
		t.Fatal("completed maintenance remained active", err)
	}
}

func TestRestoreHistoryRejectsConflictingProtectedIdentityBeforeStageWrites(t *testing.T) {
	for _, changed := range []string{"request digest", "deployment"} {
		t.Run(changed, func(t *testing.T) {
			current := testJournal(t, privateJournalDir(t))
			stage := testJournal(t, privateJournalDir(t))
			now := time.Now().UTC()
			b := admitProtectedFixture(t, stage, now)
			digest, deployment := b.RequestSHA256, b.Protected.DeploymentID
			if changed == "request digest" {
				digest = strings.Repeat("f", 64)
			} else {
				deployment = "deployment-" + strings.Repeat("f", 32)
			}
			a, _, err := current.AdmitProtectedRequest(b.ID, b.BackendRef, b.RequestID, digest, b.Network, nil, nil, deployment, "fixture", "common", now.Add(time.Minute), now)
			if err != nil {
				t.Fatal(err)
			}
			if _, err := current.MarkTerminal(a.ID, Result{Outcome: OutcomeLaunchFailed, Cleanup: CleanupNotRequired}, now); err != nil {
				t.Fatal(err)
			}
			if _, err := stage.MarkTerminal(b.ID, Result{Outcome: OutcomeLaunchFailed, Cleanup: CleanupNotRequired}, now); err != nil {
				t.Fatal(err)
			}
			path := filepath.Join(stage.dir, "requests", a.ID+".json")
			before, err := os.ReadFile(path)
			if err != nil {
				t.Fatal(err)
			}
			if err := stage.PreserveRequestHistory(current); !errors.Is(err, ErrReplayConflict) {
				t.Fatal("restore accepted a changed original request identity", err)
			}
			after, err := os.ReadFile(path)
			if err != nil || string(before) != string(after) {
				t.Fatal("conflicting history modified the restore stage", err)
			}
		})
	}
}

func TestRestoreHistoryPreservesCurrentFenceAndRejectsUnrelatedPendingBackup(t *testing.T) {
	for _, completed := range []bool{false, true} {
		t.Run(map[bool]string{false: "conflicting pending backup", true: "completed backup fence"}[completed], func(t *testing.T) {
			current := testJournal(t, privateJournalDir(t))
			stage := testJournal(t, privateJournalDir(t))
			old, active := "11111111-1111-4111-8111-111111111111", "22222222-2222-4222-8222-222222222222"
			if _, err := stage.BeginMaintenance(old, false); err != nil {
				t.Fatal(err)
			}
			if completed {
				if _, err := current.BeginMaintenance(old, false); err != nil {
					t.Fatal(err)
				}
				if _, err := current.EndMaintenance(old); err != nil {
					t.Fatal(err)
				}
			}
			if _, err := current.BeginMaintenance(active, false); err != nil {
				t.Fatal(err)
			}
			err := stage.PreserveRequestHistory(current)
			if !completed {
				if err == nil {
					t.Fatal("restore discarded an unresolved original admission fence")
				}
				return
			}
			if err != nil {
				t.Fatal(err)
			}
			if err := stage.AdmissionAllowed(); !errors.Is(err, ErrMaintenanceBusy) {
				t.Fatal("restore dropped current maintenance", err)
			}
			if result, err := stage.BeginMaintenance(active, false); err != nil || !result.Active {
				t.Fatal("restore changed original active operation", err)
			}
		})
	}
}

func TestMergeOlderHistoryRetainsCurrentJournalAndOriginalAdmission(t *testing.T) {
	current := testJournal(t, privateJournalDir(t))
	older := testJournal(t, privateJournalDir(t))
	now := time.Now().UTC()
	r := admitProtectedFixture(t, older, now)
	if _, err := older.MarkTerminal(r.ID, Result{Outcome: OutcomeLaunchFailed, Cleanup: CleanupNotRequired}, now); err != nil {
		t.Fatal(err)
	}
	op := "22222222-2222-4222-8222-222222222222"
	if _, err := current.BeginMaintenance(op, false); err != nil {
		t.Fatal(err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		if err := current.MergeHistoricalRequestHistory(older); err != nil {
			t.Fatal(err)
		}
		if maintenance, err := current.MaintenanceStatus(); err != nil || maintenance == nil || !maintenance.Active || maintenance.OperationID != op {
			t.Fatal("older history replaced current admission", err)
		}
	}
	if _, err := os.Lstat(current.recordPath(r.ID)); !os.IsNotExist(err) {
		t.Fatal("historical execution record restored", err)
	}
	if _, err := current.EndMaintenance(op); err != nil {
		t.Fatal(err)
	}
	if _, _, err := current.AdmitProtectedRequest(r.ID, r.BackendRef, r.RequestID, r.RequestSHA256, r.Network, nil, nil, r.Protected.DeploymentID, "fixture", "common", now.Add(time.Minute), now); !errors.Is(err, ErrDeliveryUnknown) {
		t.Fatal("historical request became executable", err)
	}
}
