//go:build windows

package windows

import (
	"context"
	"os"
	"os/exec"
	"os/user"
	"strings"
	"testing"
	"time"
)

func TestWindowsConnectionTaskPrincipalSID(t *testing.T) {
	platform := PowerShellConnectionTaskPlatform{}
	want, err := platform.CurrentUser()
	if err != nil {
		t.Fatal(err)
	}
	current, err := user.Current()
	if err != nil {
		t.Fatal(err)
	}
	for _, identity := range []string{want, current.Username} {
		got, err := resolveConnectionTaskSID(identity)
		if err != nil || got != want {
			t.Fatalf("current-user identity did not resolve to the process SID: %v", err)
		}
	}
	for _, identity := range []string{"", " \t", "invalid\nprincipal"} {
		if _, err := resolveConnectionTaskSID(identity); err == nil {
			t.Fatal("malformed principal accepted")
		}
	}
}

func TestWindowsConnectionTaskNativeRoundTrip(t *testing.T) {
	if os.Getenv("LOKI_WINDOWS_TASK_ACCEPTANCE") != "1" {
		t.Skip("real Task Scheduler mutations require disposable-runner opt-in")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	executable, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	distribution := "loki-task-test-" + acceptanceSecret(t)[:16]
	paths := FrontendPaths{Binary: executable}
	platform := &acceptanceTaskPlatform{delegate: PowerShellConnectionTaskPlatform{}, paths: paths}
	manager := ConnectionTaskManager{Platform: platform}
	expected := expectedConnectionTask(paths, distribution)
	// The randomized task is created only in this disposable test and never
	// started. Cleanup still uses the exact owned executable/action contract.
	t.Cleanup(func() {
		cleanupCtx, cleanupCancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cleanupCancel()
		if err := platform.delegate.Remove(cleanupCtx, expected); err != nil {
			t.Errorf("remove test-owned connection task: %v", err)
		}
	})
	if err := manager.Reconcile(ctx, distribution, true); err != nil {
		t.Fatal(err)
	}
	probe, err := platform.Probe(ctx, expected.TaskName)
	if err != nil {
		t.Fatal(err)
	}
	principal, err := platform.CurrentUser()
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(probe.UserID, "S-") || probe.UserID != principal {
		t.Fatal("Task Scheduler principal is not the current-user SID")
	}
	if err := validateConnectionTaskProbe(probe, expected, principal); err != nil {
		t.Fatal(err)
	}
	// Reproduce the previous release's task settings and exercise the real
	// in-place migration. The owned task is never stopped or executed here.
	command := exec.CommandContext(ctx, platform.delegate.powershell(), "-NoProfile", "-NonInteractive", "-Command",
		`$ErrorActionPreference='Stop';$settings=New-ScheduledTaskSettingsSet -ExecutionTimeLimit (New-TimeSpan -Minutes 10) -StartWhenAvailable -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries;Set-ScheduledTask -TaskName $env:LOKI_TEST_TASK_NAME -Settings $settings -ErrorAction Stop|Out-Null`)
	command.Env = append(withoutEnvironment(os.Environ(), "LOKI_TEST_TASK_NAME"), "LOKI_TEST_TASK_NAME="+expected.TaskName)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("prepare legacy task settings: %v: %s", err, output)
	}
	probe, err = platform.Probe(ctx, expected.TaskName)
	if err != nil || probe.RestartCount != 0 || probe.RestartIntervalTicks != 0 {
		t.Fatalf("legacy retry fixture was not installed: probe=%+v err=%v", probe, err)
	}
	if err := manager.Reconcile(ctx, distribution, true); err != nil {
		t.Fatal(err)
	}
	probe, err = platform.Probe(ctx, expected.TaskName)
	if err != nil {
		t.Fatal(err)
	}
	if err := validateConnectionTaskProbe(probe, expected, principal); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reconcile(ctx, distribution, true); err != nil {
		t.Fatal(err)
	}
	if err := manager.Reconcile(ctx, distribution, false); err != nil {
		t.Fatal(err)
	}
	probe, err = platform.Probe(ctx, expected.TaskName)
	if err != nil || probe.Present {
		t.Fatal("disabled test-owned task remained registered")
	}
}

func TestWindowsConnectionTaskScriptsParse(t *testing.T) {
	for _, script := range []string{connectionTaskProbeScript, createConnectionTaskScript, removeConnectionTaskScript, updateConnectionTaskRetryScript} {
		command := exec.CommandContext(t.Context(), "powershell.exe", "-NoProfile", "-NonInteractive", "-Command",
			`$tokens=$null;$parseErrors=$null;$null=[System.Management.Automation.Language.Parser]::ParseInput($env:LOKI_TEST_TASK_SCRIPT,[ref]$tokens,[ref]$parseErrors);if($parseErrors.Count -gt 0){$parseErrors|Out-String|Write-Error;exit 1}`)
		command.Env = append(withoutEnvironment(os.Environ(), "LOKI_TEST_TASK_SCRIPT"), "LOKI_TEST_TASK_SCRIPT="+script)
		if output, err := command.CombinedOutput(); err != nil {
			t.Fatalf("connection task PowerShell parse failed: %v: %s", err, output)
		}
	}
}
