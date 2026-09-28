package windows

import (
	"context"
	"encoding/json"
	"errors"
	"testing"
)

type fakePortProbe struct {
	available bool
	calls     []int
	err       error
}

func (probe *fakePortProbe) Available(port int) (bool, error) {
	probe.calls = append(probe.calls, port)
	return probe.available, probe.err
}

type fakeFreshInstaller struct {
	calls   int
	options InstallOptions
	err     error
}

func (installer *fakeFreshInstaller) Install(_ context.Context, _ ExpectedInstallation, options InstallOptions) error {
	installer.calls++
	installer.options = options
	return installer.err
}

func controllerFixture(
	t *testing.T,
	snapshot ExistingSnapshot,
	port *fakePortProbe,
	fresh *fakeFreshInstaller,
) InstallController {
	t.Helper()
	expected := fixtureExpected()
	fs := fakeStateFilesystem{
		paths: map[string]StatePath{},
		dirs:  map[string][]string{},
		files: map[string][]byte{},
		errs:  map[string]error{},
	}
	if snapshot.Windows.Present {
		ownership := joinWindowsPath(expected.StateDir, "ownership.json")
		fs.paths[expected.StateDir] = StatePath{Exists: true, Directory: true}
		if snapshot.Windows.Kind == WindowsStateManifest {
			fs.paths[ownership] = StatePath{Exists: true, Regular: true}
			fs.dirs[expected.StateDir] = []string{"connection.json", "mcp-token", "ownership.json"}
			raw := manifestFixture(t, expected)
			if snapshot.Windows.MCPPort != 0 || snapshot.Windows.InstallLocation != "" || !snapshot.Windows.AutoStart {
				var manifest ownershipManifestDisk
				if err := jsonUnmarshal(raw, &manifest); err != nil {
					t.Fatal(err)
				}
				if snapshot.Windows.MCPPort != 0 {
					manifest.MCPPort = snapshot.Windows.MCPPort
				}
				if snapshot.Windows.InstallLocation != "" {
					manifest.InstallLocation = snapshot.Windows.InstallLocation
					fs.paths[snapshot.Windows.InstallLocation] = StatePath{Exists: true, Directory: true}
				}
				manifest.AutoStart = snapshot.Windows.AutoStart
				raw, _ = jsonMarshal(manifest)
			} else {
				fs.paths[`D:\Loki\loki-mcp`] = StatePath{Exists: true, Directory: true}
			}
			fs.files[ownership] = raw
		} else if snapshot.Windows.Kind == WindowsStateLegacy {
			connection := joinWindowsPath(expected.StateDir, "connection.json")
			token := joinWindowsPath(expected.StateDir, "mcp-token")
			fs.paths[connection] = StatePath{Exists: true, Regular: true}
			fs.paths[token] = StatePath{Exists: true, Regular: true}
			fs.dirs[expected.StateDir] = []string{"connection.json", "mcp-token"}
			fs.files[connection], _ = jsonMarshal(map[string]any{
				"endpoint":       "http://127.0.0.1:18765/mcp",
				"transport":      "streamable-http",
				"authentication": "bearer-token-file",
				"token_file":     token,
				"distribution":   expected.Distribution,
			})
		} else {
			fs.dirs[expected.StateDir] = []string{"foreign"}
		}
	}

	runnerResults := []NativeProbe{}
	if snapshot.Distribution.State != DistributionAbsent {
		runnerResults = append(runnerResults, NativeProbe{Stdout: expected.Distribution})
		runnerResults = append(runnerResults,
			NativeProbe{Stdout: `{"generation":{"spec":{"version":"0.1.19"}}}`},
			NativeProbe{Stdout: "loki 0.1.19"},
		)
		switch snapshot.Distribution.State {
		case DistributionHealthy:
			runnerResults = append(runnerResults, NativeProbe{ExitCode: 0}, NativeProbe{}, NativeProbe{})
		case DistributionStale:
			runnerResults = append(runnerResults, NativeProbe{ExitCode: 0}, NativeProbe{ExitCode: 1}, NativeProbe{})
		case DistributionProvisioning:
			runnerResults = append(runnerResults,
				NativeProbe{ExitCode: 1},
				NativeProbe{Stdout: "ActiveState=activating\nSubState=start\nNRestarts=0"},
			)
		case DistributionIndeterminate:
			runnerResults = append(runnerResults, NativeProbe{ExitCode: 2})
		case DistributionForeign:
			runnerResults[len(runnerResults)-2] = NativeProbe{ExitCode: 1}
		}
	} else {
		runnerResults = append(runnerResults, NativeProbe{Stdout: ""})
	}
	runner := &fakeNativeRunner{results: runnerResults}
	taskProbe := StartupTaskProbe{}
	if snapshot.StartupTask.Present {
		taskProbe.Present = true
		if snapshot.StartupTask.Owned {
			taskProbe.Description = "Keep the Loki WSL2 appliance running."
			taskProbe.Actions = []StartupTaskAction{{Executable: expected.TaskExecutable, Arguments: expected.TaskArguments}}
		} else {
			taskProbe.Actions = []StartupTaskAction{{Executable: "foreign.exe"}}
		}
	}
	tasks := &fakeTaskManager{probe: taskProbe}
	collector := PreflightCollector{Filesystem: fs, Tasks: tasks, WSL: WSLClient{Runner: runner}}
	return InstallController{
		Collector:  collector,
		Recovery:   RecoveryExecutor{Collector: collector, Tasks: tasks, Filesystem: fs, Remover: &fakeRemover{}},
		Port:       port,
		Fresh:      fresh,
		Filesystem: fs,
	}
}

func TestInstallControllerHealthyNoopAndInProgressProtection(t *testing.T) {
	expected := fixtureExpected()
	for name, state := range map[string]DistributionStateKind{
		"healthy":      DistributionHealthy,
		"provisioning": DistributionProvisioning,
	} {
		t.Run(name, func(t *testing.T) {
			port := &fakePortProbe{available: true}
			fresh := &fakeFreshInstaller{}
			snapshot := ExistingSnapshot{
				Distribution: DistributionState{State: state},
				Windows: WindowsState{
					Present: true, Owned: true, Kind: WindowsStateManifest,
					MCPPort: 18765, AutoStart: true, InstallLocation: `D:\Loki\loki-mcp`,
				},
				StartupTask: StartupTaskState{Present: true, Owned: true},
			}
			controller := controllerFixture(t, snapshot, port, fresh)
			result, err := controller.Run(context.Background(), expected, InstallOptions{Distribution: expected.Distribution, MCPPort: 18765, AutoStart: true}, nil)
			if state == DistributionHealthy {
				if err != nil || result.Disposition != InstallNoop || fresh.calls != 0 {
					t.Fatalf("healthy result=%#v err=%v fresh=%d", result, err, fresh.calls)
				}
			} else if err == nil || fresh.calls != 0 {
				t.Fatalf("provisioning was mutated: result=%#v err=%v fresh=%d", result, err, fresh.calls)
			}
		})
	}
}

func TestInstallControllerHealthyOlderReleaseRequiresManagedUpgrade(t *testing.T) {
	expected := fixtureExpected()
	port := &fakePortProbe{available: true}
	fresh := &fakeFreshInstaller{}
	snapshot := ExistingSnapshot{
		Distribution: DistributionState{State: DistributionHealthy},
		Windows: WindowsState{
			Present: true, Owned: true, Kind: WindowsStateManifest,
			MCPPort: 18765, AutoStart: true, InstallLocation: `D:\Loki\loki-mcp`,
		},
		StartupTask: StartupTaskState{Present: true, Owned: true},
	}
	controller := controllerFixture(t, snapshot, port, fresh)
	controller.DesiredVersion = "0.1.22"
	runner := controller.Collector.WSL.Runner.(*fakeNativeRunner)
	runner.results = append(runner.results, NativeProbe{Stdout: `{"release":"0.1.19"}`})
	result, err := controller.Run(context.Background(), expected, InstallOptions{
		Distribution: expected.Distribution, MCPPort: 18765, AutoStart: true,
	}, nil)
	if err != nil || result.Disposition != InstallUpgradeRequired || fresh.calls != 0 {
		t.Fatalf("upgrade result=%#v err=%v fresh=%d", result, err, fresh.calls)
	}
	if result.Snapshot.Distribution.Version != "0.1.19" || result.CurrentVersion != "0.1.19" {
		t.Fatalf("upgrade versions base=%q managed=%q", result.Snapshot.Distribution.Version, result.CurrentVersion)
	}
}

func TestInstallControllerHealthyCurrentManagedReleaseIsNoop(t *testing.T) {
	expected := fixtureExpected()
	port := &fakePortProbe{available: true}
	fresh := &fakeFreshInstaller{}
	snapshot := ExistingSnapshot{
		Distribution: DistributionState{State: DistributionHealthy},
		Windows: WindowsState{
			Present: true, Owned: true, Kind: WindowsStateManifest,
			MCPPort: 18765, AutoStart: true, InstallLocation: `D:\Loki\loki-mcp`,
		},
		StartupTask: StartupTaskState{Present: true, Owned: true},
	}
	controller := controllerFixture(t, snapshot, port, fresh)
	controller.DesiredVersion = "0.1.22"
	runner := controller.Collector.WSL.Runner.(*fakeNativeRunner)
	runner.results = append(runner.results, NativeProbe{Stdout: `{"release":"0.1.22"}`})
	result, err := controller.Run(context.Background(), expected, InstallOptions{
		Distribution: expected.Distribution, MCPPort: 18765, AutoStart: true,
	}, nil)
	if err != nil || result.Disposition != InstallNoop || result.CurrentVersion != "0.1.22" || fresh.calls != 0 {
		t.Fatalf("current result=%#v err=%v fresh=%d", result, err, fresh.calls)
	}
}

func TestInstallControllerRejectsPortBeforeStaleRecovery(t *testing.T) {
	expected := fixtureExpected()
	port := &fakePortProbe{available: false}
	fresh := &fakeFreshInstaller{}
	snapshot := ExistingSnapshot{
		Distribution: DistributionState{State: DistributionStale},
		Windows:      WindowsState{Present: true, Owned: true, Kind: WindowsStateManifest, MCPPort: 18765, AutoStart: true, InstallLocation: `D:\Loki\loki-mcp`},
		StartupTask:  StartupTaskState{Present: true, Owned: true},
	}
	controller := controllerFixture(t, snapshot, port, fresh)
	_, err := controller.Run(context.Background(), expected, InstallOptions{
		Distribution: expected.Distribution,
		MCPPort:      19000, MCPPortExplicit: true,
		AutoStart:               true,
		InstallLocation:         `D:\Loki\loki-mcp`,
		InstallLocationExplicit: true,
		ReinstallRequested:      true,
	}, nil)
	var blocked InstallBlockedError
	if !errors.As(err, &blocked) || blocked.Reason != "requested-port-in-use" {
		t.Fatalf("unexpected error %v", err)
	}
	if fresh.calls != 0 {
		t.Fatal("fresh install ran after port conflict")
	}
}

func TestInstallControllerPreservesManifestSettings(t *testing.T) {
	expected := fixtureExpected()
	port := &fakePortProbe{available: true}
	fresh := &fakeFreshInstaller{}
	snapshot := ExistingSnapshot{
		Distribution: DistributionState{State: DistributionHealthy},
		Windows: WindowsState{
			Present: true, Owned: true, Kind: WindowsStateManifest,
			MCPPort: 19000, AutoStart: false, InstallLocation: `D:\Loki\loki-mcp`,
		},
	}
	controller := controllerFixture(t, snapshot, port, fresh)
	result, err := controller.Run(context.Background(), expected, InstallOptions{
		Distribution: expected.Distribution, MCPPort: 18765, AutoStart: true,
	}, nil)
	if err != nil {
		t.Fatal(err)
	}
	if result.Options.MCPPort != 19000 || result.Options.AutoStart ||
		!WindowsPathEqual(result.Options.InstallLocation, `D:\Loki\loki-mcp`) {
		t.Fatalf("settings not preserved %#v", result.Options)
	}
}

func jsonMarshal(value any) ([]byte, error)     { return json.Marshal(value) }
func jsonUnmarshal(raw []byte, value any) error { return json.Unmarshal(raw, value) }
