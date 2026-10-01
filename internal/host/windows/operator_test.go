package windows

import (
	"bytes"
	"context"
	"slices"
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

func TestOperatorIntegrationSetupUsesStdinWithoutCredentialArgv(t *testing.T) {
	secret := []byte("-----BEGIN PRIVATE KEY-----\nsecret\n-----END PRIVATE KEY-----\n")
	runner := &fakeNativeRunner{results: []NativeProbe{
		{ExitCode: 0, Stdout: ownedManifest("1.2.3")},
		{ExitCode: 0, Stdout: "loki 1.2.3"},
		{ExitCode: 0, Stdout: "GitHub App integration is configured."},
	}}
	client := OperatorClient{WSL: WSLClient{Runner: runner}}
	_, err := client.ExecuteInput(t.Context(), "loki-mcp", OperatorRequest{
		Command: "integration", Action: "setup", Integration: "github", UseStdin: true,
	}, secret)
	if err != nil {
		t.Fatal(err)
	}
	if len(runner.calls) != 3 {
		t.Fatalf("calls=%#v", runner.calls)
	}
	call := runner.calls[2]
	if !bytes.Equal(call.input, secret) {
		t.Fatalf("stdin=%q want=%q", call.input, secret)
	}
	joined := strings.Join(call.arguments, "\x00")
	if strings.Contains(joined, "secret") || strings.Contains(joined, "PRIVATE KEY") {
		t.Fatalf("credential leaked into argv: %q", call.arguments)
	}
	want := []string{
		"-d", "loki-mcp", "--user", "root", "--exec", "/usr/local/bin/loki",
		"host", "integration", "setup", "--system", "--stdin", "github",
	}
	if joined != strings.Join(want, "\x00") {
		t.Fatalf("argv=%q want=%q", call.arguments, want)
	}
}

func TestOperatorSigningSetupKeepsIdentityInArgvAndKeyOnStdin(t *testing.T) {
	args, machine, err := operatorCommandArguments(OperatorRequest{
		Command: "integration", Action: "setup", Integration: "signing",
		IdentityName: "Signing User", IdentityEmail: "signing@example.test", UseStdin: true,
	})
	if err != nil {
		t.Fatal(err)
	}
	if machine {
		t.Fatal("signing setup unexpectedly marked machine output")
	}
	want := []string{
		"host", "integration", "setup", "--system",
		"--identity-name", "Signing User", "--identity-email", "signing@example.test",
		"--key-stdin", "signing",
	}
	if strings.Join(args, "\x00") != strings.Join(want, "\x00") {
		t.Fatalf("args=%q want=%q", args, want)
	}
}

func TestOperatorIntegrationInspectionUsesMachineSchema(t *testing.T) {
	for _, action := range []string{"list", "status", "doctor"} {
		request := OperatorRequest{Command: "integration", Action: action}
		if action != "list" {
			request.Integration = "browser"
		}
		args, machine, err := operatorCommandArguments(request)
		if err != nil {
			t.Fatalf("%s: %v", action, err)
		}
		if !machine || !slices.Contains(args, "--json") {
			t.Fatalf("%s args=%q machine=%t", action, args, machine)
		}
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

func TestOperatorGitHubBrowserRelayKeepsOneTimeCodeOnStdin(t *testing.T) {
	args, machine, err := operatorCommandArguments(OperatorRequest{Command: "integration", Action: "setup", Integration: "github", UseStdin: true, GitHubBrowser: true})
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"host", "integration", "setup", "--system", "--browser-request", "github"}
	if !machine || !slices.Equal(args, want) {
		t.Fatalf("args=%q machine=%t", args, machine)
	}
	for _, bad := range []OperatorRequest{
		{Command: "status", GitHubBrowser: true},
		{Command: "integration", Action: "rotate", Integration: "github", UseStdin: true, GitHubBrowser: true},
		{Command: "integration", Action: "setup", Integration: "signing", UseStdin: true, GitHubBrowser: true},
		{Command: "integration", Action: "setup", Integration: "github", GitHubBrowser: true},
		{Command: "integration", Action: "setup", Integration: "github", UseStdin: true, GitHubBrowser: true, IdentityName: "user"},
	} {
		if _, _, err := operatorCommandArguments(bad); err == nil {
			t.Fatalf("accepted %+v", bad)
		}
	}
	code := []byte("{\"action\":\"exchange\",\"state\":\"state\",\"code\":\"one-time-secret\"}")
	runner := &fakeNativeRunner{results: []NativeProbe{{ExitCode: 0, Stdout: ownedManifest("1.2.3")}, {ExitCode: 0, Stdout: "loki 1.2.3"}, {ExitCode: 0, Stdout: `{"schema_version":1,"phase":"installation"}`}}}
	_, err = (OperatorClient{WSL: WSLClient{Runner: runner}}).ExecuteInput(t.Context(), "loki-mcp", OperatorRequest{Command: "integration", Action: "setup", Integration: "github", UseStdin: true, GitHubBrowser: true}, code)
	if err != nil {
		t.Fatal(err)
	}
	call := runner.calls[2]
	if !bytes.Equal(call.input, code) || strings.Contains(strings.Join(call.arguments, " "), "one-time-secret") {
		t.Fatal("one-time code was not isolated to stdin")
	}
}
