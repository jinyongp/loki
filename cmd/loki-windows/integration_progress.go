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
	heartbeat := progress.HeartbeatOptions{
		Operation: "integration", Phase: "execute", Every: 45 * time.Second,
		Message: fmt.Sprintf("Still waiting for %s integration %s", request.Integration, request.Action),
	}
	if request.Integration == "signing" && (request.Action == "setup" || request.Action == "rotate") {
		progress.Emit(reporter, progress.Event{
			Operation: "integration", Phase: "execute", State: progress.StateStarted, Level: progress.LevelSummary,
			Message: "Setting up Git commit signing, including a recovery backup and service restart...",
		})
		heartbeat.Every = 30 * time.Second
		heartbeat.Message = "Still setting up Git commit signing"
	}
	progress.Emit(reporter, progress.Event{
		Operation: "integration", Phase: "inspect", State: progress.StateStarted,
		Level:   integrationProgressLevel(request.Action, request.Integration),
		Message: fmt.Sprintf("Running %s integration %s; checking the WSL appliance...", request.Integration, request.Action),
	})
	stopHeartbeat := progress.StartHeartbeat(ctx, reporter, heartbeat)
	defer stopHeartbeat()
	if input != nil {
		return client.ExecuteInputStreaming(ctx, distribution, request, input, reporter)
	}
	return client.ExecuteStreaming(ctx, distribution, request, reporter)
}

func integrationProgressLevel(action, name string) progress.Level {
	if action == "doctor" && name == "github" {
		return progress.LevelSummary
	}
	return progress.LevelDetail
}
