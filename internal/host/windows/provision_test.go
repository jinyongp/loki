package windows

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeProcessStarter struct {
	executable string
	arguments  []string
	err        error
}

func (starter *fakeProcessStarter) Start(executable string, arguments []string) error {
	starter.executable = executable
	starter.arguments = append([]string(nil), arguments...)
	return starter.err
}

func noSleep(context.Context, time.Duration) error { return nil }

func TestWSLFreshProvisionerRegisterArgv(t *testing.T) {
	runner := &fakeNativeRunner{results: []NativeProbe{{ExitCode: 0}}}
	provisioner := WSLFreshProvisioner{Client: WSLClient{Runner: runner}}
	created, err := provisioner.RegisterDistribution(t.Context(), fixtureExpected(), InstallOptions{
		InstallLocation: "D:\\Loki\\loki-mcp",
	}, PreparedAppliance{Path: "C:\\Temp\\loki.wsl"})
	if err != nil || !created {
		t.Fatalf("created=%v err=%v", created, err)
	}
	want := []string{"--install", "--from-file", "C:\\Temp\\loki.wsl", "--name", "loki-mcp", "--no-launch", "--location", "D:\\Loki\\loki-mcp"}
	if !reflect.DeepEqual(runner.calls[0].arguments, want) {
		t.Fatalf("argv=%#v want=%#v", runner.calls[0].arguments, want)
	}
}

func TestWSLFreshProvisionerDoesNotClaimFailedRegistrationByName(t *testing.T) {
	runner := &fakeNativeRunner{results: []NativeProbe{{ExitCode: 1, Stderr: "synthetic failure"}}}
	provisioner := WSLFreshProvisioner{Client: WSLClient{Runner: runner}}
	created, err := provisioner.RegisterDistribution(t.Context(), fixtureExpected(), InstallOptions{}, PreparedAppliance{Path: "fixture.wsl"})
	if err == nil || created {
		t.Fatalf("failed registration claimed ownership: created=%v err=%v calls=%#v", created, err, runner.calls)
	}
	if len(runner.calls) != 1 {
		t.Fatalf("failed registration performed name-based ownership probing: %#v", runner.calls)
	}
}

func TestWSLFreshProvisionerReturnsVerifiedConnectionMaterial(t *testing.T) {
	runner := &fakeNativeRunner{results: []NativeProbe{
		{ExitCode: 0},
		{ExitCode: 0, Stdout: "ActiveState=active\nSubState=exited\nNRestarts=0\n"},
		{ExitCode: 0, Stdout: "{\"schema_version\":1}"},
		{ExitCode: 0},
		{ExitCode: 0, Stdout: "{\"schema_version\":1,\"local_origin\":{\"url\":\"http://127.0.0.1:19000/mcp\",\"transport\":\"streamable-http\",\"reachability\":\"loopback\",\"authentication\":{\"type\":\"bearer-token-file\"}}}"},
		{ExitCode: 0, Stdout: "secret-token"},
	}}
	starter := &fakeProcessStarter{}
	provisioner := WSLFreshProvisioner{
		Client: WSLClient{Runner: runner}, Starter: starter, Sleep: noSleep, Attempts: 2,
	}
	material, err := provisioner.Provision(t.Context(), fixtureExpected(), InstallOptions{MCPPort: 19000})
	if err != nil {
		t.Fatal(err)
	}
	if material.LocalOrigin != "http://127.0.0.1:19000/mcp" ||
		material.AuthenticationType != "bearer-token-file" || material.Token != "secret-token" {
		t.Fatalf("material=%#v", material)
	}
	if starter.executable != "wsl.exe" ||
		!reflect.DeepEqual(starter.arguments, []string{"-d", "loki-mcp", "--exec", "/usr/bin/sleep", "infinity"}) {
		t.Fatalf("starter=%s %#v", starter.executable, starter.arguments)
	}
}

func TestWSLFreshProvisionerRejectsRepeatedProvisionFailure(t *testing.T) {
	runner := &fakeNativeRunner{results: []NativeProbe{
		{ExitCode: 0},
		{ExitCode: 0, Stdout: "ActiveState=failed\nSubState=failed\nResult=exit-code\nNRestarts=3\nExecMainStatus=1\n"},
	}}
	provisioner := WSLFreshProvisioner{
		Client: WSLClient{Runner: runner}, Starter: &fakeProcessStarter{}, Sleep: noSleep, Attempts: 2,
	}
	_, err := provisioner.Provision(t.Context(), fixtureExpected(), InstallOptions{MCPPort: 19000})
	var failure *ProvisioningFailureError
	if !errors.As(err, &failure) || failure.Kind != ProvisioningFailureUnknown {
		t.Fatalf("unexpected repeated provisioning failure: %v", err)
	}
}

func TestWSLFreshProvisionerClassifiesPortConflictWithoutJournalDump(t *testing.T) {
	runner := &fakeNativeRunner{results: []NativeProbe{
		{ExitCode: 0},
		{ExitCode: 0, Stdout: "ActiveState=failed\nSubState=failed\nResult=exit-code\nNRestarts=3\nExecMainStatus=1\n"},
		{ExitCode: 0, Stdout: "docker: failed to bind host port 127.0.0.1:19000/tcp: address already in use"},
	}}
	provisioner := WSLFreshProvisioner{
		Client: WSLClient{Runner: runner}, Starter: &fakeProcessStarter{}, Sleep: noSleep, Attempts: 2,
	}
	_, err := provisioner.Provision(t.Context(), fixtureExpected(), InstallOptions{MCPPort: 19000})
	var failure *ProvisioningFailureError
	if !errors.As(err, &failure) || failure.Kind != ProvisioningFailurePortInUse || failure.Port != 19000 {
		t.Fatalf("unexpected port conflict: %#v err=%v", failure, err)
	}
	if failure.Diagnostics != "" || strings.Contains(err.Error(), "docker:") {
		t.Fatalf("recognized port conflict leaked journal diagnostics: %#v", failure)
	}
	wantJournal := []string{
		"-d", "loki-mcp", "--user", "root", "--exec",
		"/usr/bin/journalctl", "-u", "loki-appliance-provision.service", "--no-pager", "-n", "24",
	}
	if len(runner.calls) != 3 || !reflect.DeepEqual(runner.calls[2].arguments, wantJournal) {
		t.Fatalf("journal argv=%#v", runner.calls)
	}
}

func TestWSLFreshProvisionerRetainsBoundedUnknownDiagnostics(t *testing.T) {
	diagnostics := "unknown failure: " + strings.Repeat("x", provisioningDiagnosticMaxBytes*2)
	runner := &fakeNativeRunner{results: []NativeProbe{
		{ExitCode: 0},
		{ExitCode: 0, Stdout: "ActiveState=failed\nSubState=failed\nResult=exit-code\nNRestarts=3\nExecMainStatus=1\n"},
		{ExitCode: 0, Stdout: diagnostics},
	}}
	provisioner := WSLFreshProvisioner{
		Client: WSLClient{Runner: runner}, Starter: &fakeProcessStarter{}, Sleep: noSleep, Attempts: 2,
	}
	_, err := provisioner.Provision(t.Context(), fixtureExpected(), InstallOptions{MCPPort: 19000})
	var failure *ProvisioningFailureError
	if !errors.As(err, &failure) || failure.Kind != ProvisioningFailureUnknown {
		t.Fatalf("unexpected unknown provisioning failure: %#v err=%v", failure, err)
	}
	if failure.Diagnostics == "" || len(failure.Diagnostics) > provisioningDiagnosticMaxBytes ||
		!strings.Contains(failure.Diagnostics, "[diagnostics truncated]") {
		t.Fatalf("diagnostics were not bounded: len=%d value=%q", len(failure.Diagnostics), failure.Diagnostics)
	}
	if !strings.Contains(err.Error(), "Recent provisioning diagnostics:") {
		t.Fatalf("unknown failure omitted diagnostics: %v", err)
	}
}
