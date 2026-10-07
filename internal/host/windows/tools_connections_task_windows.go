//go:build windows

package windows

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
)

type ToolsConnectionTasks struct{ Root, Binary string }

const toolsConnectionTaskScript = `$ErrorActionPreference='Stop'
$t=Get-ScheduledTask -TaskName $env:LOKI_TASK_NAME -ErrorAction SilentlyContinue
if($t){
  $a=@($t.Actions)
  $owner=[string]$t.Principal.UserId
  if($owner -notmatch '^S-1-'){$owner=(New-Object Security.Principal.NTAccount($owner)).Translate([Security.Principal.SecurityIdentifier]).Value}
  if($a.Count -ne 1 -or -not ([string]$a[0].Execute).Equals($env:LOKI_TASK_EXE,[StringComparison]::OrdinalIgnoreCase) -or -not ([string]$a[0].Arguments).Equals($env:LOKI_TASK_ARGS,[StringComparison]::Ordinal) -or [string]$t.Description -ne 'Restore enabled Loki tool connections.' -or $owner -ne $env:LOKI_TASK_USER -or [string]$t.Principal.RunLevel -ne 'Limited'){throw 'Scheduled task does not match Loki tool connection ownership'}
}
if($env:LOKI_TASK_STOP -eq '1'){
  if($t){Stop-ScheduledTask -TaskName $env:LOKI_TASK_NAME -ErrorAction SilentlyContinue};exit 0
}
if($env:LOKI_TASK_ENABLE -ne '1'){
  if($t){Stop-ScheduledTask -TaskName $env:LOKI_TASK_NAME -ErrorAction SilentlyContinue;Unregister-ScheduledTask -TaskName $env:LOKI_TASK_NAME -Confirm:$false};exit 0
}
$a=New-ScheduledTaskAction -Execute $env:LOKI_TASK_EXE -Argument $env:LOKI_TASK_ARGS
$trigger=New-ScheduledTaskTrigger -AtLogOn -User $env:LOKI_TASK_USER
$principal=New-ScheduledTaskPrincipal -UserId $env:LOKI_TASK_USER -LogonType Interactive -RunLevel Limited
$settings=New-ScheduledTaskSettingsSet -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries -StartWhenAvailable -ExecutionTimeLimit (New-TimeSpan -Minutes 10) -RestartCount 3 -RestartInterval (New-TimeSpan -Minutes 1)
if($t){Set-ScheduledTask -TaskName $env:LOKI_TASK_NAME -Action $a -Trigger $trigger -Principal $principal -Settings $settings|Out-Null}
else{Register-ScheduledTask -TaskName $env:LOKI_TASK_NAME -Action $a -Trigger $trigger -Principal $principal -Settings $settings -Description 'Restore enabled Loki tool connections.'|Out-Null}`

func (tasks ToolsConnectionTasks) Reconcile(ctx context.Context, distribution string, enabled bool) error {
	if err := ValidateDistributionName(distribution); err != nil {
		return err
	}
	if !filepath.IsAbs(tasks.Root) || !filepath.IsAbs(tasks.Binary) {
		return fmt.Errorf("connection startup requires the installed frontend")
	}
	state := filepath.Join(tasks.Root, "control", "connections", "startup", distribution)
	if err := (WindowsFrontendPlatform{}).EnsurePrivateDirectory(ctx, state); err != nil {
		return err
	}
	expected := ExpectedInstallation{Distribution: distribution, StateDir: state, TaskExecutable: filepath.Join(state, "loki-keepalive.exe")}
	expected.TaskName = fmt.Sprintf("Loki tools connections (%x %s)", sha256.Sum256([]byte(filepath.Clean(tasks.Root))), distribution)
	var args []string
	for _, arg := range []string{"--distribution", distribution, "--frontend", tasks.Binary, "--root", tasks.Root} {
		args = append(args, syscall.EscapeArg(arg))
	}
	expected.TaskArguments = strings.Join(args, " ")
	user, err := (PowerShellConnectionTaskPlatform{}).CurrentUser()
	if err != nil {
		return err
	}
	run := func(stop bool) error {
		enable, stopping := "0", "0"
		if enabled {
			enable = "1"
		}
		if stop {
			stopping = "1"
		}
		command := exec.CommandContext(ctx, "powershell.exe", "-NoProfile", "-NonInteractive", "-Command", toolsConnectionTaskScript)
		configureNativeProcess(command)
		command.Env = append(withoutEnvironment(os.Environ(), "LOKI_TASK_NAME", "LOKI_TASK_EXE", "LOKI_TASK_ARGS", "LOKI_TASK_USER", "LOKI_TASK_ENABLE", "LOKI_TASK_STOP"), "LOKI_TASK_NAME="+expected.TaskName, "LOKI_TASK_EXE="+expected.TaskExecutable, "LOKI_TASK_ARGS="+expected.TaskArguments, "LOKI_TASK_USER="+user, "LOKI_TASK_ENABLE="+enable, "LOKI_TASK_STOP="+stopping)
		output, err := command.CombinedOutput()
		if err != nil {
			return fmt.Errorf("managed connection startup task failed: %w: %s", err, strings.TrimSpace(string(output)))
		}
		return nil
	}
	if enabled {
		if err := ensureKeepaliveExecutable(ctx, expected, func() error { return run(true) }); err != nil {
			return err
		}
	}
	return run(false)
}
