//go:build windows

package windows

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

const startupTaskProbeScript = `$ErrorActionPreference='Stop';$t=Get-ScheduledTask -TaskName $env:LOKI_TASK_NAME -ErrorAction SilentlyContinue;if(-not $t){[ordered]@{present=$false}|ConvertTo-Json -Compress;exit 0};$a=@($t.Actions|ForEach-Object{[ordered]@{executable=[string]$_.Execute;arguments=[string]$_.Arguments}});[ordered]@{present=$true;description=[string]$t.Description;actions=$a}|ConvertTo-Json -Depth 4 -Compress`

type PowerShellStartupTaskSource struct {
	Exe string
}

func (source PowerShellStartupTaskSource) Probe(ctx context.Context, taskName string) (StartupTaskProbe, error) {
	executable := strings.TrimSpace(source.Exe)
	if executable == "" {
		executable = "powershell.exe"
	}
	command := exec.CommandContext(ctx, executable, "-NoProfile", "-NonInteractive", "-Command", startupTaskProbeScript)
	command.Env = append(withoutEnvironment(os.Environ(), "LOKI_TASK_NAME"), "LOKI_TASK_NAME="+taskName)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return StartupTaskProbe{}, fmt.Errorf("query Scheduled Task %q: %w: %s", taskName, err, strings.TrimSpace(stderr.String()))
	}
	var payload struct {
		Present     bool   `json:"present"`
		Description string `json:"description"`
		Actions     []struct {
			Executable string `json:"executable"`
			Arguments  string `json:"arguments"`
		} `json:"actions"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		return StartupTaskProbe{}, fmt.Errorf("decode Scheduled Task probe: %w", err)
	}
	probe := StartupTaskProbe{Present: payload.Present, Description: payload.Description}
	for _, action := range payload.Actions {
		probe.Actions = append(probe.Actions, StartupTaskAction{Executable: action.Executable, Arguments: action.Arguments})
	}
	return probe, nil
}

func withoutEnvironment(environment []string, name string) []string {
	prefix := strings.ToUpper(name) + "="
	filtered := make([]string, 0, len(environment))
	for _, entry := range environment {
		if strings.HasPrefix(strings.ToUpper(entry), prefix) {
			continue
		}
		filtered = append(filtered, entry)
	}
	return filtered
}
