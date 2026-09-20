package service

import (
	"context"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	"loki/internal/fault"
	"loki/internal/work/jobs"
)

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

func TestJobHandlersExposeTypedPublicLifecycle(t *testing.T) {
	controller := jobControllerFixture()
	handler := JobHandlers(controller)["job"]
	if handler == nil {
		t.Fatal("job handler missing")
	}

	requestID := controller.startResult.RequestID
	start, err := handler(t.Context(), map[string]any{
		"action": "start", "request_id": requestID,
		"cwd": ".", "argv": []any{"/bin/true"}, "timeout_seconds": float64(30),
		"network":   "dependency-install",
		"endpoints": []any{map[string]any{"name": "web", "port": float64(5173)}},
	})
	if err != nil {
		t.Fatal(err)
	}
	startValue := start.StructuredContent.(map[string]any)
	if startValue["action"] != "start" || startValue["request_id"] != requestID ||
		startValue["job_id"] != controller.startResult.JobID || startValue["detached"] != true ||
		startValue["network"] != jobs.NetworkDependencyInstall ||
		!reflect.DeepEqual(startValue["endpoint_requests"], []jobs.EndpointRequest{{Name: "web", Port: 5173}}) {
		t.Fatalf("start result = %#v", startValue)
	}
	controller.mu.Lock()
	requests := append([]jobs.StartRequest(nil), controller.startRequests...)
	controller.mu.Unlock()
	if len(requests) != 1 || requests[0].RequestID != requestID ||
		!reflect.DeepEqual(requests[0].Argv, []string{"/bin/true"}) || requests[0].TimeoutSeconds != 30 ||
		requests[0].Network != jobs.NetworkDependencyInstall ||
		!reflect.DeepEqual(requests[0].Endpoints, []jobs.EndpointRequest{{Name: "web", Port: 5173}}) {
		t.Fatalf("start requests = %#v", requests)
	}

	controller.startResult.Replayed = true
	replayed, err := handler(t.Context(), map[string]any{
		"action": "start", "request_id": requestID, "argv": []any{"/bin/true"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if replayed.StructuredContent.(map[string]any)["replayed"] != true {
		t.Fatalf("replay result = %#v", replayed.StructuredContent)
	}

	controller.startErr = jobs.ErrReplayConflict
	if _, err = handler(t.Context(), map[string]any{
		"action": "start", "request_id": requestID, "argv": []any{"/bin/false"},
	}); err == nil || fault.Describe(err).Code != fault.CodeConflict {
		t.Fatalf("replay conflict = %#v", fault.Describe(err))
	}
	controller.startErr = nil

	for _, tc := range []struct {
		action string
		check  func(map[string]any)
	}{
		{"inspect", func(value map[string]any) {
			if value["action"] != "inspect" || value["state"] != jobs.StateRunning ||
				value["network"] != jobs.NetworkDependencyInstall {
				t.Fatalf("inspect result = %#v", value)
			}
			endpoints, ok := value["endpoints"].([]jobs.EndpointLease)
			if !ok || len(endpoints) != 1 || endpoints[0].Name != "web" ||
				endpoints[0].HostPort != 43001 || endpoints[0].State != jobs.EndpointLeaseActive {
				t.Fatalf("inspect endpoints = %#v", value["endpoints"])
			}
		}},
		{"output", func(value map[string]any) {
			if value["action"] != "output" || value["output"] != "live" || value["complete"] != false {
				t.Fatalf("output result = %#v", value)
			}
		}},
		{"cancel", func(value map[string]any) {
			if value["action"] != "cancel" || value["canceled"] != true {
				t.Fatalf("cancel result = %#v", value)
			}
			status := value["status"].(map[string]any)
			if status["outcome"] != jobs.OutcomeCanceled || status["cleanup"] != jobs.CleanupComplete {
				t.Fatalf("cancel status = %#v", status)
			}
		}},
	} {
		result, err := handler(t.Context(), map[string]any{
			"action": tc.action, "job_id": controller.startResult.JobID,
		})
		if err != nil {
			t.Fatalf("%s: %v", tc.action, err)
		}
		tc.check(result.StructuredContent.(map[string]any))
	}
}

func TestJobHandlerFailsClosedWithoutControllerOrValidAction(t *testing.T) {
	handler := JobHandlers(nil)["job"]
	if _, err := handler(t.Context(), map[string]any{"action": "inspect", "job_id": strings.Repeat("a", 32)}); err == nil {
		t.Fatal("job handler accepted missing controller")
	}

	controller := jobControllerFixture()
	handler = JobHandlers(controller)["job"]
	if _, err := handler(t.Context(), map[string]any{"action": "unsupported"}); err == nil {
		t.Fatal("job handler accepted unsupported action")
	}
}

func TestJobStatusResultOmitsPrivilegedAuthority(t *testing.T) {
	value := jobStatusResult(jobControllerFixture().cancel.Status)
	for _, forbidden := range []string{
		"backend_ref", "instance_ref", "container_id", "policy_sha256", "request_sha256",
		"image", "mounts", "environment", "uid", "gid",
	} {
		if _, ok := value[forbidden]; ok {
			t.Fatalf("status exposes privileged field %q", forbidden)
		}
	}
}
