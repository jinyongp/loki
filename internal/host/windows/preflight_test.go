package windows

import (
	"context"
	"encoding/json"
	"testing"
)

type fakeTaskSource struct {
	probe StartupTaskProbe
	err   error
}

func (source fakeTaskSource) Probe(context.Context, string) (StartupTaskProbe, error) {
	return source.probe, source.err
}

func TestPreflightCollectorAdoptsHealthyV019State(t *testing.T) {
	expected := fixtureExpected()
	ownership := joinWindowsPath(expected.StateDir, "ownership.json")
	fs := fakeStateFilesystem{
		paths: map[string]StatePath{
			expected.StateDir:  {Exists: true, Directory: true},
			ownership:          {Exists: true, Regular: true},
			`D:\Loki\loki-mcp`: {Exists: true, Directory: true},
		},
		dirs:  map[string][]string{expected.StateDir: {"connection.json", "mcp-token", "ownership.json"}},
		files: map[string][]byte{ownership: manifestFixture(t, expected)},
		errs:  map[string]error{},
	}
	runner := &fakeNativeRunner{results: []NativeProbe{
		{Stdout: "loki-mcp"},
		{Stdout: `{"generation":{"spec":{"version":"0.1.19"}}}`},
		{Stdout: "loki 0.1.19"},
		{ExitCode: 0},
		{ExitCode: 0},
		{ExitCode: 0, Stdout: `{"schema_version":1}`},
	}}
	collector := PreflightCollector{
		Filesystem: fs,
		Tasks: fakeTaskSource{probe: StartupTaskProbe{
			Present:     true,
			Description: "Keep the Loki WSL2 appliance running.",
			Actions:     []StartupTaskAction{{Executable: expected.TaskExecutable, Arguments: expected.TaskArguments}},
		}},
		WSL: WSLClient{Runner: runner},
	}
	snapshot, err := collector.Collect(context.Background(), expected)
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Distribution.State != DistributionHealthy || !snapshot.Windows.Owned || !snapshot.StartupTask.Owned {
		raw, _ := json.Marshal(snapshot)
		t.Fatalf("unexpected snapshot %s", raw)
	}
	if assessment := AssessExistingInstallation(snapshot); assessment.Action != ExistingHealthyNoop {
		t.Fatalf("unexpected assessment %#v", assessment)
	}
}

func TestPreflightCollectorDoesNotDowngradeAdapterErrorsToAbsent(t *testing.T) {
	expected := fixtureExpected()
	collector := PreflightCollector{
		Filesystem: fakeStateFilesystem{paths: map[string]StatePath{}, dirs: map[string][]string{}, files: map[string][]byte{}, errs: map[string]error{}},
		Tasks:      fakeTaskSource{},
		WSL:        WSLClient{Runner: &fakeNativeRunner{errs: []error{context.DeadlineExceeded}}},
	}
	if _, err := collector.Collect(context.Background(), expected); err == nil {
		t.Fatal("WSL adapter error was ignored")
	}
}
