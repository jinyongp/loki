package remote

import (
	"bufio"
	"context"
	"encoding/json"
	"net"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"

	"loki/internal/work/jobs"
)

type fakeResponse func(map[string]json.RawMessage) []byte

type requestLog struct {
	mu         sync.Mutex
	operations []string
}

func (l *requestLog) add(request map[string]json.RawMessage) {
	var operation string
	_ = json.Unmarshal(request["operation"], &operation)
	l.mu.Lock()
	defer l.mu.Unlock()
	l.operations = append(l.operations, operation)
}

func (l *requestLog) snapshot() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return append([]string(nil), l.operations...)
}

func fakeLauncherSocket(t *testing.T, respond fakeResponse) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "launcher.sock")
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: socket, Net: "unix"})
	if err != nil {
		t.Fatal(err)
	}

	var workers sync.WaitGroup
	done := make(chan struct{})
	go func() {
		defer close(done)
		for {
			conn, err := listener.AcceptUnix()
			if err != nil {
				return
			}
			workers.Add(1)
			go func() {
				defer workers.Done()
				defer conn.Close()
				raw, err := bufio.NewReader(conn).ReadBytes('\n')
				if err != nil {
					return
				}
				var request map[string]json.RawMessage
				if json.Unmarshal(raw, &request) != nil {
					return
				}
				response := respond(request)
				if len(response) != 0 {
					_, _ = conn.Write(append(response, '\n'))
				}
			}()
		}
	}()
	t.Cleanup(func() {
		_ = listener.Close()
		select {
		case <-done:
		case <-time.After(time.Second):
			t.Fatal("fake launcher accept loop did not stop")
		}
		waitDone := make(chan struct{})
		go func() {
			workers.Wait()
			close(waitDone)
		}()
		select {
		case <-waitDone:
		case <-time.After(time.Second):
			t.Fatal("fake launcher workers did not stop")
		}
	})
	return socket
}

func validOptions(t *testing.T, socket string) Options {
	t.Helper()
	uid := uint32(os.Getuid())
	return Options{
		Socket:       socket,
		ExpectedUID:  &uid,
		PolicySHA256: strings.Repeat("a", 64),
		Timeout:      time.Second,
	}
}

func validWorkload() jobs.Workload {
	return jobs.Workload{
		ID:   strings.Repeat("b", 32),
		CWD:  ".",
		Argv: []string{"/bin/true", "argument"},
	}
}

func asyncWorkload(t *testing.T) jobs.Workload {
	t.Helper()
	requestID := "123e4567-e89b-12d3-a456-426614174000"
	id, err := jobs.JobIDForRequestID(requestID)
	if err != nil {
		t.Fatal(err)
	}
	normalized, fingerprint, err := jobs.NormalizeStartRequest(jobs.StartRequest{
		RequestID: requestID, CWD: ".", Argv: []string{"/bin/sleep", "10"}, TimeoutSeconds: 30,
		Network:   jobs.NetworkDependencyInstall,
		Endpoints: []jobs.EndpointRequest{{Name: "web", Port: 5173}},
	})
	if err != nil {
		t.Fatal(err)
	}
	return jobs.Workload{
		ID: id, RequestID: requestID, RequestSHA256: fingerprint,
		CWD: normalized.CWD, Argv: normalized.Argv, TimeoutSeconds: normalized.TimeoutSeconds,
		Network: normalized.Network, Endpoints: normalized.Endpoints,
	}
}

func response(t *testing.T, value any) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"ok": true, "result": value})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func errorResponse(t *testing.T, message string) []byte {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"ok": false, "error": message})
	if err != nil {
		t.Fatal(err)
	}
	return raw
}

func startResult(workload jobs.Workload) jobs.StartResult {
	return jobs.StartResult{
		RequestID:  workload.RequestID,
		JobID:      workload.ID,
		State:      jobs.StateAdmitted,
		Detached:   true,
		DeadlineAt: time.Date(2030, 1, 1, 0, 0, 0, 0, time.UTC).Format(time.RFC3339Nano),
	}
}

func completedCancel(id string, canceled bool) jobs.CancelResult {
	return jobs.CancelResult{
		JobID:    id,
		Canceled: canceled,
		Status: jobs.Status{
			JobID: id, State: jobs.StateTerminal, Outcome: jobs.OutcomeCanceled,
			Cleanup: jobs.CleanupComplete,
		},
	}
}

func TestLauncherUsesStartThenWaitWithTrustedPolicy(t *testing.T) {
	log := &requestLog{}
	workload := validWorkload()
	socket := fakeLauncherSocket(t, func(request map[string]json.RawMessage) []byte {
		log.add(request)
		var operation string
		_ = json.Unmarshal(request["operation"], &operation)
		switch operation {
		case "start":
			if len(request) != 5 {
				t.Fatalf("start request keys = %#v", request)
			}
			var id, policy, cwd string
			var argv []string
			for key, target := range map[string]any{
				"id": &id, "policy_sha256": &policy, "cwd": &cwd, "argv": &argv,
			} {
				raw, ok := request[key]
				if !ok || json.Unmarshal(raw, target) != nil {
					t.Fatalf("start request[%q] = %s", key, raw)
				}
			}
			if id != workload.ID || policy != strings.Repeat("a", 64) ||
				cwd != "." || len(argv) != 2 || argv[0] != "/bin/true" || argv[1] != "argument" {
				t.Fatalf("start request = %#v", request)
			}
			return response(t, startResult(workload))
		case "wait":
			return response(t, map[string]any{
				"exit_code": 9, "outcome": "exited", "output": "hello",
				"truncated": false, "cleanup": "complete",
			})
		default:
			t.Fatalf("unexpected operation %q", operation)
			return nil
		}
	})
	launcher, err := New(validOptions(t, socket))
	if err != nil {
		t.Fatal(err)
	}
	result, err := launcher.Run(t.Context(), workload)
	if err != nil {
		t.Fatal(err)
	}
	workload.Argv[1] = "mutated"
	if result.ExitCode == nil || *result.ExitCode != 9 || result.Outcome != jobs.OutcomeExited ||
		result.Output.Text != "hello" || result.Output.Truncated || result.Cleanup != jobs.CleanupComplete {
		t.Fatalf("result = %#v", result)
	}
	if got := strings.Join(log.snapshot(), ","); got != "start,wait" {
		t.Fatalf("operations = %q", got)
	}
}

func TestLauncherAsyncLifecycleUsesTrustedPolicyAndPublicNeutralResults(t *testing.T) {
	log := &requestLog{}
	workload := asyncWorkload(t)
	socket := fakeLauncherSocket(t, func(request map[string]json.RawMessage) []byte {
		log.add(request)
		var operation string
		_ = json.Unmarshal(request["operation"], &operation)
		switch operation {
		case "start":
			var requestID, requestSHA, policy string
			var timeout int
			var network jobs.NetworkProfile
			var endpoints []jobs.EndpointRequest
			_ = json.Unmarshal(request["request_id"], &requestID)
			_ = json.Unmarshal(request["request_sha256"], &requestSHA)
			_ = json.Unmarshal(request["policy_sha256"], &policy)
			_ = json.Unmarshal(request["timeout_seconds"], &timeout)
			_ = json.Unmarshal(request["network"], &network)
			_ = json.Unmarshal(request["endpoints"], &endpoints)
			if requestID != workload.RequestID || requestSHA != workload.RequestSHA256 ||
				policy != strings.Repeat("a", 64) || timeout != 30 ||
				network != jobs.NetworkDependencyInstall || len(endpoints) != 1 ||
				endpoints[0] != (jobs.EndpointRequest{Name: "web", Port: 5173}) {
				t.Fatalf("async start request = %#v", request)
			}
			for _, forbidden := range []string{
				"host_port", "proxy_token", "proxy_url", "network_id", "network_name",
				"gateway_image", "backend_ref", "sandbox_sha256",
			} {
				if _, ok := request[forbidden]; ok {
					t.Fatalf("launcher request exposed %s", forbidden)
				}
			}
			return response(t, startResult(workload))
		case "inspect":
			return response(t, jobs.Status{JobID: workload.ID, State: jobs.StateRunning})
		case "output":
			return response(t, jobs.OutputSnapshot{
				JobID: workload.ID, State: jobs.StateRunning, Output: "live-output", Truncated: true,
			})
		case "cancel":
			return response(t, completedCancel(workload.ID, true))
		default:
			t.Fatalf("unexpected operation %q", operation)
			return nil
		}
	})
	launcher, err := New(validOptions(t, socket))
	if err != nil {
		t.Fatal(err)
	}
	started, err := launcher.Start(t.Context(), workload)
	if err != nil || started.JobID != workload.ID || !started.Detached {
		t.Fatalf("start = %#v, %v", started, err)
	}
	status, err := launcher.Inspect(t.Context(), workload.ID)
	if err != nil || status.State != jobs.StateRunning {
		t.Fatalf("inspect = %#v, %v", status, err)
	}
	output, err := launcher.Output(t.Context(), workload.ID)
	if err != nil || output.Output != "live-output" || !output.Truncated || output.Complete {
		t.Fatalf("output = %#v, %v", output, err)
	}
	canceled, err := launcher.Cancel(t.Context(), workload.ID)
	if err != nil || !canceled.Canceled || canceled.Status.Cleanup != jobs.CleanupComplete {
		t.Fatalf("cancel = %#v, %v", canceled, err)
	}
	if got := strings.Join(log.snapshot(), ","); got != "start,inspect,output,cancel" {
		t.Fatalf("operations = %q", got)
	}
}

func TestLauncherPreservesReplayConflictAcrossRPC(t *testing.T) {
	workload := asyncWorkload(t)
	socket := fakeLauncherSocket(t, func(request map[string]json.RawMessage) []byte {
		var operation string
		_ = json.Unmarshal(request["operation"], &operation)
		if operation == "start" {
			return errorResponse(t, jobs.ErrReplayConflict.Error())
		}
		return errorResponse(t, "unexpected")
	})
	launcher, err := New(validOptions(t, socket))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = launcher.Start(t.Context(), workload); err == nil ||
		err.Error() != jobs.ErrReplayConflict.Error() {
		t.Fatalf("replay conflict = %v", err)
	}
}

func TestLauncherCancelsAfterWaitFailure(t *testing.T) {
	log := &requestLog{}
	workload := validWorkload()
	socket := fakeLauncherSocket(t, func(request map[string]json.RawMessage) []byte {
		log.add(request)
		var operation string
		_ = json.Unmarshal(request["operation"], &operation)
		switch operation {
		case "start":
			return response(t, startResult(workload))
		case "wait":
			return errorResponse(t, "synthetic wait failure")
		case "cancel":
			return response(t, completedCancel(workload.ID, true))
		default:
			return errorResponse(t, "unexpected")
		}
	})
	launcher, err := New(validOptions(t, socket))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = launcher.Run(t.Context(), workload); err == nil || !strings.Contains(err.Error(), "synthetic wait failure") {
		t.Fatalf("wait failure = %v", err)
	}
	if got := strings.Join(log.snapshot(), ","); got != "start,wait,cancel" {
		t.Fatalf("operations = %q", got)
	}
}

func TestLauncherCancelsAfterUncertainStart(t *testing.T) {
	log := &requestLog{}
	workload := validWorkload()
	socket := fakeLauncherSocket(t, func(request map[string]json.RawMessage) []byte {
		log.add(request)
		var operation string
		_ = json.Unmarshal(request["operation"], &operation)
		if operation == "start" {
			return nil
		}
		if operation == "cancel" {
			return response(t, completedCancel(workload.ID, false))
		}
		return errorResponse(t, "unexpected")
	})
	launcher, err := New(validOptions(t, socket))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = launcher.Run(t.Context(), workload); err == nil {
		t.Fatal("uncertain start returned success")
	}
	if got := strings.Join(log.snapshot(), ","); got != "start,cancel" {
		t.Fatalf("operations = %q", got)
	}
}

func TestLauncherStrictlyRejectsInvalidWaitResultsAndCleansUp(t *testing.T) {
	tests := []struct {
		name     string
		response []byte
	}{
		{
			name: "unknown-field",
			response: response(t, map[string]any{
				"exit_code": 0, "outcome": "exited", "output": "", "truncated": false,
				"cleanup": "complete", "extra": true,
			}),
		},
		{
			name: "invalid-exit",
			response: response(t, map[string]any{
				"exit_code": 999, "outcome": "exited", "output": "", "truncated": false,
				"cleanup": "complete",
			}),
		},
		{
			name: "missing-exit",
			response: response(t, map[string]any{
				"outcome": "exited", "output": "", "truncated": false, "cleanup": "complete",
			}),
		},
		{
			name: "invalid-outcome",
			response: response(t, map[string]any{
				"outcome": "unsupported", "output": "", "truncated": false, "cleanup": "complete",
			}),
		},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			log := &requestLog{}
			workload := validWorkload()
			socket := fakeLauncherSocket(t, func(request map[string]json.RawMessage) []byte {
				log.add(request)
				var operation string
				_ = json.Unmarshal(request["operation"], &operation)
				switch operation {
				case "start":
					return response(t, startResult(workload))
				case "wait":
					return tc.response
				case "cancel":
					return response(t, completedCancel(workload.ID, false))
				default:
					return nil
				}
			})
			launcher, err := New(validOptions(t, socket))
			if err != nil {
				t.Fatal(err)
			}
			if _, err = launcher.Run(t.Context(), workload); err == nil {
				t.Fatal("invalid launcher result was accepted")
			}
			if got := strings.Join(log.snapshot(), ","); got != "start,wait,cancel" {
				t.Fatalf("operations = %q", got)
			}
		})
	}
}

func TestLauncherRejectsUntrustedPeer(t *testing.T) {
	workload := validWorkload()
	socket := fakeLauncherSocket(t, func(map[string]json.RawMessage) []byte {
		return response(t, startResult(workload))
	})
	options := validOptions(t, socket)
	uid := uint32(os.Getuid()) ^ 1
	options.ExpectedUID = &uid
	launcher, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = launcher.Run(t.Context(), workload); err == nil ||
		!strings.Contains(err.Error(), "socket owner is not trusted") {
		t.Fatalf("peer error = %v", err)
	}
}

func TestLauncherConfigurationFailsClosed(t *testing.T) {
	socket := filepath.Join(t.TempDir(), "launcher.sock")
	uid := uint32(os.Getuid())
	tests := []struct {
		name   string
		mutate func(*Options)
	}{
		{"relative-socket", func(o *Options) { o.Socket = "launcher.sock" }},
		{"root-socket", func(o *Options) { o.Socket = "/" }},
		{"missing-uid", func(o *Options) { o.ExpectedUID = nil }},
		{"bad-policy", func(o *Options) { o.PolicySHA256 = "bad" }},
		{"short-timeout", func(o *Options) { o.Timeout = 0 }},
		{"long-timeout", func(o *Options) { o.Timeout = maxRunTimeout + time.Second }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			options := Options{Socket: socket, ExpectedUID: &uid, PolicySHA256: strings.Repeat("a", 64), Timeout: time.Second}
			test.mutate(&options)
			if _, err := New(options); err == nil {
				t.Fatal("invalid remote launcher configuration was accepted")
			}
		})
	}
}

func TestNilLauncherFailsClosed(t *testing.T) {
	var launcher *Launcher
	if _, err := launcher.Run(context.Background(), validWorkload()); err == nil {
		t.Fatal("nil launcher was accepted")
	}
	if _, err := launcher.Start(context.Background(), validWorkload()); err == nil {
		t.Fatal("nil async launcher was accepted")
	}
	if _, err := launcher.Inspect(context.Background(), strings.Repeat("a", 32)); err == nil {
		t.Fatal("nil launcher inspect was accepted")
	}
}
