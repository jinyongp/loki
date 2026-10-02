package windows

import (
	"context"
	"errors"
	"reflect"
	"testing"
	"time"
)

type fakeStartupConnections struct {
	enabled        int
	validateErr    error
	reconcileErr   error
	validateCalls  int
	reconcileCalls int
}

func (connections *fakeStartupConnections) ValidateEnabled(context.Context, string) (int, error) {
	connections.validateCalls++
	return connections.enabled, connections.validateErr
}

func (connections *fakeStartupConnections) ReconcileEnabled(context.Context, string) error {
	connections.reconcileCalls++
	return connections.reconcileErr
}

type fakeConnectionStartupPlatform struct {
	paths       FrontendPaths
	snapshot    ExistingSnapshot
	doctor      []NativeProbe
	doctorErr   error
	startCalls  int
	doctorCalls int
	sleepCalls  int
	frontendErr error
	collectErr  error
	startErr    error
}

func (platform *fakeConnectionStartupPlatform) VerifyCanonicalFrontend(context.Context) (FrontendPaths, error) {
	if platform.frontendErr != nil {
		return FrontendPaths{}, platform.frontendErr
	}
	return platform.paths, nil
}

func (platform *fakeConnectionStartupPlatform) Collect(context.Context, ExpectedInstallation) (ExistingSnapshot, error) {
	if platform.collectErr != nil {
		return ExistingSnapshot{}, platform.collectErr
	}
	return platform.snapshot, nil
}

func (platform *fakeConnectionStartupPlatform) StartKeepalive(context.Context, ExpectedInstallation) error {
	platform.startCalls++
	return platform.startErr
}

func (platform *fakeConnectionStartupPlatform) Doctor(context.Context, string) (NativeProbe, error) {
	platform.doctorCalls++
	if platform.doctorErr != nil {
		return NativeProbe{}, platform.doctorErr
	}
	if len(platform.doctor) == 0 {
		return NativeProbe{ExitCode: 1}, nil
	}
	result := platform.doctor[0]
	if len(platform.doctor) > 1 {
		platform.doctor = platform.doctor[1:]
	}
	return result, nil
}

func (platform *fakeConnectionStartupPlatform) Sleep(context.Context, time.Duration) error {
	platform.sleepCalls++
	return nil
}

func startupExpected() ExpectedInstallation {
	return ExpectedInstallation{
		Distribution: "loki-mcp",
		TaskName:     "Loki WSL (loki-mcp)",
	}
}

func TestConnectionStartupNoEnabledConnectionsHasNoHostSideEffects(t *testing.T) {
	platform := &fakeConnectionStartupPlatform{}
	connections := &fakeStartupConnections{enabled: 0}
	controller := ConnectionStartupController{Platform: platform, Connections: connections}
	result, err := controller.Run(t.Context(), startupExpected())
	if err != nil {
		t.Fatal(err)
	}
	if result.EnabledConnections != 0 || platform.startCalls != 0 || platform.doctorCalls != 0 ||
		connections.reconcileCalls != 0 {
		t.Fatalf("result=%+v platform=%+v connections=%+v", result, platform, connections)
	}
}

func TestConnectionStartupHealthyRestoresStoppedKeepaliveBeforeConnections(t *testing.T) {
	platform := &fakeConnectionStartupPlatform{
		snapshot: ExistingSnapshot{
			Distribution: DistributionState{State: DistributionHealthy, Version: "1.2.3"},
			Windows:      WindowsState{Present: true, Owned: true},
			StartupTask:  StartupTaskState{Present: true, Owned: true},
		},
	}
	connections := &fakeStartupConnections{enabled: 2}
	controller := ConnectionStartupController{Platform: platform, Connections: connections}
	result, err := controller.Run(t.Context(), startupExpected())
	if err != nil {
		t.Fatal(err)
	}
	if !result.KeepaliveStarted || result.HealthAttempts != 1 ||
		platform.startCalls != 1 || connections.reconcileCalls != 1 {
		t.Fatalf("result=%+v platform=%+v connections=%+v", result, platform, connections)
	}
}

func TestConnectionStartupStaleStartsVerifiedKeepaliveAndWaitsBoundedly(t *testing.T) {
	platform := &fakeConnectionStartupPlatform{
		snapshot: ExistingSnapshot{
			Distribution: DistributionState{State: DistributionStale, Version: "1.2.3"},
			Windows:      WindowsState{Present: true, Owned: true},
			StartupTask:  StartupTaskState{Present: true, Owned: true},
		},
		doctor: []NativeProbe{{ExitCode: 1}, {ExitCode: 1}, {ExitCode: 0}},
	}
	connections := &fakeStartupConnections{enabled: 1}
	controller := ConnectionStartupController{
		Platform: platform, Connections: connections, Attempts: 4, RetryDelay: time.Millisecond,
	}
	result, err := controller.Run(t.Context(), startupExpected())
	if err != nil {
		t.Fatal(err)
	}
	if !result.KeepaliveStarted || result.HealthAttempts != 3 ||
		platform.startCalls != 1 || platform.sleepCalls != 2 ||
		connections.reconcileCalls != 1 {
		t.Fatalf("result=%+v platform=%+v connections=%+v", result, platform, connections)
	}
}

func TestConnectionStartupRefusesStaleWithoutOwnedKeepalive(t *testing.T) {
	platform := &fakeConnectionStartupPlatform{
		snapshot: ExistingSnapshot{
			Distribution: DistributionState{State: DistributionStale, Version: "1.2.3"},
			Windows:      WindowsState{Present: true, Owned: true},
			StartupTask:  StartupTaskState{Present: true, Owned: false},
		},
	}
	connections := &fakeStartupConnections{enabled: 1}
	controller := ConnectionStartupController{Platform: platform, Connections: connections}
	if _, err := controller.Run(t.Context(), startupExpected()); err == nil {
		t.Fatal("unowned keepalive task was accepted")
	}
	if platform.startCalls != 0 || connections.reconcileCalls != 0 {
		t.Fatalf("platform=%+v connections=%+v", platform, connections)
	}
}

func TestConnectionStartupFailsClosedBeforeHostMutationOnStateValidation(t *testing.T) {
	platform := &fakeConnectionStartupPlatform{
		snapshot: ExistingSnapshot{
			Distribution: DistributionState{State: DistributionStale},
			Windows:      WindowsState{Present: true, Owned: true},
			StartupTask:  StartupTaskState{Present: true, Owned: true},
		},
	}
	connections := &fakeStartupConnections{enabled: 1, validateErr: errors.New("state drift")}
	controller := ConnectionStartupController{Platform: platform, Connections: connections}
	_, err := controller.Run(t.Context(), startupExpected())
	if err == nil {
		t.Fatal("connection state validation failure ignored")
	}
	if platform.startCalls != 0 {
		t.Fatal("host mutation occurred before connection ownership validation")
	}
}

func TestConnectionStartupStopsAtBoundAndDoesNotRestoreRuntime(t *testing.T) {
	platform := &fakeConnectionStartupPlatform{
		snapshot: ExistingSnapshot{
			Distribution: DistributionState{State: DistributionStale},
			Windows:      WindowsState{Present: true, Owned: true},
			StartupTask:  StartupTaskState{Present: true, Owned: true},
		},
		doctor: []NativeProbe{{ExitCode: 1}},
	}
	connections := &fakeStartupConnections{enabled: 1}
	controller := ConnectionStartupController{
		Platform: platform, Connections: connections, Attempts: 3, RetryDelay: time.Millisecond,
	}
	result, err := controller.Run(t.Context(), startupExpected())
	if err == nil {
		t.Fatal("unhealthy appliance accepted")
	}
	if result.HealthAttempts != 3 || platform.doctorCalls != 3 ||
		platform.sleepCalls != 2 || connections.reconcileCalls != 0 {
		t.Fatalf("result=%+v platform=%+v connections=%+v", result, platform, connections)
	}
}

func TestConnectionStartupPreservesCallOrder(t *testing.T) {
	var calls []string
	connections := startupCallConnections{calls: &calls}
	platform := startupCallPlatform{calls: &calls}
	controller := ConnectionStartupController{Platform: platform, Connections: connections}
	_, err := controller.Run(t.Context(), startupExpected())
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"frontend", "validate", "frontend", "collect", "keepalive", "reconcile"}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls=%v want=%v", calls, want)
	}
}

type startupCallConnections struct{ calls *[]string }

func (connections startupCallConnections) ValidateEnabled(context.Context, string) (int, error) {
	*connections.calls = append(*connections.calls, "validate")
	return 1, nil
}

func (connections startupCallConnections) ReconcileEnabled(context.Context, string) error {
	*connections.calls = append(*connections.calls, "reconcile")
	return nil
}

type startupCallPlatform struct{ calls *[]string }

func (platform startupCallPlatform) VerifyCanonicalFrontend(context.Context) (FrontendPaths, error) {
	*platform.calls = append(*platform.calls, "frontend")
	return FrontendPaths{}, nil
}

func (platform startupCallPlatform) Collect(context.Context, ExpectedInstallation) (ExistingSnapshot, error) {
	*platform.calls = append(*platform.calls, "collect")
	return ExistingSnapshot{
		Distribution: DistributionState{State: DistributionHealthy},
		Windows:      WindowsState{Present: true, Owned: true},
		StartupTask:  StartupTaskState{Present: true, Owned: true},
	}, nil
}

func (platform startupCallPlatform) StartKeepalive(context.Context, ExpectedInstallation) error {
	*platform.calls = append(*platform.calls, "keepalive")
	return nil
}

func TestConnectionStartupHealthyRequiresOwnedRunningKeepalive(t *testing.T) {
	for _, test := range []struct {
		name     string
		task     StartupTaskState
		startErr error
		wantErr  bool
	}{
		{name: "already running", task: StartupTaskState{Present: true, Owned: true, Running: true}},
		{name: "missing", wantErr: true},
		{name: "foreign", task: StartupTaskState{Present: true}, wantErr: true},
		{name: "cannot start", task: StartupTaskState{Present: true, Owned: true}, startErr: errors.New("task exited with code 2"), wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			platform := &fakeConnectionStartupPlatform{
				snapshot: ExistingSnapshot{Distribution: DistributionState{State: DistributionHealthy}, Windows: WindowsState{Present: true, Owned: true}, StartupTask: test.task},
				startErr: test.startErr,
			}
			connections := &fakeStartupConnections{enabled: 1}
			result, err := (ConnectionStartupController{Platform: platform, Connections: connections}).Run(t.Context(), startupExpected())
			if (err != nil) != test.wantErr || (test.wantErr && connections.reconcileCalls != 0) || result.KeepaliveStarted {
				t.Fatalf("result=%+v err=%v reconciles=%d", result, err, connections.reconcileCalls)
			}
		})
	}
}

func (startupCallPlatform) Doctor(context.Context, string) (NativeProbe, error) {
	return NativeProbe{ExitCode: 0}, nil
}

func (startupCallPlatform) Sleep(context.Context, time.Duration) error {
	return nil
}
