package executor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"loki/internal/control/identity"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/rpc"
	"loki/internal/work/jobs"
)

type fakeJobRunner struct {
	calls       atomic.Int32
	result      jobs.RunResult
	startResult  jobs.StartResult
	startRequest jobs.StartRequest
	status       jobs.Status
	output      jobs.OutputSnapshot
	cancel      jobs.CancelResult
	err         error
}

func (r *fakeJobRunner) Run(_ context.Context, _ jobs.RunRequest) (jobs.RunResult, error) {
	r.calls.Add(1)
	return r.result, r.err
}

func (r *fakeJobRunner) Start(_ context.Context, request jobs.StartRequest) (jobs.StartResult, error) {
	r.calls.Add(1)
	request.Argv = append([]string(nil), request.Argv...)
	request.Endpoints = append([]jobs.EndpointRequest(nil), request.Endpoints...)
	r.startRequest = request
	return r.startResult, r.err
}

func (r *fakeJobRunner) Inspect(_ context.Context, _ string) (jobs.Status, error) {
	r.calls.Add(1)
	return r.status, r.err
}

func (r *fakeJobRunner) Output(_ context.Context, _ string) (jobs.OutputSnapshot, error) {
	r.calls.Add(1)
	return r.output, r.err
}

func (r *fakeJobRunner) Cancel(_ context.Context, _ string) (jobs.CancelResult, error) {
	r.calls.Add(1)
	return r.cancel, r.err
}

func TestOperationsExposeAgentJobLifecycle(t *testing.T) {
	runner := &fakeJobRunner{}
	operations, err := Operations(runner, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 5 {
		t.Fatalf("operations = %#v", operations)
	}
	for _, name := range []string{"run", "start", "inspect", "output", "cancel"} {
		operation, ok := operations[name]
		if !ok || operation.Grant != controlpolicy.Agent || operation.Timeout != time.Second {
			t.Fatalf("%s operation = %#v", name, operation)
		}
	}
	server := rpc.Server{Principals: identity.FixedUIDResolver{UID: 1001, Kind: identity.Agent}}
	if !server.Authorized(rpc.Peer{UID: 1001}, controlpolicy.Agent) {
		t.Fatal("agent was denied executor run")
	}
	if server.Authorized(rpc.Peer{UID: 1002}, controlpolicy.Agent) {
		t.Fatal("unknown peer obtained executor run")
	}
	if server.Authorized(rpc.Peer{UID: 1001}, controlpolicy.WorkloadLaunch) {
		t.Fatal("agent obtained workload-launch grant")
	}
}

func TestStartOperationRejectsAuthorityFieldsBeforeRunner(t *testing.T) {
	runner := &fakeJobRunner{}
	operations, err := Operations(runner, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	base := map[string]any{
		"operation":  "start",
		"request_id": "123e4567-e89b-12d3-a456-426614174000",
		"cwd":        ".",
		"argv":       []string{"/bin/true"},
	}
	for _, injected := range []map[string]any{
		{"job_id": strings.Repeat("a", 32)},
		{"id": strings.Repeat("a", 32)},
		{"policy_sha256": strings.Repeat("b", 64)},
		{"request_sha256": strings.Repeat("c", 64)},
		{"host_port": 43001},
		{"proxy_token": "secret"},
		{"proxy_url": "http://secret"},
		{"network_id": strings.Repeat("d", 64)},
		{"network_name": "loki-job-net"},
		{"gateway_image": "registry.example/gateway@sha256:" + strings.Repeat("e", 64)},
		{"backend_ref": "oci:private"},
		{"sandbox_sha256": strings.Repeat("f", 64)},
		{"endpoints": []map[string]any{{"name": "web", "port": 5173, "host_port": 43001}}},
		{"extra": true},
	} {
		request := map[string]any{}
		for key, value := range base {
			request[key] = value
		}
		for key, value := range injected {
			request[key] = value
		}
		raw, _ := json.Marshal(request)
		if _, err := operations["start"].Handle(t.Context(), raw); err == nil {
			t.Fatalf("authority field was accepted: %#v", injected)
		}
	}
	if got := runner.calls.Load(); got != 0 {
		t.Fatalf("runner calls = %d", got)
	}
}

func TestAsyncOperationsReturnDomainResults(t *testing.T) {
	jobID := strings.Repeat("c", 32)
	start := jobs.StartResult{
		RequestID: "123e4567-e89b-12d3-a456-426614174000",
		JobID:     jobID, State: jobs.StateAdmitted, Detached: true,
		DeadlineAt: time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano),
	}
	status := jobs.Status{JobID: jobID, State: jobs.StateRunning}
	output := jobs.OutputSnapshot{JobID: jobID, State: jobs.StateRunning, Output: "live"}
	cancel := jobs.CancelResult{JobID: jobID, Canceled: true, Status: jobs.Status{
		JobID: jobID, State: jobs.StateTerminal, Outcome: jobs.OutcomeCanceled, Cleanup: jobs.CleanupComplete,
	}}
	runner := &fakeJobRunner{startResult: start, status: status, output: output, cancel: cancel}
	operations, err := Operations(runner, time.Second)
	if err != nil {
		t.Fatal(err)
	}

	startRaw, _ := json.Marshal(map[string]any{
		"operation": "start", "request_id": start.RequestID,
		"cwd": ".", "argv": []string{"/bin/true"}, "timeout_seconds": 30,
		"network": "dependency-install",
		"endpoints": []map[string]any{{"name": "web", "port": 5173}},
	})
	if got, err := operations["start"].Handle(t.Context(), startRaw); err != nil || got != start {
		t.Fatalf("start = %#v, %v", got, err)
	}
	if runner.startRequest.Network != jobs.NetworkDependencyInstall ||
		len(runner.startRequest.Endpoints) != 1 ||
		runner.startRequest.Endpoints[0] != (jobs.EndpointRequest{Name: "web", Port: 5173}) {
		t.Fatalf("decoded start request = %#v", runner.startRequest)
	}
	for _, tc := range []struct {
		name string
		want any
	}{
		{"inspect", status},
		{"output", output},
		{"cancel", cancel},
	} {
		raw, _ := json.Marshal(map[string]any{"operation": tc.name, "job_id": jobID})
		got, err := operations[tc.name].Handle(t.Context(), raw)
		if err != nil {
			t.Fatalf("%s: %v", tc.name, err)
		}
		if !reflect.DeepEqual(got, tc.want) {
			t.Fatalf("%s = %#v, want %#v", tc.name, got, tc.want)
		}
	}
}

func TestRunOperationRejectsAuthorityFieldsBeforeRunner(t *testing.T) {
	runner := &fakeJobRunner{}
	operations, err := Operations(runner, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	for _, request := range []map[string]any{
		{"operation": "run", "cwd": ".", "argv": []string{"/bin/true"}, "id": strings.Repeat("a", 32)},
		{"operation": "run", "cwd": ".", "argv": []string{"/bin/true"}, "policy_sha256": strings.Repeat("b", 64)},
		{"operation": "run", "cwd": ".", "argv": []string{"/bin/true"}, "extra": true},
	} {
		raw, _ := json.Marshal(request)
		if _, err := operations["run"].Handle(t.Context(), raw); err == nil {
			t.Fatalf("authority field was accepted: %#v", request)
		}
	}
	if got := runner.calls.Load(); got != 0 {
		t.Fatalf("runner calls = %d", got)
	}
}

func TestRunOperationReturnsOnlyJobResult(t *testing.T) {
	exitCode := int64(7)
	want := jobs.RunResult{
		JobID: strings.Repeat("c", 32), ExitCode: &exitCode, Outcome: jobs.OutcomeExited,
		Output: "hello", Truncated: true, Cleanup: jobs.CleanupComplete,
	}
	runner := &fakeJobRunner{result: want}
	operations, err := Operations(runner, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(`{"operation":"run","cwd":".","argv":["/bin/true"]}`)
	result, err := operations["run"].Handle(t.Context(), raw)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(result)
	if err != nil {
		t.Fatal(err)
	}
	wantJSON := `{"job_id":"` + want.JobID + `","exit_code":7,"outcome":"exited","output":"hello","truncated":true,"cleanup":"complete"}`
	if string(encoded) != wantJSON {
		t.Fatalf("result = %s", encoded)
	}
}

func TestOperationsAndRunFailClosed(t *testing.T) {
	if _, err := Operations(nil, time.Second); err == nil {
		t.Fatal("nil runner was accepted")
	}
	if _, err := Operations(&fakeJobRunner{}, 0); err == nil {
		t.Fatal("zero timeout was accepted")
	}
	if _, err := Operations(&fakeJobRunner{}, maxRunTimeout+time.Second); err == nil {
		t.Fatal("oversized timeout was accepted")
	}
	if err := Run(t.Context(), Options{
		Socket:     filepath.Join(t.TempDir(), "executor.sock"),
		SocketGID:  os.Getgid(),
		AgentUID:   0,
		Runner:     &fakeJobRunner{},
		RunTimeout: time.Second,
	}); err == nil {
		t.Fatal("root agent UID was accepted")
	}
	if err := Run(t.Context(), Options{
		Socket:     "relative.sock",
		SocketGID:  os.Getgid(),
		AgentUID:   1001,
		Runner:     &fakeJobRunner{},
		RunTimeout: time.Second,
	}); err == nil {
		t.Fatal("relative executor socket was accepted")
	}
}
