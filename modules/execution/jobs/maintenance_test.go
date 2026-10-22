package jobs

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestMaintenanceFencesAdmissionAcrossRestartUntilExactCleanup(t *testing.T) {
	dir := privateJournalDir(t)
	j := testJournal(t, dir)
	now := time.Now().UTC()
	id, backend := strings.Repeat("a", 32), "oci:"+strings.Repeat("b", 64)
	if _, err := j.Admit(id, backend, now.Add(time.Minute), now); err != nil {
		t.Fatal(err)
	}
	op := "11111111-1111-4111-8111-111111111111"
	if _, err := j.BeginMaintenance(op, false); !errors.Is(err, ErrJobsBusy) {
		t.Fatalf("busy default: %v", err)
	}
	if err := j.AdmissionAllowed(); err != nil {
		t.Fatal("busy default changed admission", err)
	}
	if _, err := j.BeginMaintenance(op, true); err != nil {
		t.Fatal(err)
	}
	if _, err := j.Admit(strings.Repeat("c", 32), backend, now.Add(time.Minute), now); !errors.Is(err, ErrMaintenanceBusy) {
		t.Fatalf("admission escaped fence: %v", err)
	}
	if _, err := j.EndMaintenance(op); !errors.Is(err, ErrJobsBusy) {
		t.Fatalf("ended before cleanup: %v", err)
	}
	if _, err := ReadJournalSnapshot(dir, JournalLimits{}); err != nil {
		t.Fatal("host inventory rejected maintenance metadata", err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j = testJournal(t, dir)
	if err := j.AdmissionAllowed(); !errors.Is(err, ErrMaintenanceBusy) {
		t.Fatalf("restart dropped fence: %v", err)
	}
	if _, err := j.BeginMaintenance(op, false); !errors.Is(err, ErrReplayConflict) {
		t.Fatalf("changed retry accepted: %v", err)
	}
	if _, err := j.BeginMaintenance("22222222-2222-4222-8222-222222222222", true); !errors.Is(err, ErrMaintenanceBusy) {
		t.Fatalf("new operation replaced fence: %v", err)
	}
	if _, err := j.MarkTerminal(id, Result{Outcome: OutcomeCanceled, Cleanup: CleanupFailed}, now); err != nil {
		t.Fatal(err)
	}
	if _, err := j.EndMaintenance(op); !errors.Is(err, ErrJobsBusy) {
		t.Fatalf("failed cleanup released fence: %v", err)
	}
	if _, err := j.MarkCleanup(id, CleanupComplete, now); err != nil {
		t.Fatal(err)
	}
	if _, err := j.EndMaintenance("22222222-2222-4222-8222-222222222222"); !errors.Is(err, ErrMaintenanceBusy) {
		t.Fatal("wrong operation released fence", err)
	}
	if state, err := j.EndMaintenance(op); err != nil || state.Active {
		t.Fatalf("end: %+v %v", state, err)
	}
	if state, err := j.BeginMaintenance(op, true); err != nil || state.Active {
		t.Fatalf("completed retry reactivated: %+v %v", state, err)
	}
	if err := j.AdmissionAllowed(); err != nil {
		t.Fatal(err)
	}
}

func TestMaintenanceRejectsUnsafeMetadata(t *testing.T) {
	for _, kind := range []string{"symlink", "public", "duplicate", "null", "oversize"} {
		t.Run(kind, func(t *testing.T) {
			dir := privateJournalDir(t)
			path := filepath.Join(dir, maintenanceFile)
			raw := `{"version":1,"operation_id":"11111111-1111-4111-8111-111111111111","interrupt":false,"active":true}`
			switch kind {
			case "symlink":
				if err := os.Symlink("/dev/null", path); err != nil {
					t.Fatal(err)
				}
			case "duplicate":
				raw = strings.Replace(raw, `"active":true`, `"active":false,"active":true`, 1)
			case "null":
				raw = strings.Replace(raw, `"active":true`, `"active":null`, 1)
			case "oversize":
				raw = strings.Repeat(" ", 4097)
			}
			if kind != "symlink" {
				if err := os.WriteFile(path, []byte(raw), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if kind == "public" {
				if err := os.Chmod(path, 0644); err != nil {
					t.Fatal(err)
				}
			}
			if j, err := OpenJournal(dir, JournalLimits{}); err == nil {
				j.Close()
				t.Fatal("unsafe metadata accepted")
			}
		})
	}
}

func TestMaintenanceHistorySurvivesNewOperationsAndCrashWindows(t *testing.T) {
	dir := privateJournalDir(t)
	j := testJournal(t, dir)
	first := "11111111-1111-4111-8111-111111111111"
	second := "22222222-2222-4222-8222-222222222222"
	third := "33333333-3333-4333-8333-333333333333"
	for _, id := range []string{first, second} {
		if _, err := j.BeginMaintenance(id, false); err != nil {
			t.Fatal(err)
		}
		if _, err := j.EndMaintenance(id); err != nil {
			t.Fatal(err)
		}
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j = testJournal(t, dir)
	if result, err := j.BeginMaintenance(first, false); err != nil || result.Active {
		t.Fatalf("old startup layout reopened admission fence: %+v %v", result, err)
	}
	if _, err := j.BeginMaintenance(first, true); !errors.Is(err, ErrReplayConflict) {
		t.Fatal("historical operation accepted a changed interruption choice", err)
	}
	if err := j.AdmissionAllowed(); err != nil {
		t.Fatal(err)
	}
	// Crash after the durable admission reservation but before current-fence
	// publication. The same request ledger must restore the pending fence.
	value := Maintenance{Version: 1, OperationID: third, Active: true}
	if err := j.reserveMaintenanceRequest(value); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j = testJournal(t, dir)
	if err := j.AdmissionAllowed(); !errors.Is(err, ErrMaintenanceBusy) {
		t.Fatal("crash before current-fence publication lost admission intent", err)
	}
	if _, err := j.EndMaintenance(first); err != nil {
		t.Fatal(err)
	}
	if result, err := j.BeginMaintenance(second, false); err != nil || result.Active {
		t.Fatalf("historical replay replaced current operation: %+v %v", result, err)
	}
	state, err := j.MaintenanceStatus()
	if err != nil || state == nil || state.OperationID != third || !state.Active {
		t.Fatal("old ACK changed the pending operation", state, err)
	}
	// Completion was durable, but the current-fence publication/ACK was lost.
	value.Active = false
	if err := j.reserveMaintenanceRequest(value); err != nil {
		t.Fatal(err)
	}
	if err := j.Close(); err != nil {
		t.Fatal(err)
	}
	j = testJournal(t, dir)
	if err := j.AdmissionAllowed(); err != nil {
		t.Fatal("lost release ACK kept a spent operation active", err)
	}
}

func TestMaintenanceReservesTerminalHistoryBeforeAnyFence(t *testing.T) {
	j := testJournal(t, privateJournalDir(t))
	id := "11111111-1111-4111-8111-111111111111"
	j.spentBytes = protectedLedgerBudget
	if _, err := j.BeginMaintenance(id, false); !errors.Is(err, ErrJournalCapacity) {
		t.Fatal("full root ledger admitted a new maintenance identity", err)
	}
	if err := j.AdmissionAllowed(); err != nil {
		t.Fatal("capacity rejection changed admission", err)
	}
	j.spentBytes = 0
	if _, err := j.BeginMaintenance(id, false); err != nil {
		t.Fatal(err)
	}
	j.spentBytes = protectedLedgerBudget
	if _, err := j.EndMaintenance(id); err != nil {
		t.Fatal("admitted operation could not use its reserved completion space", err)
	}
}
