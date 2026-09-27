//go:build windows

package windows

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"os/user"
	"strings"
)

const connectionTaskProbeScript = `$ErrorActionPreference='Stop';$t=Get-ScheduledTask -TaskName $env:LOKI_CONNECTION_TASK_NAME -ErrorAction SilentlyContinue;if(-not $t){[ordered]@{present=$false}|ConvertTo-Json -Compress;exit 0};$a=@($t.Actions|ForEach-Object{[ordered]@{executable=[string]$_.Execute;arguments=[string]$_.Arguments}});$tr=@($t.Triggers);$logon=$false;if($tr.Count -eq 1){$logon=([string]$tr[0].CimClass.CimClassName).Equals('MSFT_TaskLogonTrigger',[StringComparison]::Ordinal)};$ticks=[System.Xml.XmlConvert]::ToTimeSpan([string]$t.Settings.ExecutionTimeLimit).Ticks;[ordered]@{present=$true;description=[string]$t.Description;actions=$a;run_level=[string]$t.Principal.RunLevel;user_id=[string]$t.Principal.UserId;trigger_count=$tr.Count;logon_trigger=$logon;execution_time_ticks=[Int64]$ticks}|ConvertTo-Json -Depth 4 -Compress`

const createConnectionTaskScript = `$ErrorActionPreference='Stop';if(Get-ScheduledTask -TaskName $env:LOKI_CONNECTION_TASK_NAME -ErrorAction SilentlyContinue){throw 'Scheduled Task already exists'};$action=New-ScheduledTaskAction -Execute $env:LOKI_CONNECTION_TASK_EXE -Argument $env:LOKI_CONNECTION_TASK_ARGS;$trigger=New-ScheduledTaskTrigger -AtLogOn -User $env:LOKI_CONNECTION_TASK_USER;$settings=New-ScheduledTaskSettingsSet -ExecutionTimeLimit (New-TimeSpan -Minutes 10) -StartWhenAvailable -AllowStartIfOnBatteries -DontStopIfGoingOnBatteries;$principal=New-ScheduledTaskPrincipal -UserId $env:LOKI_CONNECTION_TASK_USER -LogonType Interactive -RunLevel Limited;Register-ScheduledTask -TaskName $env:LOKI_CONNECTION_TASK_NAME -Action $action -Trigger $trigger -Settings $settings -Principal $principal -Description $env:LOKI_CONNECTION_TASK_DESCRIPTION -ErrorAction Stop|Out-Null`

const removeConnectionTaskScript = `$ErrorActionPreference='Stop';$t=Get-ScheduledTask -TaskName $env:LOKI_CONNECTION_TASK_NAME -ErrorAction SilentlyContinue;if(-not $t){exit 0};$a=@($t.Actions);$tr=@($t.Triggers);$ticks=[System.Xml.XmlConvert]::ToTimeSpan([string]$t.Settings.ExecutionTimeLimit).Ticks;if($a.Count -ne 1 -or -not ([string]$a[0].Execute).Equals($env:LOKI_CONNECTION_TASK_EXE,[StringComparison]::OrdinalIgnoreCase) -or -not ([string]$a[0].Arguments).Equals($env:LOKI_CONNECTION_TASK_ARGS,[StringComparison]::Ordinal) -or -not ([string]$t.Description).Equals($env:LOKI_CONNECTION_TASK_DESCRIPTION,[StringComparison]::Ordinal) -or -not ([string]$t.Principal.RunLevel).Equals('Limited',[StringComparison]::OrdinalIgnoreCase) -or $tr.Count -ne 1 -or -not ([string]$tr[0].CimClass.CimClassName).Equals('MSFT_TaskLogonTrigger',[StringComparison]::Ordinal) -or $ticks -ne 6000000000){throw 'Scheduled Task no longer matches Loki connection ownership'};Stop-ScheduledTask -TaskName $env:LOKI_CONNECTION_TASK_NAME -ErrorAction SilentlyContinue;Unregister-ScheduledTask -TaskName $env:LOKI_CONNECTION_TASK_NAME -Confirm:$false -ErrorAction Stop`

type PowerShellConnectionTaskPlatform struct {
	LocalAppData string
	Exe          string
}

func (platform PowerShellConnectionTaskPlatform) powershell() string {
	executable := strings.TrimSpace(platform.Exe)
	if executable == "" {
		return "powershell.exe"
	}
	return executable
}

func (platform PowerShellConnectionTaskPlatform) store() WindowsConnectionStateStore {
	return NewWindowsConnectionStateStore(platform.LocalAppData)
}

func (platform PowerShellConnectionTaskPlatform) Probe(ctx context.Context, taskName string) (ConnectionTaskProbe, error) {
	command := exec.CommandContext(ctx, platform.powershell(), "-NoProfile", "-NonInteractive", "-Command", connectionTaskProbeScript)
	command.Env = append(withoutEnvironment(os.Environ(), "LOKI_CONNECTION_TASK_NAME"),
		"LOKI_CONNECTION_TASK_NAME="+taskName)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		return ConnectionTaskProbe{}, fmt.Errorf("query connection Scheduled Task %q: %w: %s",
			taskName, err, strings.TrimSpace(stderr.String()))
	}
	var payload struct {
		Present     bool   `json:"present"`
		Description string `json:"description"`
		Actions     []struct {
			Executable string `json:"executable"`
			Arguments  string `json:"arguments"`
		} `json:"actions"`
		RunLevel           string `json:"run_level"`
		UserID             string `json:"user_id"`
		TriggerCount       int    `json:"trigger_count"`
		LogonTrigger       bool   `json:"logon_trigger"`
		ExecutionTimeTicks int64  `json:"execution_time_ticks"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &payload); err != nil {
		return ConnectionTaskProbe{}, fmt.Errorf("decode connection Scheduled Task probe: %w", err)
	}
	probe := ConnectionTaskProbe{
		Present: payload.Present, Description: payload.Description,
		RunLevel: payload.RunLevel, UserID: payload.UserID,
		TriggerCount: payload.TriggerCount, LogonTrigger: payload.LogonTrigger,
		ExecutionTimeTicks: payload.ExecutionTimeTicks,
	}
	for _, action := range payload.Actions {
		probe.Actions = append(probe.Actions, StartupTaskAction{
			Executable: action.Executable, Arguments: action.Arguments,
		})
	}
	return probe, nil
}

func (platform PowerShellConnectionTaskPlatform) Create(ctx context.Context, ownership ConnectionTaskOwnership) error {
	current, err := platform.CurrentUser()
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, platform.powershell(), "-NoProfile", "-NonInteractive", "-Command", createConnectionTaskScript)
	command.Env = append(withoutEnvironment(os.Environ(),
		"LOKI_CONNECTION_TASK_NAME", "LOKI_CONNECTION_TASK_EXE", "LOKI_CONNECTION_TASK_ARGS",
		"LOKI_CONNECTION_TASK_DESCRIPTION", "LOKI_CONNECTION_TASK_USER"),
		"LOKI_CONNECTION_TASK_NAME="+ownership.TaskName,
		"LOKI_CONNECTION_TASK_EXE="+ownership.Executable,
		"LOKI_CONNECTION_TASK_ARGS="+ownership.Arguments,
		"LOKI_CONNECTION_TASK_DESCRIPTION="+ownership.Description,
		"LOKI_CONNECTION_TASK_USER="+current,
	)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err = command.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		return fmt.Errorf("create connection Scheduled Task %q: %w: %s", ownership.TaskName, err, detail)
	}
	return nil
}

func (platform PowerShellConnectionTaskPlatform) Remove(ctx context.Context, ownership ConnectionTaskOwnership) error {
	command := exec.CommandContext(ctx, platform.powershell(), "-NoProfile", "-NonInteractive", "-Command", removeConnectionTaskScript)
	command.Env = append(withoutEnvironment(os.Environ(),
		"LOKI_CONNECTION_TASK_NAME", "LOKI_CONNECTION_TASK_EXE", "LOKI_CONNECTION_TASK_ARGS",
		"LOKI_CONNECTION_TASK_DESCRIPTION"),
		"LOKI_CONNECTION_TASK_NAME="+ownership.TaskName,
		"LOKI_CONNECTION_TASK_EXE="+ownership.Executable,
		"LOKI_CONNECTION_TASK_ARGS="+ownership.Arguments,
		"LOKI_CONNECTION_TASK_DESCRIPTION="+ownership.Description,
	)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		return fmt.Errorf("remove connection Scheduled Task %q: %w: %s", ownership.TaskName, err, detail)
	}
	return nil
}

func (platform PowerShellConnectionTaskPlatform) ownershipPath(distribution string) (string, error) {
	root, err := platform.store().distributionRoot(distribution)
	if err != nil {
		return "", err
	}
	return joinWindowsPath(root, connectionTaskOwnershipFileName), nil
}

func (platform PowerShellConnectionTaskPlatform) ReadOwnership(distribution string) (ConnectionTaskOwnership, bool, error) {
	store := platform.store()
	root, err := store.distributionRoot(distribution)
	if err != nil {
		return ConnectionTaskOwnership{}, false, err
	}
	rootInfo, err := OSStateFilesystem{}.Lstat(root)
	if err != nil {
		return ConnectionTaskOwnership{}, false, err
	}
	if !rootInfo.Exists {
		return ConnectionTaskOwnership{}, false, nil
	}
	if !rootInfo.Directory || rootInfo.Reparse {
		return ConnectionTaskOwnership{}, false, errors.New("connection startup-task ownership root is not a real directory")
	}
	if err = verifyPrivateACL(root, true); err != nil {
		return ConnectionTaskOwnership{}, false, fmt.Errorf("connection startup-task ownership root ACL drift: %w", err)
	}
	path := joinWindowsPath(root, connectionTaskOwnershipFileName)
	info, err := OSStateFilesystem{}.Lstat(path)
	if err != nil {
		return ConnectionTaskOwnership{}, false, err
	}
	if !info.Exists {
		return ConnectionTaskOwnership{}, false, nil
	}
	if !info.Regular || info.Reparse {
		return ConnectionTaskOwnership{}, false, errors.New("connection startup-task ownership is not a regular non-reparse file")
	}
	if err = verifyPrivateACL(path, false); err != nil {
		return ConnectionTaskOwnership{}, false, fmt.Errorf("connection startup-task ownership ACL drift: %w", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return ConnectionTaskOwnership{}, false, err
	}
	ownership, err := parseConnectionTaskOwnership(raw)
	if err != nil {
		return ConnectionTaskOwnership{}, false, err
	}
	if ownership.Distribution != distribution {
		return ConnectionTaskOwnership{}, false, errors.New("connection startup-task ownership distribution changed")
	}
	return ownership, true, nil
}

func (platform PowerShellConnectionTaskPlatform) WriteOwnership(ctx context.Context, ownership ConnectionTaskOwnership) error {
	store := platform.store()
	if err := store.EnsureDistributionRoot(ctx, ownership.Distribution); err != nil {
		return err
	}
	path, err := platform.ownershipPath(ownership.Distribution)
	if err != nil {
		return err
	}
	raw, err := encodeConnectionTaskOwnership(ownership)
	if err != nil {
		return err
	}
	frontend := store.Platform
	if frontend.Runner == nil {
		frontend = NewWindowsFrontendPlatform()
	}
	if err = frontend.WriteProtectedAtomic(ctx, path, raw); err != nil {
		return err
	}
	return verifyPrivateACL(path, false)
}

func (platform PowerShellConnectionTaskPlatform) DeleteOwnership(ctx context.Context, distribution string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	path, err := platform.ownershipPath(distribution)
	if err != nil {
		return err
	}
	ownership, present, err := platform.ReadOwnership(distribution)
	if err != nil {
		return err
	}
	if !present {
		return nil
	}
	if ownership.Distribution != distribution {
		return errors.New("refusing to delete mismatched connection startup-task ownership")
	}
	if err = os.Remove(path); err != nil {
		return err
	}
	return platform.store().removeEmptyDistributionRoot(distribution)
}

func (platform PowerShellConnectionTaskPlatform) VerifyCanonicalFrontend(ctx context.Context) (FrontendPaths, error) {
	if err := ctx.Err(); err != nil {
		return FrontendPaths{}, err
	}
	paths, err := platform.store().frontendPaths()
	if err != nil {
		return FrontendPaths{}, err
	}
	filesystem := OSStateFilesystem{}
	for _, candidate := range []struct {
		path      string
		directory bool
	}{
		{paths.Root, true}, {paths.BinDir, true}, {paths.Binary, false}, {paths.Ownership, false},
	} {
		info, statErr := filesystem.Lstat(candidate.path)
		if statErr != nil {
			return FrontendPaths{}, statErr
		}
		if !info.Exists || info.Reparse || candidate.directory && !info.Directory || !candidate.directory && !info.Regular {
			return FrontendPaths{}, errors.New("canonical Windows frontend ownership path is missing or unsafe")
		}
		if aclErr := verifyPrivateACL(candidate.path, candidate.directory); aclErr != nil {
			return FrontendPaths{}, fmt.Errorf("canonical Windows frontend ACL drift: %w", aclErr)
		}
	}
	raw, err := os.ReadFile(paths.Ownership)
	if err != nil {
		return FrontendPaths{}, err
	}
	ownership, err := ParseFrontendOwnership(raw, paths)
	if err != nil {
		return FrontendPaths{}, err
	}
	digest, length, err := (WindowsFrontendPlatform{}).FileDigest(paths.Binary)
	if err != nil {
		return FrontendPaths{}, err
	}
	if digest != ownership.SHA256 || length != ownership.Length {
		return FrontendPaths{}, errors.New("canonical Windows frontend bytes no longer match verified ownership")
	}
	return paths, nil
}

func (platform PowerShellConnectionTaskPlatform) CurrentUser() (string, error) {
	current, err := user.Current()
	if err != nil {
		return "", fmt.Errorf("resolve current Windows user: %w", err)
	}
	if strings.TrimSpace(current.Username) == "" {
		return "", errors.New("current Windows user name is empty")
	}
	return current.Username, nil
}

func NewWindowsConnectionTaskManager(localAppData string) ConnectionTaskManager {
	return ConnectionTaskManager{Platform: PowerShellConnectionTaskPlatform{LocalAppData: localAppData}}
}
