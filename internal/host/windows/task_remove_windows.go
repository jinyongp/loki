//go:build windows

package windows

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const removeOwnedStartupTaskScript = `$ErrorActionPreference='Stop'
$t=Get-ScheduledTask -TaskName $env:LOKI_TASK_NAME -ErrorAction SilentlyContinue
if(-not $t){exit 0}
$a=@($t.Actions)
$current=$a.Count -eq 1 -and ([string]$a[0].Execute).Equals($env:LOKI_TASK_EXE,[StringComparison]::OrdinalIgnoreCase) -and ([string]$a[0].Arguments).Equals($env:LOKI_TASK_ARGS,[StringComparison]::Ordinal)
$legacy=$a.Count -eq 1 -and $env:LOKI_TASK_LEGACY_EXE -and ([string]$a[0].Execute).Equals($env:LOKI_TASK_LEGACY_EXE,[StringComparison]::OrdinalIgnoreCase) -and ([string]$a[0].Arguments).Equals($env:LOKI_TASK_LEGACY_ARGS,[StringComparison]::Ordinal)
$hidden=$a.Count -eq 1 -and $env:LOKI_TASK_HIDDEN_EXE -and ([string]$a[0].Execute).Equals($env:LOKI_TASK_HIDDEN_EXE,[StringComparison]::OrdinalIgnoreCase) -and ([string]$a[0].Arguments).Equals($env:LOKI_TASK_HIDDEN_ARGS,[StringComparison]::Ordinal)
if((-not $current -and -not $legacy -and -not $hidden) -or -not ([string]$t.Description).Equals('Keep the Loki WSL2 appliance running.',[StringComparison]::Ordinal)){throw 'Scheduled Task no longer matches Loki ownership'}
Stop-ScheduledTask -TaskName $env:LOKI_TASK_NAME -ErrorAction SilentlyContinue
Unregister-ScheduledTask -TaskName $env:LOKI_TASK_NAME -Confirm:$false -ErrorAction Stop`

func (source PowerShellStartupTaskSource) RemoveOwned(ctx context.Context, expected ExpectedInstallation) error {
	executable := strings.TrimSpace(source.Exe)
	if executable == "" {
		executable = "powershell.exe"
	}
	command := exec.CommandContext(ctx, executable, "-NoProfile", "-NonInteractive", "-Command", removeOwnedStartupTaskScript)
	configureNativeProcess(command)
	command.Env = append(withoutEnvironment(os.Environ(),
		"LOKI_TASK_NAME", "LOKI_TASK_EXE", "LOKI_TASK_ARGS", "LOKI_TASK_LEGACY_EXE", "LOKI_TASK_LEGACY_ARGS", "LOKI_TASK_HIDDEN_EXE", "LOKI_TASK_HIDDEN_ARGS"),
		"LOKI_TASK_NAME="+expected.TaskName,
		"LOKI_TASK_EXE="+expected.TaskExecutable,
		"LOKI_TASK_ARGS="+expected.TaskArguments,
		"LOKI_TASK_LEGACY_EXE="+expected.LegacyTaskExecutable,
		"LOKI_TASK_LEGACY_ARGS="+expected.LegacyTaskArguments,
		"LOKI_TASK_HIDDEN_EXE="+expected.LegacyHiddenTaskExecutable,
		"LOKI_TASK_HIDDEN_ARGS="+expected.LegacyHiddenTaskArguments,
	)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		return fmt.Errorf("remove owned Scheduled Task %q: %w: %s", expected.TaskName, err, detail)
	}
	return nil
}
