package main

import (
	"context"
	"fmt"
	"io"

	"loki/internal/progress"
)

func startHostIntegrationProgress(ctx context.Context, stderr io.Writer, action, name string) (progress.Reporter, func()) {
	reporter := progress.NewLineReporter(stderr)
	progress.Emit(reporter, progress.Event{
		Operation: "integration", Phase: "inspect", State: progress.StateStarted,
		Message: fmt.Sprintf("Running %s integration %s; inspecting the current configuration...", name, action),
	})
	stopHeartbeat := progress.StartHeartbeat(ctx, reporter, progress.HeartbeatOptions{
		Operation: "integration", Phase: "execute",
		Message: fmt.Sprintf("Still working on %s integration %s; waiting for the current step", name, action),
	})
	return reporter, stopHeartbeat
}
