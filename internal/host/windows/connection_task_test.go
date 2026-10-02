package windows

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type fakeConnectionTaskPlatform struct {
	paths       FrontendPaths
	user        string
	probe       ConnectionTaskProbe
	ownership   ConnectionTaskOwnership
	owned       bool
	createCalls int
	removeCalls int
	writeCalls  int
	deleteCalls int
	updateCalls int
	errAt       string
}

func (platform *fakeConnectionTaskPlatform) Probe(context.Context, string) (ConnectionTaskProbe, error) {
	if platform.errAt == "probe" {
		return ConnectionTaskProbe{}, errors.New("probe failed")
	}
	return platform.probe, nil
}

func (platform *fakeConnectionTaskPlatform) Create(_ context.Context, ownership ConnectionTaskOwnership) error {
	if platform.errAt == "create" {
		return errors.New("create failed")
	}
	platform.createCalls++
	platform.probe = ConnectionTaskProbe{
		Present: true, Description: ownership.Description,
		Actions:  []StartupTaskAction{{Executable: ownership.Executable, Arguments: ownership.Arguments}},
		RunLevel: "Limited", UserID: platform.user, TriggerCount: 1, LogonTrigger: true,
		ExecutionTimeTicks: connectionTaskExecutionTicks,
		RestartCount:       connectionTaskRestartCount, RestartIntervalTicks: connectionTaskRestartIntervalTicks,
	}
	return nil
}

func (platform *fakeConnectionTaskPlatform) UpdateRetryPolicy(context.Context, ConnectionTaskOwnership) error {
	if platform.errAt == "update" {
		return errors.New("retry policy update failed")
	}
	platform.updateCalls++
	if platform.errAt == "update-noop" {
		return nil
	}
	platform.probe.RestartCount = connectionTaskRestartCount
	platform.probe.RestartIntervalTicks = connectionTaskRestartIntervalTicks
	return nil
}

func (platform *fakeConnectionTaskPlatform) Remove(_ context.Context, _ ConnectionTaskOwnership) error {
	if platform.errAt == "remove" {
		return errors.New("remove failed")
	}
	platform.removeCalls++
	platform.probe = ConnectionTaskProbe{}
	return nil
}

func (platform *fakeConnectionTaskPlatform) ReadOwnership(string) (ConnectionTaskOwnership, bool, error) {
	if platform.errAt == "read" {
		return ConnectionTaskOwnership{}, false, errors.New("read failed")
	}
	return platform.ownership, platform.owned, nil
}

func (platform *fakeConnectionTaskPlatform) WriteOwnership(_ context.Context, ownership ConnectionTaskOwnership) error {
	if platform.errAt == "write" {
		return errors.New("write failed")
	}
	platform.writeCalls++
	platform.ownership = ownership
	platform.owned = true
	return nil
}

func (platform *fakeConnectionTaskPlatform) DeleteOwnership(context.Context, string) error {
	if platform.errAt == "delete" {
		return errors.New("delete failed")
	}
	platform.deleteCalls++
	platform.ownership = ConnectionTaskOwnership{}
	platform.owned = false
	return nil
}

func (platform *fakeConnectionTaskPlatform) VerifyCanonicalFrontend(context.Context) (FrontendPaths, error) {
	if platform.errAt == "frontend" {
		return FrontendPaths{}, errors.New("frontend failed")
	}
	return platform.paths, nil
}

func (platform *fakeConnectionTaskPlatform) CurrentUser() (string, error) {
	return platform.user, nil
}

func connectionTaskFixture(t *testing.T) (*fakeConnectionTaskPlatform, ConnectionTaskManager, ConnectionTaskOwnership) {
	t.Helper()
	paths, err := ResolveFrontendPaths(`C:\Users\alice\AppData\Local`)
	if err != nil {
		t.Fatal(err)
	}
	platform := &fakeConnectionTaskPlatform{paths: paths, user: `DESKTOP\alice`}
	manager := ConnectionTaskManager{Platform: platform}
	expected := expectedConnectionTask(paths, "loki-mcp")
	return platform, manager, expected
}

func TestConnectionTaskManagerCreatesFiniteOwnedTaskAndRemovesIt(t *testing.T) {
	platform, manager, expected := connectionTaskFixture(t)
	if err := manager.Reconcile(t.Context(), "loki-mcp", true); err != nil {
		t.Fatal(err)
	}
	if platform.createCalls != 1 || platform.writeCalls != 1 || !platform.owned {
		t.Fatalf("create=%d write=%d owned=%v", platform.createCalls, platform.writeCalls, platform.owned)
	}
	if err := validateConnectionTaskOwnership(platform.ownership, expected); err != nil {
		t.Fatal(err)
	}
	if platform.probe.ExecutionTimeTicks != 6_000_000_000 || !platform.probe.LogonTrigger {
		t.Fatalf("probe=%+v", platform.probe)
	}

	if err := manager.Reconcile(t.Context(), "loki-mcp", true); err != nil {
		t.Fatal(err)
	}
	if platform.createCalls != 1 || platform.writeCalls != 1 {
		t.Fatal("idempotent reconcile recreated owned task")
	}

	if err := manager.Reconcile(t.Context(), "loki-mcp", false); err != nil {
		t.Fatal(err)
	}
	if platform.removeCalls != 1 || platform.deleteCalls != 1 || platform.owned || platform.probe.Present {
		t.Fatalf("remove=%d delete=%d owned=%v probe=%+v", platform.removeCalls, platform.deleteCalls, platform.owned, platform.probe)
	}
}

func TestConnectionTaskManagerRefusesUnownedOrDriftedTask(t *testing.T) {
	platform, manager, expected := connectionTaskFixture(t)
	platform.probe = ConnectionTaskProbe{
		Present: true, Description: expected.Description,
		Actions:  []StartupTaskAction{{Executable: expected.Executable, Arguments: expected.Arguments}},
		RunLevel: "Limited", UserID: platform.user, TriggerCount: 1, LogonTrigger: true,
		ExecutionTimeTicks: connectionTaskExecutionTicks,
	}
	if err := manager.Reconcile(t.Context(), "loki-mcp", true); err == nil {
		t.Fatal("unowned existing task was adopted")
	}

	platform.ownership = expected
	platform.owned = true
	platform.probe.Actions[0].Arguments = "connect startup --distribution foreign"
	if err := manager.Reconcile(t.Context(), "loki-mcp", true); err == nil {
		t.Fatal("drifted owned task was accepted")
	}
}

func TestConnectionTaskManagerRepairsMissingTaskOnlyWithOwnership(t *testing.T) {
	platform, manager, expected := connectionTaskFixture(t)
	platform.ownership = expected
	platform.owned = true
	if err := manager.Reconcile(t.Context(), "loki-mcp", true); err != nil {
		t.Fatal(err)
	}
	if platform.createCalls != 1 || platform.writeCalls != 0 {
		t.Fatalf("create=%d write=%d", platform.createCalls, platform.writeCalls)
	}
	if !reflect.DeepEqual(platform.ownership, expected) {
		t.Fatalf("ownership changed=%+v", platform.ownership)
	}
}

func TestConnectionTaskManagerRollsBackTaskWhenOwnershipPublishFails(t *testing.T) {
	platform, manager, _ := connectionTaskFixture(t)
	platform.errAt = "write"
	if err := manager.Reconcile(t.Context(), "loki-mcp", true); err == nil {
		t.Fatal("ownership publish failure ignored")
	}
	if platform.createCalls != 1 || platform.removeCalls != 1 || platform.probe.Present {
		t.Fatalf("create=%d remove=%d probe=%+v", platform.createCalls, platform.removeCalls, platform.probe)
	}
}

func TestConnectionTaskManagerUpgradesOnlyVerifiedLegacyRetryPolicy(t *testing.T) {
	for _, test := range []struct {
		name        string
		count       int
		interval    int64
		owned       bool
		wantUpgrade bool
	}{
		{"legacy", 0, 0, true, true},
		{"current", connectionTaskRestartCount, connectionTaskRestartIntervalTicks, true, false},
		{"unowned", 0, 0, false, false},
		{"drifted-count", 7, connectionTaskRestartIntervalTicks, true, false},
		{"drifted-interval", connectionTaskRestartCount, 1, true, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			platform, manager, expected := connectionTaskFixture(t)
			if err := platform.Create(t.Context(), expected); err != nil {
				t.Fatal(err)
			}
			platform.owned, platform.ownership = test.owned, expected
			platform.probe.RestartCount, platform.probe.RestartIntervalTicks = test.count, test.interval
			err := manager.Reconcile(t.Context(), "loki-mcp", true)
			valid := test.wantUpgrade || test.name == "current"
			if (err == nil) != valid || (platform.updateCalls == 1) != test.wantUpgrade || platform.removeCalls != 0 {
				t.Fatalf("err=%v updates=%d removals=%d", err, platform.updateCalls, platform.removeCalls)
			}
			if valid && (platform.probe.RestartCount != connectionTaskRestartCount || platform.probe.RestartIntervalTicks != connectionTaskRestartIntervalTicks) {
				t.Fatalf("retry policy was not installed: %+v", platform.probe)
			}
		})
	}
}

func TestConnectionTaskManagerReportsLegacyUpgradeFailure(t *testing.T) {
	platform, manager, expected := connectionTaskFixture(t)
	if err := platform.Create(t.Context(), expected); err != nil {
		t.Fatal(err)
	}
	platform.owned, platform.ownership = true, expected
	platform.probe.RestartCount, platform.probe.RestartIntervalTicks = 0, 0
	platform.errAt = "update"
	if err := manager.Reconcile(t.Context(), "loki-mcp", true); err == nil {
		t.Fatal("failed retry policy update was reported as successful")
	}
	if platform.removeCalls != 0 || !platform.owned {
		t.Fatal("upgrade failure destroyed owned task")
	}
	platform.errAt = ""
	if err := manager.Reconcile(t.Context(), "loki-mcp", false); err != nil {
		t.Fatal(err)
	}
}

func TestConnectionTaskManagerVerifiesUpdatedRetrySettings(t *testing.T) {
	platform, manager, expected := connectionTaskFixture(t)
	if err := platform.Create(t.Context(), expected); err != nil {
		t.Fatal(err)
	}
	platform.owned, platform.ownership = true, expected
	platform.probe.RestartCount, platform.probe.RestartIntervalTicks = 0, 0
	platform.errAt = "update-noop"
	if err := manager.Reconcile(t.Context(), "loki-mcp", true); err == nil {
		t.Fatal("unchanged legacy settings were accepted after an ineffective update")
	}
}
