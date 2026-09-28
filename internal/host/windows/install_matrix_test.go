package windows

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
)

type mapStateRemover struct {
	filesystem *fakeStateFilesystem
	removed    []string
}

func (remover *mapStateRemover) RemoveAll(target string) error {
	remover.removed = append(remover.removed, target)
	for path := range remover.filesystem.paths {
		if WindowsPathEqual(path, target) || windowsPathWithinNormalized(path, target) {
			delete(remover.filesystem.paths, path)
		}
	}
	delete(remover.filesystem.dirs, target)
	for path := range remover.filesystem.files {
		if WindowsPathEqual(path, target) || windowsPathWithinNormalized(path, target) {
			delete(remover.filesystem.files, path)
		}
	}
	return nil
}

func windowsPathWithinNormalized(candidate, parent string) bool {
	left, leftOK := normalizeWindowsPath(candidate)
	right, rightOK := normalizeWindowsPath(parent)
	return leftOK && rightOK && windowsPathWithin(left, right)
}

func TestInstallMatrixStaleApprovalDeniedAndApproved(t *testing.T) {
	expected := fixtureExpected()
	t.Run("denied", func(t *testing.T) {
		runner := &fakeNativeRunner{results: stalePreflightResults(expected.Distribution)}
		fs := fakeStateFilesystem{paths: map[string]StatePath{}, dirs: map[string][]string{}, files: map[string][]byte{}, errs: map[string]error{}}
		tasks := &fakeTaskManager{}
		collector := PreflightCollector{Filesystem: fs, Tasks: tasks, WSL: WSLClient{Runner: runner}}
		fresh := &fakeFreshInstaller{}
		controller := InstallController{
			Collector:  collector,
			Recovery:   RecoveryExecutor{Collector: collector, Tasks: tasks, Filesystem: fs, Remover: &fakeRemover{}},
			Port:       &fakePortProbe{available: true},
			Fresh:      fresh,
			Filesystem: fs,
		}
		_, err := controller.Run(context.Background(), expected, InstallOptions{
			Distribution: expected.Distribution, MCPPort: 18765, AutoStart: true,
		}, nil)
		var blocked InstallBlockedError
		if !errors.As(err, &blocked) || blocked.Reason != "stale-reinstall-approval-required" {
			t.Fatalf("unexpected stale denial: %v", err)
		}
		if fresh.calls != 0 || len(runner.calls) != 7 {
			t.Fatalf("denied stale recovery mutated state: fresh=%d calls=%#v", fresh.calls, runner.calls)
		}
	})

	t.Run("approved", func(t *testing.T) {
		results := append(stalePreflightResults(expected.Distribution),
			stalePreflightResults(expected.Distribution)...,
		)
		results = append(results,
			NativeProbe{Stdout: `{"generation":{"spec":{"version":"0.1.19"}}}`},
			NativeProbe{Stdout: "loki 0.1.19"},
			NativeProbe{},
			NativeProbe{Stdout: `{"generation":{"spec":{"version":"0.1.19"}}}`},
			NativeProbe{Stdout: "loki 0.1.19"},
			NativeProbe{},
			NativeProbe{Stdout: ""},
			NativeProbe{Stdout: ""},
		)
		runner := &fakeNativeRunner{results: results}
		fs := fakeStateFilesystem{paths: map[string]StatePath{}, dirs: map[string][]string{}, files: map[string][]byte{}, errs: map[string]error{}}
		tasks := &fakeTaskManager{}
		collector := PreflightCollector{Filesystem: fs, Tasks: tasks, WSL: WSLClient{Runner: runner}}
		fresh := &fakeFreshInstaller{}
		controller := InstallController{
			Collector:  collector,
			Recovery:   RecoveryExecutor{Collector: collector, Tasks: tasks, Filesystem: fs, Remover: &fakeRemover{}},
			Port:       &fakePortProbe{available: true},
			Fresh:      fresh,
			Filesystem: fs,
		}
		result, err := controller.Run(context.Background(), expected, InstallOptions{
			Distribution: expected.Distribution, MCPPort: 18765, AutoStart: true, ReinstallRequested: true,
		}, nil)
		if err != nil {
			t.Fatal(err)
		}
		if result.Disposition != InstallCompleted || fresh.calls != 1 {
			t.Fatalf("stale approved result=%#v fresh=%d", result, fresh.calls)
		}
		if len(runner.calls) != 21 {
			t.Fatalf("unexpected stale recovery calls=%#v", runner.calls)
		}
	})
}

func stalePreflightResults(distribution string) []NativeProbe {
	return []NativeProbe{
		{Stdout: distribution},
		{Stdout: `{"generation":{"spec":{"version":"0.1.19"}}}`},
		{Stdout: "loki 0.1.19"},
		{ExitCode: 0},
		{ExitCode: 1},
		{ExitCode: 0, Stdout: `{"schema_version":1}`},
	}
}

func TestInstallMatrixOrphanPortConflictDoesNotMutate(t *testing.T) {
	expected := fixtureExpected()
	connection := joinWindowsPath(expected.StateDir, "connection.json")
	token := joinWindowsPath(expected.StateDir, "mcp-token")
	connectionRaw, _ := jsonMarshal(map[string]any{
		"endpoint":       "http://127.0.0.1:18765/mcp",
		"transport":      "streamable-http",
		"authentication": "bearer-token-file",
		"token_file":     token,
		"distribution":   expected.Distribution,
	})
	fs := fakeStateFilesystem{
		paths: map[string]StatePath{
			expected.StateDir: {Exists: true, Directory: true},
			connection:        {Exists: true, Regular: true},
			token:             {Exists: true, Regular: true},
		},
		dirs:  map[string][]string{expected.StateDir: {"connection.json", "mcp-token"}},
		files: map[string][]byte{connection: connectionRaw},
		errs:  map[string]error{},
	}
	runner := &fakeNativeRunner{results: []NativeProbe{{Stdout: ""}}}
	tasks := &fakeTaskManager{}
	collector := PreflightCollector{Filesystem: fs, Tasks: tasks, WSL: WSLClient{Runner: runner}}
	remover := &mapStateRemover{filesystem: &fs}
	fresh := &fakeFreshInstaller{}
	port := &fakePortProbe{available: false}
	controller := InstallController{
		Collector:  collector,
		Recovery:   RecoveryExecutor{Collector: collector, Tasks: tasks, Filesystem: fs, Remover: remover},
		Port:       port,
		Fresh:      fresh,
		Filesystem: fs,
	}
	_, err := controller.Run(context.Background(), expected, InstallOptions{
		Distribution: expected.Distribution, MCPPort: 18765, AutoStart: true,
	}, nil)
	var blocked InstallBlockedError
	if !errors.As(err, &blocked) || blocked.Reason != "mcp-port-in-use" {
		t.Fatalf("unexpected orphan port conflict: %v", err)
	}
	if len(remover.removed) != 0 || fresh.calls != 0 || tasks.removed != 0 {
		t.Fatalf("orphan port conflict mutated state: removed=%#v fresh=%d tasks=%d", remover.removed, fresh.calls, tasks.removed)
	}
	if !reflect.DeepEqual(port.calls, []int{18765}) {
		t.Fatalf("port preflight calls=%#v", port.calls)
	}
}

func TestInstallMatrixOrphanCleanupAndFreshInstall(t *testing.T) {
	expected := fixtureExpected()
	connection := joinWindowsPath(expected.StateDir, "connection.json")
	token := joinWindowsPath(expected.StateDir, "mcp-token")
	connectionRaw, _ := jsonMarshal(map[string]any{
		"endpoint":       "http://127.0.0.1:18765/mcp",
		"transport":      "streamable-http",
		"authentication": "bearer-token-file",
		"token_file":     token,
		"distribution":   expected.Distribution,
	})
	fs := fakeStateFilesystem{
		paths: map[string]StatePath{
			expected.StateDir: {Exists: true, Directory: true},
			connection:        {Exists: true, Regular: true},
			token:             {Exists: true, Regular: true},
		},
		dirs:  map[string][]string{expected.StateDir: {"connection.json", "mcp-token"}},
		files: map[string][]byte{connection: connectionRaw},
		errs:  map[string]error{},
	}
	runner := &fakeNativeRunner{results: []NativeProbe{{Stdout: ""}, {Stdout: ""}, {Stdout: ""}}}
	tasks := &fakeTaskManager{}
	collector := PreflightCollector{Filesystem: fs, Tasks: tasks, WSL: WSLClient{Runner: runner}}
	remover := &mapStateRemover{filesystem: &fs}
	fresh := &fakeFreshInstaller{}
	controller := InstallController{
		Collector:  collector,
		Recovery:   RecoveryExecutor{Collector: collector, Tasks: tasks, Filesystem: fs, Remover: remover},
		Port:       &fakePortProbe{available: true},
		Fresh:      fresh,
		Filesystem: fs,
	}
	result, err := controller.Run(context.Background(), expected, InstallOptions{
		Distribution: expected.Distribution, MCPPort: 18765, AutoStart: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != InstallCompleted || fresh.calls != 1 ||
		len(remover.removed) != 1 || !WindowsPathEqual(remover.removed[0], expected.StateDir) {
		t.Fatalf("orphan recovery result=%#v fresh=%d removed=%#v", result, fresh.calls, remover.removed)
	}
}

func TestInstallMatrixFreshPortConflictDoesNotMutate(t *testing.T) {
	expected := fixtureExpected()
	fs := fakeStateFilesystem{paths: map[string]StatePath{}, dirs: map[string][]string{}, files: map[string][]byte{}, errs: map[string]error{}}
	runner := &fakeNativeRunner{results: []NativeProbe{{Stdout: ""}}}
	tasks := &fakeTaskManager{}
	collector := PreflightCollector{Filesystem: fs, Tasks: tasks, WSL: WSLClient{Runner: runner}}
	fresh := &fakeFreshInstaller{}
	port := &fakePortProbe{available: false}
	controller := InstallController{
		Collector:  collector,
		Recovery:   RecoveryExecutor{Collector: collector, Tasks: tasks, Filesystem: fs, Remover: &fakeRemover{}},
		Port:       port,
		Fresh:      fresh,
		Filesystem: fs,
	}
	_, err := controller.Run(context.Background(), expected, InstallOptions{
		Distribution: expected.Distribution, MCPPort: 18765, AutoStart: true,
	}, nil)
	var blocked InstallBlockedError
	if !errors.As(err, &blocked) || blocked.Reason != "mcp-port-in-use" {
		t.Fatalf("unexpected fresh port conflict: %v", err)
	}
	if fresh.calls != 0 || tasks.removed != 0 || len(runner.calls) != 2 ||
		!reflect.DeepEqual(runner.calls[0].arguments, []string{"--help"}) ||
		!reflect.DeepEqual(runner.calls[1].arguments, []string{"--list", "--quiet"}) {
		t.Fatalf("fresh port conflict mutated state: fresh=%d tasks=%d calls=%#v", fresh.calls, tasks.removed, runner.calls)
	}
}

func TestInstallMatrixHealthyKeepaliveTaskIsAdoptedUnchanged(t *testing.T) {
	expected := fixtureExpected()
	snapshot := ExistingSnapshot{
		Distribution: DistributionState{State: DistributionHealthy},
		Windows: WindowsState{
			Present: true, Owned: true, Kind: WindowsStateManifest,
			MCPPort: 18765, AutoStart: true, InstallLocation: `D:\Loki\loki-mcp`,
		},
		StartupTask: StartupTaskState{Present: true, Owned: true},
	}
	port := &fakePortProbe{available: true}
	fresh := &fakeFreshInstaller{}
	controller := controllerFixture(t, snapshot, port, fresh)
	result, err := controller.Run(context.Background(), expected, InstallOptions{
		Distribution: expected.Distribution, MCPPort: 18765, AutoStart: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != InstallNoop || fresh.calls != 0 {
		t.Fatalf("healthy adoption result=%#v fresh=%d", result, fresh.calls)
	}
	if len(port.calls) != 0 {
		t.Fatalf("healthy adoption unexpectedly probed install port: %#v", port.calls)
	}
}

func TestInstallMatrixTransactionalFreshRollbackAndRetry(t *testing.T) {
	expected := fixtureExpected()
	fs := fakeStateFilesystem{paths: map[string]StatePath{}, dirs: map[string][]string{}, files: map[string][]byte{}, errs: map[string]error{}}
	runner := &fakeNativeRunner{results: []NativeProbe{{Stdout: ""}, {Stdout: ""}}}
	tasks := &fakeTaskManager{}
	collector := PreflightCollector{Filesystem: fs, Tasks: tasks, WSL: WSLClient{Runner: runner}}
	platform := &fakeFreshPlatform{failStage: "ownership"}
	controller := InstallController{
		Collector:  collector,
		Recovery:   RecoveryExecutor{Collector: collector, Tasks: tasks, Filesystem: fs, Remover: &fakeRemover{}},
		Port:       &fakePortProbe{available: true},
		Fresh:      TransactionalFreshInstaller{Platform: platform},
		Filesystem: fs,
	}
	options := InstallOptions{Distribution: expected.Distribution, MCPPort: 18765, AutoStart: true}
	if _, err := controller.Run(context.Background(), expected, options, nil); err == nil {
		t.Fatal("transactional fresh failure was ignored")
	}
	if len(platform.rollback) != 1 || !platform.rollback[0].CreatedDistribution ||
		!platform.rollback[0].CreatedStateDir || !platform.rollback[0].CreatedStartupTask {
		t.Fatalf("rollback transaction=%#v calls=%#v", platform.rollback, platform.calls)
	}
	platform.failStage = ""
	result, err := controller.Run(context.Background(), expected, options, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != InstallCompleted || len(platform.rollback) != 1 {
		t.Fatalf("retry result=%#v rollback=%#v calls=%#v", result, platform.rollback, platform.calls)
	}
}

func TestInstallMatrixCustomLocationConflict(t *testing.T) {
	expected := fixtureExpected()
	location := `D:\Existing\loki`
	fs := fakeStateFilesystem{
		paths: map[string]StatePath{location: {Exists: true, Directory: true}},
		dirs:  map[string][]string{}, files: map[string][]byte{}, errs: map[string]error{},
	}
	runner := &fakeNativeRunner{results: []NativeProbe{{Stdout: ""}}}
	tasks := &fakeTaskManager{}
	collector := PreflightCollector{Filesystem: fs, Tasks: tasks, WSL: WSLClient{Runner: runner}}
	fresh := &fakeFreshInstaller{}
	controller := InstallController{
		Collector:  collector,
		Recovery:   RecoveryExecutor{Collector: collector, Tasks: tasks, Filesystem: fs, Remover: &fakeRemover{}},
		Port:       &fakePortProbe{available: true},
		Fresh:      fresh,
		Filesystem: fs,
	}
	_, err := controller.Run(context.Background(), expected, InstallOptions{
		Distribution: expected.Distribution, MCPPort: 18765, AutoStart: true,
		InstallLocation: location, InstallLocationExplicit: true,
	}, nil)
	var blocked InstallBlockedError
	if !errors.As(err, &blocked) || blocked.Reason != "install-location-exists-unowned" {
		t.Fatalf("unexpected custom-location result: %v", err)
	}
	if fresh.calls != 0 {
		t.Fatal("fresh install ran against occupied custom location")
	}
}

func TestInstallMatrixFreshRetryAfterFailure(t *testing.T) {
	expected := fixtureExpected()
	fs := fakeStateFilesystem{paths: map[string]StatePath{}, dirs: map[string][]string{}, files: map[string][]byte{}, errs: map[string]error{}}
	runner := &fakeNativeRunner{results: []NativeProbe{{Stdout: ""}, {Stdout: ""}}}
	tasks := &fakeTaskManager{}
	collector := PreflightCollector{Filesystem: fs, Tasks: tasks, WSL: WSLClient{Runner: runner}}
	fresh := &fakeFreshInstaller{err: errors.New("synthetic fresh failure")}
	controller := InstallController{
		Collector:  collector,
		Recovery:   RecoveryExecutor{Collector: collector, Tasks: tasks, Filesystem: fs, Remover: &fakeRemover{}},
		Port:       &fakePortProbe{available: true},
		Fresh:      fresh,
		Filesystem: fs,
	}
	options := InstallOptions{Distribution: expected.Distribution, MCPPort: 18765, AutoStart: true}
	if _, err := controller.Run(context.Background(), expected, options, nil); err == nil {
		t.Fatal("synthetic fresh failure was ignored")
	}
	fresh.err = nil
	result, err := controller.Run(context.Background(), expected, options, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Disposition != InstallCompleted || fresh.calls != 2 {
		t.Fatalf("retry result=%#v calls=%d", result, fresh.calls)
	}
}

func TestInstallMatrixUnverifiedResourcesRemainUntouched(t *testing.T) {
	expected := fixtureExpected()
	fs := fakeStateFilesystem{
		paths: map[string]StatePath{expected.StateDir: {Exists: true, Directory: true}},
		dirs:  map[string][]string{expected.StateDir: {"foreign.txt"}},
		files: map[string][]byte{},
		errs:  map[string]error{},
	}
	runner := &fakeNativeRunner{results: []NativeProbe{{Stdout: ""}}}
	tasks := &fakeTaskManager{}
	collector := PreflightCollector{Filesystem: fs, Tasks: tasks, WSL: WSLClient{Runner: runner}}
	remover := &mapStateRemover{filesystem: &fs}
	fresh := &fakeFreshInstaller{}
	controller := InstallController{
		Collector:  collector,
		Recovery:   RecoveryExecutor{Collector: collector, Tasks: tasks, Filesystem: fs, Remover: remover},
		Port:       &fakePortProbe{available: true},
		Fresh:      fresh,
		Filesystem: fs,
	}
	_, err := controller.Run(context.Background(), expected, InstallOptions{Distribution: expected.Distribution, MCPPort: 18765}, nil)
	var blocked InstallBlockedError
	if !errors.As(err, &blocked) || !strings.Contains(blocked.Reason, "unverified") {
		t.Fatalf("unexpected error %v", err)
	}
	if len(remover.removed) != 0 || fresh.calls != 0 {
		t.Fatalf("unverified state mutated removed=%#v fresh=%d", remover.removed, fresh.calls)
	}
}
