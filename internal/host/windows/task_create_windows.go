//go:build windows

package windows

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strings"
)

const createOwnedStartupTaskScript = `$ErrorActionPreference='Stop';$existing=Get-ScheduledTask -TaskName $env:LOKI_TASK_NAME -ErrorAction SilentlyContinue;if($existing){throw 'Scheduled Task already exists'};$action=New-ScheduledTaskAction -Execute $env:LOKI_TASK_EXE -Argument $env:LOKI_TASK_ARGS;$trigger=New-ScheduledTaskTrigger -AtLogOn -User $env:LOKI_TASK_USER;$settings=New-ScheduledTaskSettingsSet -ExecutionTimeLimit ([TimeSpan]::Zero) -StartWhenAvailable -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries;Register-ScheduledTask -TaskName $env:LOKI_TASK_NAME -Action $action -Trigger $trigger -Settings $settings -Description 'Keep the Loki WSL2 appliance running.' -ErrorAction Stop|Out-Null`

func (source PowerShellStartupTaskSource) CreateOwned(ctx context.Context, expected ExpectedInstallation) (bool, error) {
	current, err := user.Current()
	if err != nil {
		return false, fmt.Errorf("resolve current Windows user: %w", err)
	}
	executable := strings.TrimSpace(source.Exe)
	if executable == "" {
		executable = "powershell.exe"
	}
	command := exec.CommandContext(ctx, executable, "-NoProfile", "-NonInteractive", "-Command", createOwnedStartupTaskScript)
	command.Env = append(withoutEnvironment(os.Environ(),
		"LOKI_TASK_NAME", "LOKI_TASK_EXE", "LOKI_TASK_ARGS", "LOKI_TASK_USER"),
		"LOKI_TASK_NAME="+expected.TaskName,
		"LOKI_TASK_EXE="+expected.TaskExecutable,
		"LOKI_TASK_ARGS="+expected.TaskArguments,
		"LOKI_TASK_USER="+current.Username,
	)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		probe, probeErr := source.Probe(ctx, expected.TaskName)
		if probeErr == nil && ClassifyStartupTask(probe, expected).Owned {
			return true, fmt.Errorf("create Scheduled Task %q: %w", expected.TaskName, err)
		}
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		return false, fmt.Errorf("create Scheduled Task %q: %w: %s", expected.TaskName, err, detail)
	}
	probe, err := source.Probe(ctx, expected.TaskName)
	if err != nil {
		return true, fmt.Errorf("verify created Scheduled Task %q: %w", expected.TaskName, err)
	}
	if !ClassifyStartupTask(probe, expected).Owned {
		return true, fmt.Errorf("created Scheduled Task %q does not match Loki's managed startup action", expected.TaskName)
	}
	return true, nil
}
