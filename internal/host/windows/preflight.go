package windows

import (
	"context"
	"fmt"
)

type StartupTaskSource interface {
	Probe(context.Context, string) (StartupTaskProbe, error)
}

type PreflightCollector struct {
	Filesystem StateFilesystem
	Tasks      StartupTaskSource
	WSL        WSLClient
}

func (collector PreflightCollector) Collect(ctx context.Context, expected ExpectedInstallation) (ExistingSnapshot, error) {
	if collector.Filesystem == nil || collector.Tasks == nil || collector.WSL.Runner == nil {
		return ExistingSnapshot{}, fmt.Errorf("Windows preflight adapters are incomplete")
	}
	present, err := collector.WSL.DistributionPresent(ctx, expected.Distribution)
	if err != nil {
		return ExistingSnapshot{}, err
	}
	windowsState, err := InspectWindowsState(collector.Filesystem, expected)
	if err != nil {
		return ExistingSnapshot{}, err
	}
	taskProbe, err := collector.Tasks.Probe(ctx, expected.TaskName)
	if err != nil {
		return ExistingSnapshot{}, err
	}
	distributionProbe, err := collector.WSL.ProbeDistribution(ctx, expected.Distribution, present)
	if err != nil {
		return ExistingSnapshot{}, err
	}
	return ExistingSnapshot{
		Distribution: ClassifyDistribution(distributionProbe),
		Windows:      windowsState,
		StartupTask:  ClassifyStartupTask(taskProbe, expected),
	}, nil
}
