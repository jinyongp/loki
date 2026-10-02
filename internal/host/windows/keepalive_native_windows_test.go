//go:build windows

package windows

import (
	"os"
	"os/exec"
	"strings"
	"testing"
)

// Exercise the actual PowerShell task script without altering operator tasks.
func TestWindowsKeepaliveTaskScript(t *testing.T) {
	for _, test := range []struct {
		name, fixture, wantError string
	}{
		{name: "running", fixture: `$script:taskState='Running';function Start-ScheduledTask {throw 'running task was restarted'}`},
		{name: "configured retry", fixture: `$script:taskState='Running';$script:retryCount=2;$script:retryInterval='PT2M';function Set-ScheduledTask {throw 'configured retries were overwritten'};function Start-ScheduledTask {throw 'running task was restarted'}`},
		{name: "stopped", fixture: `$script:taskState='Ready';function Start-ScheduledTask {$script:taskState='Running'}`},
		{name: "failed", fixture: `$script:taskState='Ready';function Start-ScheduledTask {};function Get-ScheduledTaskInfo {[pscustomobject]@{LastTaskResult=2}}`, wantError: "last exit code: 2"},
		{name: "foreign", fixture: `$script:taskState='Ready';$script:taskArgs='foreign';function Start-ScheduledTask {throw 'foreign task was started'}`, wantError: "no longer matches Loki WSL ownership"},
		{name: "retry migration failure", fixture: `$script:taskState='Ready';function Set-ScheduledTask {};function Start-ScheduledTask {throw 'task started after failed migration'}`, wantError: "retry settings could not be restored"},
	} {
		t.Run(test.name, func(t *testing.T) {
			fixture := `$script:taskArgs=$env:LOKI_KEEPALIVE_TASK_ARGS
$script:retryCount=0
$script:retryInterval=$null
function Get-ScheduledTask {
  [pscustomobject]@{State=$script:taskState;Description='Keep the Loki WSL2 appliance running.';Actions=@([pscustomobject]@{Execute=$env:LOKI_KEEPALIVE_TASK_EXE;Arguments=$script:taskArgs});Settings=[pscustomobject]@{RestartCount=$script:retryCount;RestartInterval=$script:retryInterval}}
}
function Set-ScheduledTask {
  param($TaskName,$Settings,$ErrorAction)
  if($Settings.RestartCount -ne 3 -or $Settings.RestartInterval -ne 'PT1M'){throw 'incorrect migration settings'}
  $script:retryCount=$Settings.RestartCount
  $script:retryInterval=$Settings.RestartInterval
}
function Start-Sleep {}
` + test.fixture + "\n" + startVerifiedKeepaliveTaskScript
			command := exec.CommandContext(t.Context(), "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", fixture)
			command.Env = append(withoutEnvironment(os.Environ(), "LOKI_KEEPALIVE_TASK_NAME", "LOKI_KEEPALIVE_TASK_EXE", "LOKI_KEEPALIVE_TASK_ARGS"),
				"LOKI_KEEPALIVE_TASK_NAME=Loki WSL (loki-test)",
				`LOKI_KEEPALIVE_TASK_EXE=C:\Windows\System32\wsl.exe`,
				"LOKI_KEEPALIVE_TASK_ARGS=-d loki-test --exec /usr/bin/sleep infinity")
			output, err := command.CombinedOutput()
			if test.wantError == "" {
				if err != nil {
					t.Fatalf("task script failed: %v: %s", err, output)
				}
			} else if err == nil || !strings.Contains(string(output), test.wantError) {
				t.Fatalf("task script did not diagnose %q: %v: %s", test.wantError, err, output)
			}
		})
	}
}
