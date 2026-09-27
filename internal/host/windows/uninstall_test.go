package windows

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type fakeUninstallPlatform struct {
	snapshots []ExistingSnapshot
	steps     []string
	errStep   string
}

func (platform *fakeUninstallPlatform) Collect(context.Context, ExpectedInstallation) (ExistingSnapshot, error) {
	platform.steps = append(platform.steps, "collect")
	if platform.errStep == "collect" {
		return ExistingSnapshot{}, errors.New("collect failed")
	}
	if len(platform.snapshots) == 0 {
		return ExistingSnapshot{}, errors.New("no snapshot")
	}
	snapshot := platform.snapshots[0]
	if len(platform.snapshots) > 1 {
		platform.snapshots = platform.snapshots[1:]
	}
	return snapshot, nil
}

func (platform *fakeUninstallPlatform) RemoveConnections(context.Context, string) error {
	platform.steps = append(platform.steps, "connections")
	if platform.errStep == "connections" {
		return errors.New("connections failed")
	}
	return nil
}

func (platform *fakeUninstallPlatform) RemoveStartupTask(context.Context, ExpectedInstallation) error {
	platform.steps = append(platform.steps, "task")
	return nil
}

func (platform *fakeUninstallPlatform) VerifyDistributionIdentity(context.Context, string, string) error {
	platform.steps = append(platform.steps, "verify")
	if platform.errStep == "verify" {
		return errors.New("verify failed")
	}
	return nil
}

func (platform *fakeUninstallPlatform) TerminateDistribution(context.Context, string) error {
	platform.steps = append(platform.steps, "terminate")
	return nil
}

func (platform *fakeUninstallPlatform) UnregisterDistribution(context.Context, string) error {
	platform.steps = append(platform.steps, "unregister")
	return nil
}

func (platform *fakeUninstallPlatform) DistributionPresent(context.Context, string) (bool, error) {
	platform.steps = append(platform.steps, "present")
	return false, nil
}

func (platform *fakeUninstallPlatform) RemoveWindowsState(ExpectedInstallation, WindowsState) error {
	platform.steps = append(platform.steps, "state")
	return nil
}

func ownedUninstallSnapshot() ExistingSnapshot {
	return ExistingSnapshot{
		Distribution: DistributionState{State: DistributionHealthy, Version: "1.2.3"},
		Windows: WindowsState{
			Present: true, Owned: true, Kind: WindowsStateManifest,
			MCPPort: 18765, AutoStart: true, AutoStartKnown: true,
		},
		StartupTask: StartupTaskState{Present: true, Owned: true},
	}
}

func TestUninstallRequiresApprovalAndUsesSafeOrder(t *testing.T) {
	expected := ExpectedInstallation{Distribution: "loki-mcp"}
	owned := ownedUninstallSnapshot()
	absent := ExistingSnapshot{Distribution: DistributionState{State: DistributionAbsent}}
	platform := &fakeUninstallPlatform{snapshots: []ExistingSnapshot{owned, owned, absent}}
	controller := UninstallController{Platform: platform}

	if err := controller.Run(t.Context(), expected, false); err == nil {
		t.Fatal("unapproved uninstall accepted")
	}
	if len(platform.steps) != 0 {
		t.Fatalf("unapproved uninstall mutated/inspected state: %v", platform.steps)
	}

	if err := controller.Run(t.Context(), expected, true); err != nil {
		t.Fatal(err)
	}
	want := []string{
		"collect", "collect", "connections", "task", "verify", "terminate", "verify",
		"unregister", "present", "state", "collect",
	}
	if !reflect.DeepEqual(platform.steps, want) {
		t.Fatalf("steps=%v want=%v", platform.steps, want)
	}
}

func TestUninstallRefusesUnverifiedOrChangedStateBeforeMutation(t *testing.T) {
	expected := ExpectedInstallation{Distribution: "loki-mcp"}
	t.Run("unverified", func(t *testing.T) {
		snapshot := ownedUninstallSnapshot()
		snapshot.Windows.Owned = false
		platform := &fakeUninstallPlatform{snapshots: []ExistingSnapshot{snapshot}}
		err := (UninstallController{Platform: platform}).Run(t.Context(), expected, true)
		if err == nil || !strings.Contains(err.Error(), "unverified") {
			t.Fatalf("err=%v", err)
		}
		if !reflect.DeepEqual(platform.steps, []string{"collect"}) {
			t.Fatalf("steps=%v", platform.steps)
		}
	})
	t.Run("changed", func(t *testing.T) {
		first := ownedUninstallSnapshot()
		second := ownedUninstallSnapshot()
		second.Distribution.Version = "1.2.4"
		platform := &fakeUninstallPlatform{snapshots: []ExistingSnapshot{first, second}}
		err := (UninstallController{Platform: platform}).Run(t.Context(), expected, true)
		if err == nil || !strings.Contains(err.Error(), "changed after approval") {
			t.Fatalf("err=%v", err)
		}
		if !reflect.DeepEqual(platform.steps, []string{"collect", "collect"}) {
			t.Fatalf("steps=%v", platform.steps)
		}
	})
}

func TestUninstallRefusesProvisioningAndForeignDistribution(t *testing.T) {
	expected := ExpectedInstallation{Distribution: "loki-mcp"}
	for _, state := range []DistributionStateKind{DistributionProvisioning, DistributionForeign, DistributionIndeterminate} {
		t.Run(string(state), func(t *testing.T) {
			snapshot := ownedUninstallSnapshot()
			snapshot.Distribution.State = state
			platform := &fakeUninstallPlatform{snapshots: []ExistingSnapshot{snapshot}}
			if err := (UninstallController{Platform: platform}).Run(t.Context(), expected, true); err == nil {
				t.Fatalf("state %s accepted", state)
			}
		})
	}
}
