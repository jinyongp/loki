package main

import (
	"context"
	"fmt"
	"io"
	"time"

	windowshost "loki/internal/host/windows"
	"loki/internal/progress"
)

func executeWindowsIntegrationWithProgress(ctx context.Context, client windowshost.OperatorClient, distribution string, request windowshost.OperatorRequest, input []byte, stderr io.Writer) (windowshost.OperatorResult, error) {
	reporter := progress.NewLineReporter(stderr)
	progress.Emit(reporter, progress.Event{
		Operation: "integration", Phase: "inspect", State: progress.StateStarted,
		Message: fmt.Sprintf("Running %s integration %s; checking the WSL appliance...", request.Integration, request.Action),
	})
	stopHeartbeat := progress.StartHeartbeat(ctx, reporter, progress.HeartbeatOptions{
		Operation: "integration", Phase: "execute", Every: 45 * time.Second,
		Message: fmt.Sprintf("Still waiting for %s integration %s", request.Integration, request.Action),
	})
	defer stopHeartbeat()
	if input != nil {
		return client.ExecuteInputStreaming(ctx, distribution, request, input, reporter)
	}
	return client.ExecuteStreaming(ctx, distribution, request, reporter)
}
