package mcpapp

import (
	"context"
	"encoding/json"
	"strings"
	"sync"
	"time"

	"loki/internal/work/jobs"
)

type runtimeFixture func(context.Context, any) (json.RawMessage, error)

func (f runtimeFixture) Call(ctx context.Context, request any) (json.RawMessage, error) {
	return f(ctx, request)
}

type browserFixture func(context.Context, string, map[string]any) (map[string]any, error)

func (f browserFixture) Call(ctx context.Context, operation string, args map[string]any) (map[string]any, error) {
	return f(ctx, operation, args)
}

type fakeJobsController struct {
	mu sync.Mutex

	startRequests []jobs.StartRequest
	startResult   jobs.StartResult
	startErr      error
	status        jobs.Status
	inspectErr    error
	output        jobs.OutputSnapshot
	outputErr     error
	cancel        jobs.CancelResult
	cancelErr     error
}

func (f *fakeJobsController) Start(_ context.Context, request jobs.StartRequest) (jobs.StartResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	request.Argv = append([]string(nil), request.Argv...)
	request.Endpoints = append([]jobs.EndpointRequest(nil), request.Endpoints...)
	request.Toolchains = append([]jobs.ToolchainRef(nil), request.Toolchains...)
	f.startRequests = append(f.startRequests, request)
	return f.startResult, f.startErr
}

func (f *fakeJobsController) Inspect(_ context.Context, _ string) (jobs.Status, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.status, f.inspectErr
}

func (f *fakeJobsController) Output(_ context.Context, _ string) (jobs.OutputSnapshot, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.output, f.outputErr
}

func (f *fakeJobsController) Cancel(_ context.Context, _ string) (jobs.CancelResult, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.cancel, f.cancelErr
}

func jobControllerFixture() *fakeJobsController {
	requestID := "123e4567-e89b-12d3-a456-426614174300"
	jobID, _ := jobs.JobIDForRequestID(requestID)
	now := time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC)
	return &fakeJobsController{
		startResult: jobs.StartResult{
			RequestID: requestID,
			JobID:     jobID, State: jobs.StateAdmitted, Detached: true,
			DeadlineAt: now.Add(time.Minute).Format(time.RFC3339Nano),
		},
		status: jobs.Status{
			JobID: jobID, State: jobs.StateRunning, Network: jobs.NetworkDependencyInstall,
			CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano),
			DeadlineAt: now.Add(time.Minute).Format(time.RFC3339Nano),
			Endpoints: []jobs.EndpointLease{{
				ID: strings.Repeat("a", 32), JobID: jobID, Name: "web", Port: 5173, HostPort: 43001,
				State: jobs.EndpointLeaseActive, CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Format(time.RFC3339Nano),
			}},
		},
		output: jobs.OutputSnapshot{JobID: jobID, State: jobs.StateRunning, Output: "live"},
		cancel: jobs.CancelResult{
			JobID: jobID, Canceled: true,
			Status: jobs.Status{
				JobID: jobID, State: jobs.StateTerminal, Network: jobs.NetworkNone,
				CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Add(time.Second).Format(time.RFC3339Nano),
				DeadlineAt: now.Add(time.Minute).Format(time.RFC3339Nano),
				Outcome:    jobs.OutcomeCanceled, Cleanup: jobs.CleanupComplete,
			},
		},
	}
}
