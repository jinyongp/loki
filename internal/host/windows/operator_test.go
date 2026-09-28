package windows

import (
	"context"
	"strings"
	"testing"
)

func ownedManifest(version string) string {
	return `{"generation":{"spec":{"version":"` + version + `"}}}`
}

func TestOperatorClientUsesExactArgvAndValidatesSchema(t *testing.T) {
	runner := &fakeNativeRunner{results: []NativeProbe{
		{ExitCode: 0, Stdout: ownedManifest("1.2.3")},
		{ExitCode: 0, Stdout: "loki 1.2.3"},
		{ExitCode: 0, Stdout: `{"schema_version":1,"state":"installed","release":"1.2.4","update_available":false,"update_prepared":false}`},
	}}
	client := OperatorClient{WSL: WSLClient{Runner: runner}}
	result, err := client.Execute(t.Context(), "loki-mcp", OperatorRequest{Command: "status"})
	if err != nil {
		t.Fatal(err)
	}
	if result.DistributionVersion != "1.2.3" || result.Probe.ExitCode != 0 {
		t.Fatalf("result=%+v", result)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("calls=%+v", runner.calls)
	}
	want := []string{
		"-d", "loki-mcp", "--user", "root", "--exec", "/usr/local/bin/loki",
		"host", "status", "--system", "--json",
	}
	if got := runner.calls[2].arguments; strings.Join(got, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("argv=%q want=%q", got, want)
	}
}

func TestOperatorClientRejectsMissingOrUnsupportedSchema(t *testing.T) {
	for name, payload := range map[string]string{
		"missing":     `{"state":"installed"}`,
		"unsupported": `{"schema_version":2,"state":"installed"}`,
	} {
		t.Run(name, func(t *testing.T) {
			runner := &fakeNativeRunner{results: []NativeProbe{
				{ExitCode: 0, Stdout: ownedManifest("1.2.3")},
				{ExitCode: 0, Stdout: "loki 1.2.3"},
				{ExitCode: 0, Stdout: payload},
			}}
			client := OperatorClient{WSL: WSLClient{Runner: runner}}
			if _, err := client.Execute(t.Context(), "loki-mcp", OperatorRequest{Command: "status"}); err == nil {
				t.Fatal("invalid operator schema accepted")
			}
		})
	}
}

func TestOperatorArgumentsKeepRestoreIDAsSingleArg(t *testing.T) {
	args, machine, err := operatorCommandArguments(OperatorRequest{
		Command: "restore", BackupID: "backup id ; $(ignored)", InterruptActiveJobs: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if machine {
		t.Fatal("restore unexpectedly marked machine output")
	}
	want := []string{"host", "restore", "--system", "--interrupt-active-jobs", "backup id ; $(ignored)"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args=%q want=%q", args, want)
	}
}

func TestOperatorUpdateApplyRequiresExplicitApproval(t *testing.T) {
	if _, _, err := operatorCommandArguments(OperatorRequest{Command: "update", Action: "apply"}); err == nil {
		t.Fatal("update apply without approval accepted")
	}
	args, _, err := operatorCommandArguments(OperatorRequest{
		Command: "update", Action: "apply", Approve: true, InterruptActiveJobs: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"host", "update", "apply", "--system", "--approve", "--interrupt-active-jobs"}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args=%q want=%q", args, want)
	}
}

func TestManagedReleaseVersionReadsHostStatusRelease(t *testing.T) {
	runner := &fakeNativeRunner{results: []NativeProbe{
		{ExitCode: 0, Stdout: `{"release":"0.1.19"}`},
	}}
	version, err := (WSLClient{Runner: runner}).ManagedReleaseVersion(context.Background(), "loki-mcp")
	if err != nil || version != "0.1.19" {
		t.Fatalf("managed release version=%q err=%v", version, err)
	}
	want := []string{"-d", "loki-mcp", "--user", "root", "--exec",
		"/usr/local/bin/loki", "host", "status", "--system", "--json"}
	if len(runner.calls) != 1 || strings.Join(runner.calls[0].arguments, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("managed release argv=%#v", runner.calls)
	}
}

func TestOwnedDistributionVersionRejectsForeignIdentity(t *testing.T) {
	runner := &fakeNativeRunner{results: []NativeProbe{
		{ExitCode: 0, Stdout: ownedManifest("1.2.3")},
		{ExitCode: 0, Stdout: "foreign 1.2.3"},
	}}
	_, err := (WSLClient{Runner: runner}).OwnedDistributionVersion(context.Background(), "loki-mcp")
	if err == nil {
		t.Fatal("foreign identity accepted")
	}
}
