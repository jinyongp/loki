package windows

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"

	"loki/internal/progress"
)

type runnerCall struct {
	executable string
	arguments  []string
	input      []byte
}

type fakeNativeRunner struct {
	results     []NativeProbe
	errs        []error
	calls       []runnerCall
	resultIndex int
	helpResult  *NativeProbe
	helpErr     error
}

func (runner *fakeNativeRunner) Run(_ context.Context, executable string, arguments []string) (NativeProbe, error) {
	return runner.run(executable, arguments, nil)
}

func (runner *fakeNativeRunner) RunInput(_ context.Context, executable string, arguments []string, input []byte) (NativeProbe, error) {
	return runner.run(executable, arguments, input)
}

func (runner *fakeNativeRunner) run(executable string, arguments []string, input []byte) (NativeProbe, error) {
	runner.calls = append(runner.calls, runnerCall{
		executable: executable, arguments: append([]string(nil), arguments...), input: append([]byte(nil), input...),
	})
	if reflect.DeepEqual(arguments, []string{"--help"}) {
		if runner.helpErr != nil {
			return NativeProbe{}, runner.helpErr
		}
		if runner.helpResult != nil {
			return *runner.helpResult, nil
		}
		return NativeProbe{Stdout: "--from-file --name --no-launch"}, nil
	}
	index := runner.resultIndex
	runner.resultIndex++
	var result NativeProbe
	if index < len(runner.results) {
		result = runner.results[index]
	}
	var err error
	if index < len(runner.errs) {
		err = runner.errs[index]
	}
	return result, err
}

func TestWSLInstallCapabilityGate(t *testing.T) {
	runner := &fakeNativeRunner{}
	client := WSLClient{Runner: runner}
	if err := client.RequireInstallCapabilities(context.Background()); err != nil {
		t.Fatal(err)
	}

	windowsHelp := NativeProbe{
		ExitCode: int(uint64(^uint32(0))),
		Stdout:   "--from-file --name --no-launch",
	}
	runner = &fakeNativeRunner{helpResult: &windowsHelp}
	client.Runner = runner
	if err := client.RequireInstallCapabilities(context.Background()); err != nil {
		t.Fatalf("Windows unsigned -1 help exit was rejected: %v", err)
	}

	missing := NativeProbe{Stdout: "--from-file --no-launch"}
	runner = &fakeNativeRunner{helpResult: &missing}
	client.Runner = runner
	if err := client.RequireInstallCapabilities(context.Background()); err == nil {
		t.Fatal("missing WSL --name capability was accepted")
	}
}

func TestNormalizeNativeExitCodeHandlesWindowsUnsignedMinusOne(t *testing.T) {
	if got := normalizeNativeExitCode(int(uint64(^uint32(0)))); got != -1 {
		t.Fatalf("normalized exit code = %d, want -1", got)
	}
	if got := normalizeNativeExitCode(7); got != 7 {
		t.Fatalf("normalized ordinary exit code = %d, want 7", got)
	}
}

func TestWSLClientRejectsMissingRunner(t *testing.T) {
	client := WSLClient{}
	if _, err := client.ListDistributions(context.Background()); err == nil {
		t.Fatal("nil WSL runner caused no error")
	}
}

func TestWSLProbeUsesExactManagementArgv(t *testing.T) {
	runner := &fakeNativeRunner{results: []NativeProbe{
		{Stdout: `{"generation":{"spec":{"version":"0.1.19"}}}`},
		{Stdout: "loki 0.1.19"},
		{ExitCode: 0},
		{ExitCode: 0},
		{ExitCode: 0, Stdout: `{"schema_version":1}`},
	}}
	client := WSLClient{Runner: runner, Exe: "wsl.exe"}
	probe, err := client.ProbeDistribution(context.Background(), "loki-mcp", true)
	if err != nil {
		t.Fatal(err)
	}
	if got := ClassifyDistribution(probe).State; got != DistributionHealthy {
		t.Fatalf("state=%s", got)
	}
	want := [][]string{
		{"-d", "loki-mcp", "--user", "root", "--exec", "/bin/cat", "/usr/lib/loki-appliance/release-manifest.json"},
		{"-d", "loki-mcp", "--user", "root", "--exec", "/usr/lib/loki-appliance/loki", "version"},
		{"-d", "loki-mcp", "--user", "root", "--exec", "/usr/bin/test", "-f", "/var/lib/loki-appliance/provisioned"},
		{"-d", "loki-mcp", "--user", "root", "--exec", "/usr/local/bin/loki", "host", "doctor", "--system"},
		{"-d", "loki-mcp", "--user", "root", "--exec", "/usr/local/bin/loki", "host", "connection", "--system", "--json"},
	}
	if len(runner.calls) != len(want) {
		t.Fatalf("calls=%#v", runner.calls)
	}
	for index := range want {
		if runner.calls[index].executable != "wsl.exe" || !reflect.DeepEqual(runner.calls[index].arguments, want[index]) {
			t.Fatalf("call %d=%#v want %#v", index, runner.calls[index], want[index])
		}
	}
}

func TestWSLProbeStopsAtForeignIdentity(t *testing.T) {
	runner := &fakeNativeRunner{results: []NativeProbe{{ExitCode: 1}, {ExitCode: 0}}}
	client := WSLClient{Runner: runner}
	probe, err := client.ProbeDistribution(context.Background(), "loki-mcp", true)
	if err != nil {
		t.Fatal(err)
	}
	if ClassifyDistribution(probe).State != DistributionForeign {
		t.Fatal("foreign identity not classified")
	}
	if len(runner.calls) != 2 {
		t.Fatalf("unexpected calls %#v", runner.calls)
	}
}

func TestWSLListAndDestructiveCommands(t *testing.T) {
	runner := &fakeNativeRunner{results: []NativeProbe{
		{Stdout: "Ubuntu\x00\r\nLOKI-MCP\x00\r\n"},
		{},
		{},
	}}
	client := WSLClient{Runner: runner}
	present, err := client.DistributionPresent(context.Background(), "loki-mcp")
	if err != nil || !present {
		t.Fatalf("present=%v err=%v", present, err)
	}
	if err = client.Terminate(context.Background(), "loki-mcp"); err != nil {
		t.Fatal(err)
	}
	if err = client.Unregister(context.Background(), "loki-mcp"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(runner.calls[1].arguments, []string{"--terminate", "loki-mcp"}) ||
		!reflect.DeepEqual(runner.calls[2].arguments, []string{"--unregister", "loki-mcp"}) {
		t.Fatalf("destructive argv=%#v", runner.calls)
	}
}

func TestWSLRunnerStartErrorIsNotExitCode(t *testing.T) {
	runner := &fakeNativeRunner{errs: []error{errors.New("cannot start")}}
	client := WSLClient{Runner: runner}
	if _, err := client.ListDistributions(context.Background()); err == nil {
		t.Fatal("runner start error was ignored")
	}
}

func TestVerifyDistributionIdentityRequiresApprovedVersion(t *testing.T) {
	runner := &fakeNativeRunner{results: []NativeProbe{
		{Stdout: `{"generation":{"spec":{"version":"0.1.19"}}}`},
		{Stdout: "loki 0.1.19"},
	}}
	client := WSLClient{Runner: runner, Exe: "wsl.exe"}
	if err := client.VerifyDistributionIdentity(context.Background(), "loki-mcp", "0.1.19"); err != nil {
		t.Fatal(err)
	}
	want := [][]string{
		{"-d", "loki-mcp", "--user", "root", "--exec", "/bin/cat", "/usr/lib/loki-appliance/release-manifest.json"},
		{"-d", "loki-mcp", "--user", "root", "--exec", "/usr/lib/loki-appliance/loki", "version"},
	}
	for index := range want {
		if !reflect.DeepEqual(runner.calls[index].arguments, want[index]) {
			t.Fatalf("call %d=%#v want %#v", index, runner.calls[index], want[index])
		}
	}

	runner = &fakeNativeRunner{results: []NativeProbe{
		{Stdout: `{"generation":{"spec":{"version":"0.1.20"}}}`},
		{Stdout: "loki 0.1.20"},
	}}
	client.Runner = runner
	if err := client.VerifyDistributionIdentity(context.Background(), "loki-mcp", "0.1.19"); err == nil {
		t.Fatal("changed Loki distribution identity was accepted")
	}
}

func TestWSLIdentityProbeFailuresPreserveNativeCause(t *testing.T) {
	for _, operation := range []string{"owned-version", "verify"} {
		for _, failed := range []string{"manifest", "version"} {
			t.Run(operation+"/"+failed, func(t *testing.T) {
				failure := NativeProbe{ExitCode: 7, Stderr: "WSL instance is shutting down"}
				results := []NativeProbe{failure}
				if failed == "version" {
					results = []NativeProbe{{Stdout: ownedManifest("1.2.3")}, failure}
				}
				runner := &fakeNativeRunner{results: results}
				client := WSLClient{Runner: runner}
				var err error
				if operation == "verify" {
					err = client.VerifyDistributionIdentity(t.Context(), "loki-test", "1.2.3")
				} else {
					_, err = client.OwnedDistributionVersion(t.Context(), "loki-test")
				}
				if err == nil || !strings.Contains(err.Error(), "identity "+failed) || !strings.Contains(err.Error(), "exit code 7") || !strings.Contains(err.Error(), failure.Stderr) || strings.Contains(err.Error(), "does not match") {
					t.Fatalf("native identity failure cause lost: %v", err)
				}
				if len(runner.calls) != len(results) {
					t.Fatal("failed identity read continued probing")
				}
			})
		}
	}
}

func TestNativeProgressRelayHandlesChunkedRecordEndings(t *testing.T) {
	var out bytes.Buffer
	relay := newNativeProgressRelay(progress.NewLineReporter(progress.WithVerbose(&out)))
	if _, err := relay.Write([]byte("ordinary stderr\n[loki] Preparing")); err != nil {
		t.Fatal(err)
	}
	if _, err := relay.Write([]byte(" update...\r[loki] Downloading...\r")); err != nil {
		t.Fatal(err)
	}
	if _, err := relay.Write([]byte("\nwarning\r\n[loki] Applying update...")); err != nil {
		t.Fatal(err)
	}
	relay.Flush()
	if got, want := out.String(), "[loki] Preparing update...\n[loki] Downloading...\n[loki] Applying update...\n"; got != want {
		t.Fatalf("progress=%q want=%q", got, want)
	}
	if strings.ContainsRune(out.String(), '\r') {
		t.Fatalf("progress retained carriage return: %q", out.String())
	}
}
