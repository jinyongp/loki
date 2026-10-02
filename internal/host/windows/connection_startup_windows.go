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

const startVerifiedKeepaliveTaskScript = `$ErrorActionPreference='Stop';$t=Get-ScheduledTask -TaskName $env:LOKI_KEEPALIVE_TASK_NAME -ErrorAction Stop;$a=@($t.Actions);if($a.Count -ne 1 -or -not ([string]$a[0].Execute).Equals($env:LOKI_KEEPALIVE_TASK_EXE,[StringComparison]::OrdinalIgnoreCase) -or -not ([string]$a[0].Arguments).Equals($env:LOKI_KEEPALIVE_TASK_ARGS,[StringComparison]::Ordinal) -or -not ([string]$t.Description).Equals('Keep the Loki WSL2 appliance running.',[StringComparison]::Ordinal)){throw 'Scheduled Task no longer matches Loki WSL ownership'};Start-ScheduledTask -TaskName $env:LOKI_KEEPALIVE_TASK_NAME -ErrorAction Stop`

type WindowsConnectionStartupPlatform struct {
	LocalAppData string
	Collector    PreflightCollector
	WSL          WSLClient
	TaskExe      string
	SleepFunc    SleepFunc
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
	executable := strings.TrimSpace(platform.TaskExe)
	if executable == "" {
		executable = "powershell.exe"
	}
	command := exec.CommandContext(ctx, executable, "-NoProfile", "-NonInteractive", "-Command", startVerifiedKeepaliveTaskScript)
	command.Env = append(withoutEnvironment(os.Environ(),
		"LOKI_KEEPALIVE_TASK_NAME", "LOKI_KEEPALIVE_TASK_EXE", "LOKI_KEEPALIVE_TASK_ARGS"),
		"LOKI_KEEPALIVE_TASK_NAME="+expected.TaskName,
		"LOKI_KEEPALIVE_TASK_EXE="+expected.TaskExecutable,
		"LOKI_KEEPALIVE_TASK_ARGS="+expected.TaskArguments,
	)
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
	return ConnectionManager{
		Helpers:  NewWindowsHelperManagerWithProgress(binding, paths, reporter),
		Store:    store,
		Tasks:    NewWindowsConnectionTaskManager(localAppData),
		Adapters: append([]RemoteConnectionAdapter(nil), adapters...),
		Platform: FrontendArchitecture,
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
