package remote

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loki/internal/work/jobs"
)

func validExecutorOptions(t *testing.T, socket string) ExecutorOptions {
	t.Helper()
	uid := uint32(os.Getuid())
	return ExecutorOptions{Socket: socket, ExpectedUID: &uid, Timeout: time.Second}
}

func TestExecutorAsyncLifecycleUsesOnlyPublicNeutralFields(t *testing.T) {
	requestID := "123e4567-e89b-12d3-a456-426614174200"
	jobID, err := jobs.JobIDForRequestID(requestID)
	if err != nil {
		t.Fatal(err)
	}
	log := &requestLog{}
	socket := fakeLauncherSocket(t, func(request map[string]json.RawMessage) []byte {
		log.add(request)
		var operation string
		_ = json.Unmarshal(request["operation"], &operation)
		switch operation {
		case "start":
			if len(request) != 7 {
				t.Fatalf("executor start keys = %#v", request)
			}
			for _, forbidden := range []string{
				"id", "job_id", "policy_sha256", "request_sha256", "host_port",
				"proxy_token", "proxy_url", "network_id", "network_name",
				"gateway_image", "backend_ref", "sandbox_sha256",
			} {
				if _, ok := request[forbidden]; ok {
					t.Fatalf("executor start leaked %s: %#v", forbidden, request)
				}
			}
			var network jobs.NetworkProfile
			var endpoints []jobs.EndpointRequest
			_ = json.Unmarshal(request["network"], &network)
			_ = json.Unmarshal(request["endpoints"], &endpoints)
			if network != jobs.NetworkDependencyInstall || len(endpoints) != 1 ||
				endpoints[0] != (jobs.EndpointRequest{Name: "web", Port: 5173}) {
				t.Fatalf("executor logical intent = network %q endpoints %#v", network, endpoints)
			}
			return response(t, jobs.StartResult{
				RequestID: requestID, JobID: jobID, State: jobs.StateAdmitted,
				Detached: true, DeadlineAt: time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano),
			})
		case "inspect":
			return response(t, jobs.Status{JobID: jobID, State: jobs.StateRunning})
		case "output":
			return response(t, jobs.OutputSnapshot{JobID: jobID, State: jobs.StateRunning, Output: "live"})
		case "cancel":
			return response(t, jobs.CancelResult{
				JobID: jobID, Canceled: true,
				Status: jobs.Status{JobID: jobID, State: jobs.StateTerminal, Outcome: jobs.OutcomeCanceled, Cleanup: jobs.CleanupComplete},
			})
		default:
			return errorResponse(t, "unexpected")
		}
	})
	executor, err := NewExecutor(validExecutorOptions(t, socket))
	if err != nil {
		t.Fatal(err)
	}
	started, err := executor.Start(t.Context(), jobs.StartRequest{
		RequestID: strings.ToUpper(requestID),
		CWD:       ".", Argv: []string{"/bin/sleep", "10"}, TimeoutSeconds: 30,
		Network:   jobs.NetworkDependencyInstall,
		Endpoints: []jobs.EndpointRequest{{Name: "web", Port: 5173}},
	})
	if err != nil || started.RequestID != requestID || started.JobID != jobID {
		t.Fatalf("start = %#v, %v", started, err)
	}
	status, err := executor.Inspect(t.Context(), jobID)
	if err != nil || status.State != jobs.StateRunning {
		t.Fatalf("inspect = %#v, %v", status, err)
	}
	output, err := executor.Output(t.Context(), jobID)
	if err != nil || output.Output != "live" || output.Complete {
		t.Fatalf("output = %#v, %v", output, err)
	}
	canceled, err := executor.Cancel(t.Context(), jobID)
	if err != nil || !canceled.Canceled || canceled.Status.Cleanup != jobs.CleanupComplete {
		t.Fatalf("cancel = %#v, %v", canceled, err)
	}
	if got := strings.Join(log.snapshot(), ","); got != "start,inspect,output,cancel" {
		t.Fatalf("operations = %q", got)
	}
}

func TestExecutorPreservesReplayConflictAcrossRPC(t *testing.T) {
	requestID := "123e4567-e89b-12d3-a456-426614174202"
	socket := fakeLauncherSocket(t, func(request map[string]json.RawMessage) []byte {
		var operation string
		_ = json.Unmarshal(request["operation"], &operation)
		if operation == "start" {
			return errorResponse(t, jobs.ErrReplayConflict.Error())
		}
		return errorResponse(t, "unexpected")
	})
	executor, err := NewExecutor(validExecutorOptions(t, socket))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = executor.Start(t.Context(), jobs.StartRequest{
		RequestID: requestID, CWD: ".", Argv: []string{"/bin/true"},
	}); err == nil || err.Error() != jobs.ErrReplayConflict.Error() {
		t.Fatalf("replay conflict = %v", err)
	}
}

func TestExecutorRejectsInvalidResultsAndConfiguration(t *testing.T) {
	socket := fakeLauncherSocket(t, func(request map[string]json.RawMessage) []byte {
		var operation string
		_ = json.Unmarshal(request["operation"], &operation)
		switch operation {
		case "start":
			return response(t, jobs.StartResult{
				RequestID: "123e4567-e89b-12d3-a456-426614174201",
				JobID:     strings.Repeat("f", 32), State: jobs.StateAdmitted, Detached: true,
				DeadlineAt: "bad",
			})
		default:
			return errorResponse(t, "unexpected")
		}
	})
	executor, err := NewExecutor(validExecutorOptions(t, socket))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = executor.Start(t.Context(), jobs.StartRequest{
		RequestID: "123e4567-e89b-12d3-a456-426614174201",
		CWD:       ".", Argv: []string{"/bin/true"},
	}); err == nil {
		t.Fatal("invalid executor result was accepted")
	}

	uid := uint32(os.Getuid())
	for _, options := range []ExecutorOptions{
		{Socket: "relative.sock", ExpectedUID: &uid, Timeout: time.Second},
		{Socket: "/", ExpectedUID: &uid, Timeout: time.Second},
		{Socket: filepath.Join(t.TempDir(), "executor.sock"), Timeout: time.Second},
		{Socket: filepath.Join(t.TempDir(), "executor.sock"), ExpectedUID: &uid},
		{Socket: filepath.Join(t.TempDir(), "executor.sock"), ExpectedUID: &uid, Timeout: maxRunTimeout + time.Second},
	} {
		if _, err := NewExecutor(options); err == nil {
			t.Fatalf("invalid executor options accepted: %#v", options)
		}
	}
}

func TestNilExecutorFailsClosed(t *testing.T) {
	var executor *Executor
	if _, err := executor.Start(context.Background(), jobs.StartRequest{}); err == nil {
		t.Fatal("nil executor start was accepted")
	}
	if _, err := executor.Inspect(context.Background(), strings.Repeat("a", 32)); err == nil {
		t.Fatal("nil executor inspect was accepted")
	}
	if _, err := executor.Output(context.Background(), strings.Repeat("a", 32)); err == nil {
		t.Fatal("nil executor output was accepted")
	}
	if _, err := executor.Cancel(context.Background(), strings.Repeat("a", 32)); err == nil {
		t.Fatal("nil executor cancel was accepted")
	}
}
