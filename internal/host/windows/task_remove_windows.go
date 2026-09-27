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

const removeOwnedStartupTaskScript = `$ErrorActionPreference='Stop';$t=Get-ScheduledTask -TaskName $env:LOKI_TASK_NAME -ErrorAction SilentlyContinue;if(-not $t){exit 0};$a=@($t.Actions);if($a.Count -ne 1 -or -not ([string]$a[0].Execute).Equals($env:LOKI_TASK_EXE,[StringComparison]::OrdinalIgnoreCase) -or -not ([string]$a[0].Arguments).Equals($env:LOKI_TASK_ARGS,[StringComparison]::Ordinal) -or -not ([string]$t.Description).Equals('Keep the Loki WSL2 appliance running.',[StringComparison]::Ordinal)){throw 'Scheduled Task no longer matches Loki ownership'};Stop-ScheduledTask -TaskName $env:LOKI_TASK_NAME -ErrorAction SilentlyContinue;Unregister-ScheduledTask -TaskName $env:LOKI_TASK_NAME -Confirm:$false -ErrorAction Stop`

func (source PowerShellStartupTaskSource) RemoveOwned(ctx context.Context, expected ExpectedInstallation) error {
	executable := strings.TrimSpace(source.Exe)
	if executable == "" {
		executable = "powershell.exe"
	}
	command := exec.CommandContext(ctx, executable, "-NoProfile", "-NonInteractive", "-Command", removeOwnedStartupTaskScript)
	command.Env = append(withoutEnvironment(os.Environ(),
		"LOKI_TASK_NAME", "LOKI_TASK_EXE", "LOKI_TASK_ARGS"),
		"LOKI_TASK_NAME="+expected.TaskName,
		"LOKI_TASK_EXE="+expected.TaskExecutable,
		"LOKI_TASK_ARGS="+expected.TaskArguments,
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
