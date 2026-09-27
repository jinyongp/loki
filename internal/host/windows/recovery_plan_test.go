package windows

import (
	"reflect"
	"testing"
)

func TestBuildRecoveryPlanNeverCreatesDestructiveStepsWithoutAuthority(t *testing.T) {
	for name, snapshot := range map[string]ExistingSnapshot{
		"foreign":       {Distribution: DistributionState{State: DistributionForeign}},
		"indeterminate": {Distribution: DistributionState{State: DistributionIndeterminate}},
		"provisioning":  {Distribution: DistributionState{State: DistributionProvisioning}},
		"unverified-state": {
			Distribution: DistributionState{State: DistributionAbsent},
			Windows:      WindowsState{Present: true, Kind: WindowsStateUnverified},
		},
		"unverified-task": {
			Distribution: DistributionState{State: DistributionAbsent},
			StartupTask:  StartupTaskState{Present: true},
		},
		"stale-unapproved": {Distribution: DistributionState{State: DistributionStale}},
	} {
		t.Run(name, func(t *testing.T) {
			if plan := BuildRecoveryPlan(snapshot, false); len(plan.Steps) != 0 {
				t.Fatalf("destructive plan generated: %#v", plan)
			}
		})
	}
}

func TestBuildRecoveryPlanStaleApprovedOrder(t *testing.T) {
	snapshot := ExistingSnapshot{
		Distribution: DistributionState{State: DistributionStale},
		Windows:      WindowsState{Present: true, Owned: true, Kind: WindowsStateManifest},
		StartupTask:  StartupTaskState{Present: true, Owned: true},
	}
	got := BuildRecoveryPlan(snapshot, true)
	want := []RecoveryStep{
		RecoveryRemoveStartupTask,
		RecoveryTerminateDistro,
		RecoveryUnregisterDistro,
		RecoveryVerifyDistroGone,
		RecoveryRemoveWindowsState,
		RecoveryVerifyFresh,
	}
	if !reflect.DeepEqual(got.Steps, want) {
		t.Fatalf("steps=%#v want=%#v", got.Steps, want)
	}
}

func TestBuildRecoveryPlanOrphanDoesNotTouchWSL(t *testing.T) {
	snapshot := ExistingSnapshot{
		Distribution: DistributionState{State: DistributionAbsent},
		Windows:      WindowsState{Present: true, Owned: true, Kind: WindowsStateLegacy},
		StartupTask:  StartupTaskState{Present: true, Owned: true},
	}
	got := BuildRecoveryPlan(snapshot, false)
	want := []RecoveryStep{RecoveryRemoveStartupTask, RecoveryRemoveWindowsState, RecoveryVerifyFresh}
	if !reflect.DeepEqual(got.Steps, want) {
		t.Fatalf("steps=%#v want=%#v", got.Steps, want)
	}
}

func TestClassifyStartupTaskPreservesLegacyKeepaliveSignature(t *testing.T) {
	expected := fixtureExpected()
	probe := StartupTaskProbe{
		Present:     true,
		Description: "Keep the Loki WSL2 appliance running.",
		Actions: []StartupTaskAction{{
			Executable: `C:\WINDOWS\System32\wsl.exe`,
			Arguments:  expected.TaskArguments,
		}},
	}
	if state := ClassifyStartupTask(probe, expected); !state.Present || !state.Owned {
		t.Fatalf("expected owned task, got %#v", state)
	}
	probe.Actions[0].Arguments = "-d ubuntu --exec /usr/bin/sleep infinity"
	if state := ClassifyStartupTask(probe, expected); state.Owned {
		t.Fatal("foreign task action accepted")
	}
}
