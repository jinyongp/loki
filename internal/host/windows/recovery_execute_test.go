package windows

import (
	"context"
	"errors"
	"reflect"
	"testing"
)

type fakeTaskManager struct {
	probe   StartupTaskProbe
	removed int
	err     error
}

func (manager *fakeTaskManager) Probe(context.Context, string) (StartupTaskProbe, error) {
	return manager.probe, nil
}

func (manager *fakeTaskManager) RemoveOwned(context.Context, ExpectedInstallation) error {
	manager.removed++
	return manager.err
}

type fakeRemover struct {
	paths []string
	err   error
}

func (remover *fakeRemover) RemoveAll(path string) error {
	remover.paths = append(remover.paths, path)
	return remover.err
}

func TestRecoveryExecutorDoesNotMutateBlockedState(t *testing.T) {
	expected := fixtureExpected()
	tasks := &fakeTaskManager{}
	remover := &fakeRemover{}
	runner := &fakeNativeRunner{}
	executor := RecoveryExecutor{
		Collector: PreflightCollector{
			Filesystem: fakeStateFilesystem{paths: map[string]StatePath{}, dirs: map[string][]string{}, files: map[string][]byte{}, errs: map[string]error{}},
			Tasks:      tasks,
			WSL:        WSLClient{Runner: runner},
		},
		Tasks:      tasks,
		Filesystem: fakeStateFilesystem{paths: map[string]StatePath{}, dirs: map[string][]string{}, files: map[string][]byte{}, errs: map[string]error{}},
		Remover:    remover,
	}
	snapshot := ExistingSnapshot{
		Distribution: DistributionState{State: DistributionAbsent},
		Windows:      WindowsState{Present: true, Kind: WindowsStateUnverified},
	}
	plan, err := executor.Recover(context.Background(), expected, snapshot, false)
	if err != nil {
		t.Fatal(err)
	}
	if len(plan.Steps) != 0 || tasks.removed != 0 || len(remover.paths) != 0 || len(runner.calls) != 0 {
		t.Fatalf("blocked recovery mutated state: plan=%#v tasks=%d paths=%#v calls=%#v", plan, tasks.removed, remover.paths, runner.calls)
	}
}

func TestRemoveOwnedWindowsStateRevalidatesPaths(t *testing.T) {
	expected := fixtureExpected()
	location := `D:\Loki\loki-mcp`
	ownership := joinWindowsPath(expected.StateDir, "ownership.json")
	fs := fakeStateFilesystem{
		paths: map[string]StatePath{
			expected.StateDir: {Exists: true, Directory: true},
			ownership:         {Exists: true, Regular: true},
			location:          {Exists: true, Directory: true},
		},
		dirs: map[string][]string{
			expected.StateDir: {"connection.json", "mcp-token", "ownership.json"},
		},
		files: map[string][]byte{ownership: manifestFixture(t, expected)},
		errs:  map[string]error{},
	}
	state, err := InspectWindowsState(fs, expected)
	if err != nil {
		t.Fatal(err)
	}
	remover := &fakeRemover{}
	if err := RemoveOwnedWindowsState(fs, remover, expected, state); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(remover.paths, []string{location, expected.StateDir}) {
		t.Fatalf("removed %#v", remover.paths)
	}
	fs.paths[location] = StatePath{Exists: true, Directory: true, Reparse: true}
	remover.paths = nil
	if err := RemoveOwnedWindowsState(fs, remover, expected, state); err == nil {
		t.Fatal("reparse install location accepted")
	}
	if len(remover.paths) != 0 {
		t.Fatalf("unsafe state mutated: %#v", remover.paths)
	}
}

func TestRemoveOwnedWindowsStatePreservesProofWhenCustomLocationRemovalFails(t *testing.T) {
	expected := fixtureExpected()
	location := `D:\Loki\loki-mcp`
	ownership := joinWindowsPath(expected.StateDir, "ownership.json")
	fs := fakeStateFilesystem{
		paths: map[string]StatePath{
			expected.StateDir: {Exists: true, Directory: true},
			ownership:         {Exists: true, Regular: true},
			location:          {Exists: true, Directory: true},
		},
		dirs:  map[string][]string{expected.StateDir: {"connection.json", "mcp-token", "ownership.json"}},
		files: map[string][]byte{ownership: manifestFixture(t, expected)},
		errs:  map[string]error{},
	}
	state, err := InspectWindowsState(fs, expected)
	if err != nil {
		t.Fatal(err)
	}
	if !WindowsPathEqual(state.InstallLocation, location) {
		t.Fatalf("manifest location=%q want=%q", state.InstallLocation, location)
	}
	remover := &fakeRemover{err: errors.New("location locked")}
	if err := RemoveOwnedWindowsState(fs, remover, expected, state); err == nil {
		t.Fatal("custom location removal failure was ignored")
	}
	if !reflect.DeepEqual(remover.paths, []string{location}) {
		t.Fatalf("ownership proof was removed before custom location: %#v", remover.paths)
	}
}

func TestRemoveOwnedWindowsStateRejectsOwnershipDrift(t *testing.T) {
	expected := fixtureExpected()
	location := `D:\Loki\loki-mcp`
	ownership := joinWindowsPath(expected.StateDir, "ownership.json")
	fs := fakeStateFilesystem{
		paths: map[string]StatePath{
			expected.StateDir: {Exists: true, Directory: true},
			ownership:         {Exists: true, Regular: true},
			location:          {Exists: true, Directory: true},
		},
		dirs:  map[string][]string{expected.StateDir: {"connection.json", "mcp-token", "ownership.json"}},
		files: map[string][]byte{ownership: manifestFixture(t, expected)},
		errs:  map[string]error{},
	}
	approved, err := InspectWindowsState(fs, expected)
	if err != nil {
		t.Fatal(err)
	}
	var manifest ownershipManifestDisk
	if err := jsonUnmarshal(fs.files[ownership], &manifest); err != nil {
		t.Fatal(err)
	}
	manifest.MCPPort = 19000
	fs.files[ownership], _ = jsonMarshal(manifest)
	remover := &fakeRemover{}
	if err := RemoveOwnedWindowsState(fs, remover, expected, approved); err == nil {
		t.Fatal("changed ownership manifest was accepted")
	}
	if len(remover.paths) != 0 {
		t.Fatalf("ownership drift mutated state: %#v", remover.paths)
	}
}

func TestRemoveOwnedWindowsStateRejectsUnverifiedState(t *testing.T) {
	expected := fixtureExpected()
	if err := RemoveOwnedWindowsState(
		fakeStateFilesystem{}, &fakeRemover{}, expected,
		WindowsState{Present: true, Kind: WindowsStateUnverified},
	); err == nil {
		t.Fatal("unverified state removal accepted")
	}
}

func TestRecoveryExecutorRejectsOwnershipDriftBeforeMutation(t *testing.T) {
	expected := fixtureExpected()
	fs := fakeStateFilesystem{
		paths: map[string]StatePath{expected.StateDir: {Exists: true, Directory: true}},
		dirs:  map[string][]string{expected.StateDir: {"foreign.txt"}},
		files: map[string][]byte{},
		errs:  map[string]error{},
	}
	tasks := &fakeTaskManager{}
	runner := &fakeNativeRunner{results: []NativeProbe{{Stdout: ""}}}
	collector := PreflightCollector{Filesystem: fs, Tasks: tasks, WSL: WSLClient{Runner: runner}}
	remover := &fakeRemover{}
	executor := RecoveryExecutor{Collector: collector, Tasks: tasks, Filesystem: fs, Remover: remover}
	approved := ExistingSnapshot{
		Distribution: DistributionState{State: DistributionAbsent},
		Windows:      WindowsState{Present: true, Owned: true, Kind: WindowsStateLegacy, MCPPort: 18765},
	}
	if _, err := executor.Recover(context.Background(), expected, approved, false); err == nil {
		t.Fatal("ownership drift was accepted")
	}
	if tasks.removed != 0 || len(remover.paths) != 0 {
		t.Fatalf("ownership drift mutated state: tasks=%d paths=%#v", tasks.removed, remover.paths)
	}
}

func TestRecoveryExecutorRevalidatesDistroIdentityBeforeTerminate(t *testing.T) {
	expected := fixtureExpected()
	runner := &fakeNativeRunner{results: []NativeProbe{
		{Stdout: expected.Distribution},
		{Stdout: `{"generation":{"spec":{"version":"0.1.19"}}}`},
		{Stdout: "loki 0.1.19"},
		{ExitCode: 0},
		{ExitCode: 1},
		{ExitCode: 0, Stdout: `{"schema_version":1}`},
		{Stdout: `{"generation":{"spec":{"version":"9.9.9"}}}`},
		{Stdout: "loki 9.9.9"},
	}}
	fs := fakeStateFilesystem{paths: map[string]StatePath{}, dirs: map[string][]string{}, files: map[string][]byte{}, errs: map[string]error{}}
	tasks := &fakeTaskManager{}
	collector := PreflightCollector{Filesystem: fs, Tasks: tasks, WSL: WSLClient{Runner: runner}}
	executor := RecoveryExecutor{Collector: collector, Tasks: tasks, Filesystem: fs, Remover: &fakeRemover{}}
	approved := ExistingSnapshot{
		Distribution: DistributionState{State: DistributionStale, Version: "0.1.19"},
		Windows:      WindowsState{Kind: WindowsStateAbsent},
	}

	if _, err := executor.Recover(context.Background(), expected, approved, true); err == nil {
		t.Fatal("changed WSL identity was accepted")
	}
	if len(runner.calls) < 8 {
		t.Fatalf("identity revalidation was not reached: %#v", runner.calls)
	}
	for _, call := range runner.calls {
		if reflect.DeepEqual(call.arguments, []string{"--terminate", expected.Distribution}) ||
			reflect.DeepEqual(call.arguments, []string{"--unregister", expected.Distribution}) {
			t.Fatalf("changed WSL identity was mutated: %#v", runner.calls)
		}
	}
}

func TestRecoveryExecutorRevalidatesDistroIdentityBeforeUnregister(t *testing.T) {
	expected := fixtureExpected()
	runner := &fakeNativeRunner{results: []NativeProbe{
		{Stdout: expected.Distribution},
		{Stdout: `{"generation":{"spec":{"version":"0.1.19"}}}`},
		{Stdout: "loki 0.1.19"},
		{ExitCode: 0},
		{ExitCode: 1},
		{ExitCode: 0, Stdout: `{"schema_version":1}`},
		{Stdout: `{"generation":{"spec":{"version":"0.1.19"}}}`},
		{Stdout: "loki 0.1.19"},
		{ExitCode: 0},
		{Stdout: `{"generation":{"spec":{"version":"9.9.9"}}}`},
		{Stdout: "loki 9.9.9"},
	}}
	fs := fakeStateFilesystem{paths: map[string]StatePath{}, dirs: map[string][]string{}, files: map[string][]byte{}, errs: map[string]error{}}
	tasks := &fakeTaskManager{}
	collector := PreflightCollector{Filesystem: fs, Tasks: tasks, WSL: WSLClient{Runner: runner}}
	executor := RecoveryExecutor{Collector: collector, Tasks: tasks, Filesystem: fs, Remover: &fakeRemover{}}
	approved := ExistingSnapshot{
		Distribution: DistributionState{State: DistributionStale, Version: "0.1.19"},
		Windows:      WindowsState{Kind: WindowsStateAbsent},
	}

	if _, err := executor.Recover(context.Background(), expected, approved, true); err == nil {
		t.Fatal("changed WSL identity was accepted before unregister")
	}
	if len(runner.calls) < 11 {
		t.Fatalf("second identity revalidation was not reached: %#v", runner.calls)
	}
	terminateCalls := 0
	unregisterCalls := 0
	for _, call := range runner.calls {
		if reflect.DeepEqual(call.arguments, []string{"--terminate", expected.Distribution}) {
			terminateCalls++
		}
		if reflect.DeepEqual(call.arguments, []string{"--unregister", expected.Distribution}) {
			unregisterCalls++
		}
	}
	if terminateCalls != 1 || unregisterCalls != 0 {
		t.Fatalf("destructive WSL calls after identity drift: terminate=%d unregister=%d calls=%#v", terminateCalls, unregisterCalls, runner.calls)
	}
}

func TestBuildRollbackPlanOnlyContainsCreatedResources(t *testing.T) {
	if steps := BuildRollbackPlan(InstallTransaction{}); len(steps) != 0 {
		t.Fatalf("empty transaction generated rollback %#v", steps)
	}
	got := BuildRollbackPlan(InstallTransaction{
		CreatedDistribution: true,
		CreatedStateDir:     true,
		CreatedStartupTask:  true,
		InstallLocation:     `D:\Loki\loki-mcp`,
	})
	want := []RollbackStep{
		RollbackRemoveStartupTask,
		RollbackRemoveStateDir,
		RollbackTerminateDistro,
		RollbackUnregisterDistro,
		RollbackRemoveInstallLocation,
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("rollback=%#v want=%#v", got, want)
	}
}

func TestRecoveryExecutorStopsOnMutationFailure(t *testing.T) {
	expected := fixtureExpected()
	tasks := &fakeTaskManager{err: errors.New("task changed")}
	executor := RecoveryExecutor{Tasks: tasks}
	snapshot := ExistingSnapshot{
		Distribution: DistributionState{State: DistributionStale},
		StartupTask:  StartupTaskState{Present: true, Owned: true},
	}
	plan, err := executor.Recover(context.Background(), expected, snapshot, true)
	if err == nil || len(plan.Steps) == 0 {
		t.Fatalf("expected mutation failure, plan=%#v err=%v", plan, err)
	}
	if tasks.removed != 0 {
		t.Fatalf("adapter validation allowed partial mutation: removed=%d", tasks.removed)
	}
}
