//go:build windows

package windows

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
	"time"

	"loki/internal/progress"
)

const startVerifiedKeepaliveTaskScript = `$ErrorActionPreference='Stop'
$t=Get-ScheduledTask -TaskName $env:LOKI_KEEPALIVE_TASK_NAME -ErrorAction Stop
$a=@($t.Actions)
$current=$a.Count -eq 1 -and ([string]$a[0].Execute).Equals($env:LOKI_KEEPALIVE_TASK_EXE,[StringComparison]::OrdinalIgnoreCase) -and ([string]$a[0].Arguments).Equals($env:LOKI_KEEPALIVE_TASK_ARGS,[StringComparison]::Ordinal)
$legacy=$a.Count -eq 1 -and $env:LOKI_KEEPALIVE_LEGACY_EXE -and ([string]$a[0].Execute).Equals($env:LOKI_KEEPALIVE_LEGACY_EXE,[StringComparison]::OrdinalIgnoreCase) -and ([string]$a[0].Arguments).Equals($env:LOKI_KEEPALIVE_LEGACY_ARGS,[StringComparison]::Ordinal)
$hidden=$a.Count -eq 1 -and $env:LOKI_KEEPALIVE_HIDDEN_EXE -and ([string]$a[0].Execute).Equals($env:LOKI_KEEPALIVE_HIDDEN_EXE,[StringComparison]::OrdinalIgnoreCase) -and ([string]$a[0].Arguments).Equals($env:LOKI_KEEPALIVE_HIDDEN_ARGS,[StringComparison]::Ordinal)
if((-not $current -and -not $legacy -and -not $hidden) -or -not ([string]$t.Description).Equals('Keep the Loki WSL2 appliance running.',[StringComparison]::Ordinal)){throw 'Scheduled Task no longer matches Loki WSL ownership'}
if($env:LOKI_KEEPALIVE_PREPARE_ONLY -eq '1'){
  if([string]$t.State -eq 'Running'){
    Stop-ScheduledTask -TaskName $env:LOKI_KEEPALIVE_TASK_NAME -ErrorAction Stop
    for($attempt=0;$attempt -lt 10;$attempt++){
      $t=Get-ScheduledTask -TaskName $env:LOKI_KEEPALIVE_TASK_NAME -ErrorAction Stop
      if([string]$t.State -ne 'Running'){exit 0}
      Start-Sleep -Milliseconds 500
    }
    throw 'WSL keepalive task could not be stopped before companion update'
  }
  exit 0
}
if(-not $current){
  $wasRunning=[string]$t.State -eq 'Running'
  $action=New-ScheduledTaskAction -Execute $env:LOKI_KEEPALIVE_TASK_EXE -Argument $env:LOKI_KEEPALIVE_TASK_ARGS
  Set-ScheduledTask -TaskName $env:LOKI_KEEPALIVE_TASK_NAME -Action $action -ErrorAction Stop|Out-Null
  $t=Get-ScheduledTask -TaskName $env:LOKI_KEEPALIVE_TASK_NAME -ErrorAction Stop
  $a=@($t.Actions)
  if($a.Count -ne 1 -or -not ([string]$a[0].Execute).Equals($env:LOKI_KEEPALIVE_TASK_EXE,[StringComparison]::OrdinalIgnoreCase) -or -not ([string]$a[0].Arguments).Equals($env:LOKI_KEEPALIVE_TASK_ARGS,[StringComparison]::Ordinal)){throw 'WSL keepalive background action could not be restored'}
  if($wasRunning){
    Stop-ScheduledTask -TaskName $env:LOKI_KEEPALIVE_TASK_NAME -ErrorAction Stop
    for($attempt=0;$attempt -lt 10;$attempt++){
      $t=Get-ScheduledTask -TaskName $env:LOKI_KEEPALIVE_TASK_NAME -ErrorAction Stop
      if([string]$t.State -ne 'Running'){break}
      Start-Sleep -Milliseconds 500
    }
    if([string]$t.State -eq 'Running'){throw 'WSL keepalive foreground task could not be stopped'}
  }
}
if([int]$t.Settings.RestartCount -eq 0 -and -not [string]$t.Settings.RestartInterval){
  $settings=$t.Settings
  $settings.RestartCount=3
  $settings.RestartInterval='PT1M'
  Set-ScheduledTask -TaskName $env:LOKI_KEEPALIVE_TASK_NAME -Settings $settings -ErrorAction Stop|Out-Null
  $t=Get-ScheduledTask -TaskName $env:LOKI_KEEPALIVE_TASK_NAME -ErrorAction Stop
  if([int]$t.Settings.RestartCount -ne 3 -or [System.Xml.XmlConvert]::ToTimeSpan([string]$t.Settings.RestartInterval) -ne [TimeSpan]::FromMinutes(1)){throw 'WSL keepalive retry settings could not be restored'}
}
if([string]$t.State -eq 'Running'){exit 0}
Start-ScheduledTask -TaskName $env:LOKI_KEEPALIVE_TASK_NAME -ErrorAction Stop
for($attempt=0;$attempt -lt 10;$attempt++){
  Start-Sleep -Milliseconds 500
  $t=Get-ScheduledTask -TaskName $env:LOKI_KEEPALIVE_TASK_NAME -ErrorAction Stop
  if([string]$t.State -eq 'Running'){exit 0}
}
$info=Get-ScheduledTaskInfo -TaskName $env:LOKI_KEEPALIVE_TASK_NAME -ErrorAction Stop
throw ('WSL keepalive task did not remain running (last exit code: '+$info.LastTaskResult+'). Check the Loki WSL task in Task Scheduler.')`

type WindowsConnectionStartupPlatform struct {
	LocalAppData string
	Collector    PreflightCollector
	WSL          WSLClient
	TaskExe      string
	SleepFunc    SleepFunc
	Progress     progress.Reporter
}

func NewWindowsConnectionStartupPlatform(localAppData string) WindowsConnectionStartupPlatform {
	runner := ExecNativeRunner{}
	wsl := WSLClient{Runner: runner}
	return WindowsConnectionStartupPlatform{
		LocalAppData: localAppData,
		Collector: PreflightCollector{
			Filesystem: OSStateFilesystem{},
			Tasks:      PowerShellStartupTaskSource{},
			WSL:        wsl,
		},
		WSL: wsl,
	}
}

func (platform WindowsConnectionStartupPlatform) VerifyCanonicalFrontend(ctx context.Context) (FrontendPaths, error) {
	return (PowerShellConnectionTaskPlatform{LocalAppData: platform.LocalAppData}).VerifyCanonicalFrontend(ctx)
}

func (platform WindowsConnectionStartupPlatform) Collect(
	ctx context.Context,
	expected ExpectedInstallation,
) (ExistingSnapshot, error) {
	collector := platform.Collector
	if collector.Filesystem == nil || collector.Tasks == nil || collector.WSL.Runner == nil {
		collector = NewWindowsPreflightCollector()
	}
	return collector.Collect(ctx, expected)
}

func (platform WindowsConnectionStartupPlatform) StartKeepalive(
	ctx context.Context,
	expected ExpectedInstallation,
) error {
	if err := ensureKeepaliveExecutable(ctx, expected, func() error {
		return platform.runKeepaliveTask(ctx, expected, true)
	}); err != nil {
		return err
	}
	return platform.runKeepaliveTask(ctx, expected, false)
}

func (platform WindowsConnectionStartupPlatform) runKeepaliveTask(ctx context.Context, expected ExpectedInstallation, prepareOnly bool) error {
	executable := strings.TrimSpace(platform.TaskExe)
	if executable == "" {
		executable = "powershell.exe"
	}
	command := exec.CommandContext(ctx, executable, "-NoProfile", "-NonInteractive", "-Command", startVerifiedKeepaliveTaskScript)
	configureNativeProcess(command)
	command.Env = append(withoutEnvironment(os.Environ(),
		"LOKI_KEEPALIVE_TASK_NAME", "LOKI_KEEPALIVE_TASK_EXE", "LOKI_KEEPALIVE_TASK_ARGS", "LOKI_KEEPALIVE_LEGACY_EXE", "LOKI_KEEPALIVE_LEGACY_ARGS", "LOKI_KEEPALIVE_HIDDEN_EXE", "LOKI_KEEPALIVE_HIDDEN_ARGS", "LOKI_KEEPALIVE_PREPARE_ONLY"),
		"LOKI_KEEPALIVE_TASK_NAME="+expected.TaskName,
		"LOKI_KEEPALIVE_TASK_EXE="+expected.TaskExecutable,
		"LOKI_KEEPALIVE_TASK_ARGS="+expected.TaskArguments,
		"LOKI_KEEPALIVE_LEGACY_EXE="+expected.LegacyTaskExecutable,
		"LOKI_KEEPALIVE_LEGACY_ARGS="+expected.LegacyTaskArguments,
		"LOKI_KEEPALIVE_HIDDEN_EXE="+expected.LegacyHiddenTaskExecutable,
		"LOKI_KEEPALIVE_HIDDEN_ARGS="+expected.LegacyHiddenTaskArguments,
	)
	if prepareOnly {
		command.Env = append(command.Env, "LOKI_KEEPALIVE_PREPARE_ONLY=1")
	}
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	if err := command.Run(); err != nil {
		detail := strings.TrimSpace(stderr.String())
		if detail == "" {
			detail = strings.TrimSpace(stdout.String())
		}
		return fmt.Errorf("start verified WSL keepalive Scheduled Task %q: %w: %s", expected.TaskName, err, detail)
	}
	return nil
}

func (platform WindowsConnectionStartupPlatform) Prepare(ctx context.Context, distribution string) error {
	expected, err := ExpectedFromOptions(InstallOptions{Distribution: distribution}, platform.LocalAppData, os.Getenv("SystemRoot"))
	if err != nil {
		return err
	}
	_, err = (ConnectionStartupController{Platform: platform, Progress: platform.Progress}).EnsureAppliance(ctx, expected)
	return err
}

func (platform WindowsConnectionStartupPlatform) Doctor(
	ctx context.Context,
	distribution string,
) (NativeProbe, error) {
	if err := ValidateDistributionName(distribution); err != nil {
		return NativeProbe{}, err
	}
	client := platform.WSL
	if client.Runner == nil {
		client = WSLClient{Runner: ExecNativeRunner{}}
	}
	result, err := client.run(ctx,
		"-d", distribution, "--user", "root", "--exec",
		"/usr/local/bin/loki", "host", "doctor", "--system",
	)
	if err != nil {
		return NativeProbe{}, err
	}
	return result, nil
}

func (platform WindowsConnectionStartupPlatform) Sleep(ctx context.Context, delay time.Duration) error {
	if platform.SleepFunc != nil {
		return platform.SleepFunc(ctx, delay)
	}
	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func NewWindowsConnectionManager(
	binding ReleaseBinding,
	localAppData string,
	adapters []RemoteConnectionAdapter,
) (ConnectionManager, error) {
	return NewWindowsConnectionManagerWithProgress(binding, localAppData, adapters, nil)
}

func NewWindowsConnectionManagerWithProgress(
	binding ReleaseBinding,
	localAppData string,
	adapters []RemoteConnectionAdapter,
	reporter progress.Reporter,
) (ConnectionManager, error) {
	paths, err := ResolveFrontendPaths(localAppData)
	if err != nil {
		return ConnectionManager{}, err
	}
	if !validFrontendReleaseTag(binding.ReleaseTag) {
		return ConnectionManager{}, errors.New("managed connection release binding is invalid")
	}
	store := NewWindowsConnectionStateStore(localAppData)
	for _, adapter := range adapters {
		if openAI, ok := adapter.(*OpenAIAdapter); ok {
			openAI.Progress = reporter
		}
	}
	appliance := NewWindowsConnectionStartupPlatform(localAppData)
	appliance.Progress = reporter
	return ConnectionManager{
		Appliance: appliance,
		Helpers:   NewWindowsHelperManagerWithProgress(binding, paths, reporter),
		Store:     store,
		Tasks:     NewWindowsConnectionTaskManager(localAppData),
		Adapters:  append([]RemoteConnectionAdapter(nil), adapters...),
		Platform:  FrontendArchitecture,
	}, nil
}

func NewWindowsConnectionStartupController(
	localAppData string,
	connections ConnectionStartupConnections,
) ConnectionStartupController {
	return ConnectionStartupController{
		Platform:    NewWindowsConnectionStartupPlatform(localAppData),
		Connections: connections,
	}
}
