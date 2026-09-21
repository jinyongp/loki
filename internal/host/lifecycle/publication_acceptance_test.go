package lifecycle

import (
	"errors"
	"strings"
	"testing"
	"time"
)

type publicationStep struct {
	name          string
	state         OperationState
	phase         OperationPhase
	beforeState   *OperationState
	beforePhase   *OperationPhase
	afterTerminal bool
}

func TestOperationJournalPublicationCrashMatrixAcceptance(t *testing.T) {
	applying := OperationApplying
	admitted := PhaseAdmitted
	snapshot := PhaseSnapshot
	switchPhase := PhaseSwitch
	migrate := PhaseMigrate
	restart := PhaseRestart
	health := PhaseHealth
	steps := []publicationStep{
		{name: "admitted", state: OperationApplying, phase: PhaseAdmitted},
		{name: "snapshot", state: OperationApplying, phase: PhaseSnapshot, beforeState: &applying, beforePhase: &admitted},
		{name: "switch", state: OperationApplying, phase: PhaseSwitch, beforeState: &applying, beforePhase: &snapshot},
		{name: "migrate", state: OperationApplying, phase: PhaseMigrate, beforeState: &applying, beforePhase: &switchPhase},
		{name: "restart", state: OperationApplying, phase: PhaseRestart, beforeState: &applying, beforePhase: &migrate},
		{name: "health", state: OperationApplying, phase: PhaseHealth, beforeState: &applying, beforePhase: &restart},
		{name: "succeeded", state: OperationSucceeded, phase: PhaseComplete, beforeState: &applying, beforePhase: &health, afterTerminal: true},
	}
	for _, step := range steps {
		for _, timing := range []string{"before", "after"} {
			t.Run(step.name+"/"+timing, func(t *testing.T) {
				root := privateLifecycleRoot(t)
				lock, err := AcquireOperationLock(root)
				if err != nil {
					t.Fatal(err)
				}
				now := time.Date(2026, 9, 21, 14, 0, 0, 0, time.UTC)
				target := timing + "_publish:" + string(step.state) + ":" + string(step.phase)
				syntheticCrash := errors.New("synthetic publication crash")
				journal, err := OpenOperationJournal(root, lock, OperationJournalOptions{
					Failpoint: func(name string) error {
						if name == target {
							return syntheticCrash
						}
						return nil
					},
				})
				if err != nil {
					t.Fatal(err)
				}
				plan := operationPlanFixture(t, now)
				err = drivePublicationSequence(journal, plan, step, now)
				if !errors.Is(err, syntheticCrash) {
					t.Fatalf("publication failpoint %q error = %v", target, err)
				}
				if _, _, err = journal.Active(); err == nil || !strings.Contains(err.Error(), "requires reopen") {
					t.Fatalf("poisoned journal remained usable after %q: %v", target, err)
				}
				if err = lock.Close(); err != nil {
					t.Fatal(err)
				}

				reopenedLock, err := AcquireOperationLock(root)
				if err != nil {
					t.Fatal(err)
				}
				defer reopenedLock.Close()
				reopened, err := OpenOperationJournal(root, reopenedLock, OperationJournalOptions{})
				if err != nil {
					t.Fatal(err)
				}
				records, err := reopened.List()
				if err != nil {
					t.Fatal(err)
				}
				if timing == "before" && step.beforeState == nil {
					if len(records) != 0 {
						t.Fatalf("pre-prepare crash published a record: %#v", records)
					}
					if _, found, activeErr := reopened.Active(); activeErr != nil || found {
						t.Fatalf("pre-prepare active = found=%v err=%v", found, activeErr)
					}
					return
				}
				if len(records) != 1 {
					t.Fatalf("reopened records = %#v", records)
				}
				record := records[0]
				expectedState := step.state
				expectedPhase := step.phase
				expectedTerminal := step.afterTerminal
				if timing == "before" {
					expectedState = *step.beforeState
					expectedPhase = *step.beforePhase
					expectedTerminal = false
				}
				if record.State != expectedState || record.Phase != expectedPhase ||
					record.ActiveGenerationID != plan.ActiveGenerationID ||
					record.CandidateGenerationID != plan.CandidateGenerationID {
					t.Fatalf("reopened record after %q = %#v", target, record)
				}
				if record.State.Terminal() != expectedTerminal {
					t.Fatalf("terminal state after %q = %v, want %v", target, record.State.Terminal(), expectedTerminal)
				}
				active, found, err := reopened.Active()
				if err != nil {
					t.Fatal(err)
				}
				if expectedTerminal {
					if found {
						t.Fatalf("terminal publication remained active: %#v", active)
					}
					return
				}
				if !found || active.ID != record.ID || active.State != record.State || active.Phase != record.Phase {
					t.Fatalf("active operation after %q = %#v found=%v", target, active, found)
				}
			})
		}
	}
}

func drivePublicationSequence(journal *OperationJournal, plan PreparedPlan, target publicationStep, now time.Time) error {
	record, err := journal.Begin(OperationApply, plan, now)
	if err != nil {
		return err
	}
	if target.phase == PhaseAdmitted && target.state == OperationApplying {
		return errors.New("target publication unexpectedly succeeded")
	}
	record, err = journal.RecordSnapshot(record.ID, operationBackupID(), now.Add(time.Second))
	if err != nil {
		return err
	}
	if target.phase == PhaseSnapshot && target.state == OperationApplying {
		return errors.New("target publication unexpectedly succeeded")
	}
	for index, phase := range []OperationPhase{PhaseSwitch, PhaseMigrate, PhaseRestart, PhaseHealth} {
		record, err = journal.Advance(record.ID, phase, now.Add(time.Duration(index+2)*time.Second))
		if err != nil {
			return err
		}
		if target.phase == phase && target.state == OperationApplying {
			return errors.New("target publication unexpectedly succeeded")
		}
	}
	_, err = journal.MarkSucceeded(record.ID, now.Add(7*time.Second))
	return err
}
