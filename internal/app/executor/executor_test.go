package executor

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
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
	calls  atomic.Int32
	result jobs.RunResult
	err    error
}

func (r *fakeJobRunner) Run(_ context.Context, _ jobs.RunRequest) (jobs.RunResult, error) {
	r.calls.Add(1)
	return r.result, r.err
}

func TestOperationsExposeOnlyAgentRun(t *testing.T) {
	runner := &fakeJobRunner{}
	operations, err := Operations(runner, time.Second)
	if err != nil {
		t.Fatal(err)
	}
	if len(operations) != 1 {
		t.Fatalf("operations = %#v", operations)
	}
	run, ok := operations["run"]
	if !ok || run.Grant != controlpolicy.Agent || run.Timeout != time.Second {
		t.Fatalf("run operation = %#v", run)
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
	want := jobs.RunResult{JobID: strings.Repeat("c", 32), ExitCode: 7}
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
	if string(encoded) != `{"job_id":"`+want.JobID+`","exit_code":7}` {
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
