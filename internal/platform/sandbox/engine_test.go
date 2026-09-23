package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
	"time"
)

type requestRecorder struct {
	mu     sync.Mutex
	events []string
}

func (r *requestRecorder) add(request *http.Request) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.events = append(r.events, request.Method+" "+request.URL.RequestURI())
}

func (r *requestRecorder) snapshot() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]string(nil), r.events...)
}

func fakeDockerSocket(t *testing.T, handler http.Handler) string {
	t.Helper()
	socket := filepath.Join(t.TempDir(), "docker.sock")
	listener, err := net.Listen("unix", socket)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: handler}
	done := make(chan error, 1)
	go func() { done <- server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
		select {
		case err := <-done:
			if err != nil && !errors.Is(err, http.ErrServerClosed) {
				t.Errorf("fake Docker server: %v", err)
			}
		case <-time.After(time.Second):
			t.Error("fake Docker server did not stop")
		}
	})
	return socket
}

func validPlan(t *testing.T) Plan {
	t.Helper()
	policy, err := NewPolicy(validPolicyOptions())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := policy.Plan(validWorkloadSpec())
	if err != nil {
		t.Fatal(err)
	}
	return plan
}

func engineForSocket(t *testing.T, socket string, mutate func(*EngineOptions)) *Engine {
	t.Helper()
	uid := uint32(os.Getuid())
	options := EngineOptions{
		Socket:         socket,
		ExpectedUID:    &uid,
		RequestBytes:   256 << 10,
		ResponseBytes:  1 << 20,
		ControlTimeout: 2 * time.Second,
		CleanupTimeout: 2 * time.Second,
	}
	if mutate != nil {
		mutate(&options)
	}
	engine, err := NewEngine(options)
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func TestEngineFiniteLifecycle(t *testing.T) {
	const version = "1.44"
	containerID := strings.Repeat("d", 64)
	plan := validPlan(t)
	resource := plan.Resource()
	recorder := &requestRecorder{}
	removed := false
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.add(r)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/version":
			_ = json.NewEncoder(w).Encode(map[string]any{"ApiVersion": version, "Version": "fixture"})
		case r.Method == http.MethodPost && r.URL.Path == "/v"+version+"/containers/create":
			if r.URL.Query().Get("name") != resource.Name() {
				t.Errorf("container name = %q", r.URL.Query().Get("name"))
			}
			var create dockerCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&create); err != nil {
				t.Errorf("decode create request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if !create.HostConfig.ReadonlyRootfs || create.HostConfig.NetworkMode != "none" || create.Image == "" ||
				!resource.owns(create.Labels) {
				t.Errorf("create security envelope = %#v", create)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": containerID})
		case r.Method == http.MethodPost && r.URL.Path == "/v"+version+"/containers/"+resource.Name()+"/start":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/v"+version+"/containers/"+resource.Name()+"/wait":
			_ = json.NewEncoder(w).Encode(map[string]any{"StatusCode": 7})
		case r.Method == http.MethodGet && r.URL.Path == "/v"+version+"/containers/"+resource.Name()+"/json":
			if removed {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":     containerID,
				"Config": map[string]any{"Labels": resource.labels()},
				"State":  map[string]any{"Status": "exited", "Running": false, "OOMKilled": false, "ExitCode": 7},
			})
		case r.Method == http.MethodDelete && r.URL.Path == "/v"+version+"/containers/"+containerID:
			removed = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected Docker request: %s %s", r.Method, r.URL.RequestURI())
			http.NotFound(w, r)
		}
	})
	socket := fakeDockerSocket(t, handler)
	result, err := engineForSocket(t, socket, nil).Run(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 7 || result.Outcome != OutcomeExited || result.Cleanup != CleanupComplete {
		t.Fatalf("result = %#v", result)
	}
	want := []string{
		"GET /version",
		"POST /v" + version + "/containers/create?name=" + resource.Name(),
		"POST /v" + version + "/containers/" + resource.Name() + "/start",
		"POST /v" + version + "/containers/" + resource.Name() + "/wait?condition=not-running",
		"GET /v" + version + "/containers/" + resource.Name() + "/json",
		"GET /v" + version + "/containers/" + resource.Name() + "/json",
		"DELETE /v" + version + "/containers/" + containerID + "?force=1&v=1",
		"GET /v" + version + "/containers/" + resource.Name() + "/json",
	}
	if got := recorder.snapshot(); strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("Docker request sequence = %#v, want %#v", got, want)
	}
}

func TestEngineVerifiesPeerAndBoundsResponses(t *testing.T) {
	t.Run("peer", func(t *testing.T) {
		socket := fakeDockerSocket(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"ApiVersion": "1.44"})
		}))
		uid := uint32(os.Getuid()) + 1
		engine, err := NewEngine(EngineOptions{Socket: socket, ExpectedUID: &uid})
		if err != nil {
			t.Fatal(err)
		}
		if _, err = engine.Run(t.Context(), validPlan(t)); err == nil || !strings.Contains(err.Error(), "not trusted") {
			t.Fatalf("peer error = %v", err)
		}
	})
	t.Run("old-api", func(t *testing.T) {
		socket := fakeDockerSocket(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			_ = json.NewEncoder(w).Encode(map[string]any{"ApiVersion": "1.40"})
		}))
		if _, err := engineForSocket(t, socket, nil).Run(t.Context(), validPlan(t)); err == nil || !strings.Contains(err.Error(), "too old") {
			t.Fatalf("API version error = %v", err)
		}
	})
	t.Run("oversized-version", func(t *testing.T) {
		socket := fakeDockerSocket(t, http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write([]byte(strings.Repeat("x", 4097)))
		}))
		engine := engineForSocket(t, socket, func(o *EngineOptions) { o.ResponseBytes = 4096 })
		if _, err := engine.Run(t.Context(), validPlan(t)); err == nil || !strings.Contains(err.Error(), "exceeds limit") {
			t.Fatalf("response limit error = %v", err)
		}
	})
	t.Run("invalid-container-id", func(t *testing.T) {
		containerID := strings.Repeat("a", 64)
		plan := validPlan(t)
		resource := plan.Resource()
		removed := false
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/version":
				_ = json.NewEncoder(w).Encode(map[string]any{"ApiVersion": "1.44"})
			case r.URL.Path == "/v1.44/containers/create":
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(map[string]any{"Id": "../../unsafe"})
			case r.Method == http.MethodGet && r.URL.Path == "/v1.44/containers/"+resource.Name()+"/json":
				if removed {
					http.NotFound(w, r)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"Id":     containerID,
					"Config": map[string]any{"Labels": resource.labels()},
					"State":  map[string]any{"Status": "created", "Running": false, "OOMKilled": false, "ExitCode": 0},
				})
			case r.Method == http.MethodDelete && r.URL.Path == "/v1.44/containers/"+containerID:
				removed = true
				w.WriteHeader(http.StatusNoContent)
			default:
				http.NotFound(w, r)
			}
		})
		socket := fakeDockerSocket(t, handler)
		result, err := engineForSocket(t, socket, nil).Run(t.Context(), plan)
		if err == nil || !strings.Contains(err.Error(), "invalid container ID") {
			t.Fatalf("container ID error = %v", err)
		}
		if result.Outcome != OutcomeLaunchFailed || result.Cleanup != CleanupComplete || !removed {
			t.Fatalf("result = %#v, removed = %v", result, removed)
		}
	})
	t.Run("invalid-exit-code", func(t *testing.T) {
		const version = "1.44"
		containerID := strings.Repeat("f", 64)
		plan := validPlan(t)
		resource := plan.Resource()
		removed := false
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			switch {
			case r.URL.Path == "/version":
				_ = json.NewEncoder(w).Encode(map[string]any{"ApiVersion": version})
			case r.URL.Path == "/v"+version+"/containers/create":
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(map[string]any{"Id": containerID})
			case strings.HasSuffix(r.URL.Path, "/start"):
				w.WriteHeader(http.StatusNoContent)
			case strings.HasSuffix(r.URL.Path, "/wait"):
				_ = json.NewEncoder(w).Encode(map[string]any{"StatusCode": 999})
			case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/json"):
				if removed {
					http.NotFound(w, r)
					return
				}
				_ = json.NewEncoder(w).Encode(map[string]any{
					"Id":     containerID,
					"Config": map[string]any{"Labels": resource.labels()},
					"State":  map[string]any{"Status": "exited", "Running": false, "OOMKilled": false, "ExitCode": 0},
				})
			case r.Method == http.MethodDelete:
				removed = true
				w.WriteHeader(http.StatusNoContent)
			default:
				http.NotFound(w, r)
			}
		})
		socket := fakeDockerSocket(t, handler)
		result, err := engineForSocket(t, socket, nil).Run(t.Context(), plan)
		if err == nil || !strings.Contains(err.Error(), "invalid exit code") {
			t.Fatalf("exit-code error = %v", err)
		}
		if result.Outcome != OutcomeUnknown || result.Cleanup != CleanupComplete || !removed {
			t.Fatalf("result = %#v, removed = %v", result, removed)
		}
	})
}

func TestEngineCancellationUsesGracefulStopThenKillFallback(t *testing.T) {
	for _, tc := range []struct {
		name            string
		stopStatus      int
		killStatus      int
		wantKill        bool
		wantCleanup     CleanupStatus
		wantCleanupText string
	}{
		{name: "graceful-stop", stopStatus: http.StatusNoContent, wantCleanup: CleanupComplete},
		{name: "kill-fallback", stopStatus: http.StatusInternalServerError, killStatus: http.StatusNoContent, wantKill: true, wantCleanup: CleanupComplete},
		{name: "kill-failure", stopStatus: http.StatusInternalServerError, killStatus: http.StatusInternalServerError, wantKill: true, wantCleanup: CleanupFailed, wantCleanupText: "HTTP 500"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const version = "1.44"
			containerID := strings.Repeat("e", 64)
			plan := validPlan(t)
			resource := plan.Resource()
			recorder := &requestRecorder{}
			waitEntered := make(chan struct{})
			var once sync.Once
			var stateMu sync.Mutex
			stopped := false
			removed := false
			handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				recorder.add(r)
				switch {
				case r.URL.Path == "/version":
					_ = json.NewEncoder(w).Encode(map[string]any{"ApiVersion": version})
				case r.URL.Path == "/v"+version+"/containers/create":
					w.WriteHeader(http.StatusCreated)
					_ = json.NewEncoder(w).Encode(map[string]any{"Id": containerID})
				case strings.HasSuffix(r.URL.Path, "/start"):
					w.WriteHeader(http.StatusNoContent)
				case strings.HasSuffix(r.URL.Path, "/wait"):
					once.Do(func() { close(waitEntered) })
					<-r.Context().Done()
				case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/json"):
					stateMu.Lock()
					isStopped, isRemoved := stopped, removed
					stateMu.Unlock()
					if isRemoved {
						http.NotFound(w, r)
						return
					}
					status := "running"
					running := true
					exitCode := int64(0)
					if isStopped {
						status, running, exitCode = "exited", false, 143
					}
					_ = json.NewEncoder(w).Encode(map[string]any{
						"Id":     containerID,
						"Config": map[string]any{"Labels": resource.labels()},
						"State":  map[string]any{"Status": status, "Running": running, "OOMKilled": false, "ExitCode": exitCode},
					})
				case strings.HasSuffix(r.URL.Path, "/stop"):
					if tc.stopStatus == http.StatusNoContent {
						stateMu.Lock()
						stopped = true
						stateMu.Unlock()
					}
					w.WriteHeader(tc.stopStatus)
				case strings.HasSuffix(r.URL.Path, "/kill"):
					if tc.killStatus == http.StatusNoContent {
						stateMu.Lock()
						stopped = true
						stateMu.Unlock()
					}
					w.WriteHeader(tc.killStatus)
				case r.Method == http.MethodDelete:
					stateMu.Lock()
					removed = true
					stateMu.Unlock()
					w.WriteHeader(http.StatusNoContent)
				default:
					http.NotFound(w, r)
				}
			})
			engine := engineForSocket(t, fakeDockerSocket(t, handler), func(options *EngineOptions) {
				options.CleanupTimeout = time.Second
				options.GracefulStopTimeout = time.Second
			})
			ctx, cancel := context.WithCancel(t.Context())
			type runResult struct {
				result Result
				err    error
			}
			done := make(chan runResult, 1)
			go func() {
				result, err := engine.Run(ctx, plan)
				done <- runResult{result: result, err: err}
			}()
			select {
			case <-waitEntered:
			case <-time.After(2 * time.Second):
				t.Fatal("wait request was not reached")
			}
			cancel()
			var got runResult
			select {
			case got = <-done:
			case <-time.After(3 * time.Second):
				t.Fatal("canceled sandbox run did not finish")
			}
			if !errors.Is(got.err, context.Canceled) || got.result.Outcome != OutcomeCanceled || got.result.Cleanup != tc.wantCleanup {
				t.Fatalf("run result = %#v, error = %v", got.result, got.err)
			}
			if tc.wantCleanupText != "" && !strings.Contains(got.err.Error(), tc.wantCleanupText) {
				t.Fatalf("cleanup error = %v, want %q", got.err, tc.wantCleanupText)
			}
			events := strings.Join(recorder.snapshot(), "\n")
			if !strings.Contains(events, "/stop?t=1") {
				t.Fatalf("cleanup sequence lacks graceful stop: %s", events)
			}
			if strings.Contains(events, "/kill?signal=KILL") != tc.wantKill {
				t.Fatalf("kill fallback mismatch: %s", events)
			}
			if tc.wantCleanup == CleanupComplete {
				if !strings.Contains(events, "DELETE /v"+version+"/containers/"+containerID+"?force=1&v=1") {
					t.Fatalf("cleanup sequence lacks removal: %s", events)
				}
			}
		})
	}
}

func TestEngineOptionsRejectUntrustedConfiguration(t *testing.T) {
	uid := uint32(os.Getuid())
	for _, options := range []EngineOptions{
		{Socket: "relative.sock", ExpectedUID: &uid},
		{Socket: "/run/docker.sock"},
		{Socket: "/run/docker.sock", ExpectedUID: &uid, RequestBytes: 1024},
		{Socket: "/run/docker.sock", ExpectedUID: &uid, ResponseBytes: 1024},
		{Socket: "/run/docker.sock", ExpectedUID: &uid, ControlTimeout: time.Millisecond},
		{Socket: "/run/docker.sock", ExpectedUID: &uid, GracefulStopTimeout: time.Millisecond},
	} {
		if _, err := NewEngine(options); err == nil {
			t.Fatalf("invalid EngineOptions accepted: %#v", options)
		}
	}
}

func TestUnexpectedStatusIncludesBoundedDockerMessage(t *testing.T) {
	engine := &Engine{responseBytes: 4096}
	response := &http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(strings.NewReader(`{"message":"conflicting options: port publishing and the container type network mode"}`)),
	}
	err := engine.unexpectedStatus(response)
	if err == nil || !strings.Contains(err.Error(), "HTTP 400: conflicting options: port publishing") {
		t.Fatalf("unexpected status error = %v", err)
	}

	response = &http.Response{
		StatusCode: http.StatusBadRequest,
		Body:       io.NopCloser(strings.NewReader(`{"message":"unsafe\nmessage"}`)),
	}
	err = engine.unexpectedStatus(response)
	if err == nil || err.Error() != "sandbox Docker daemon returned HTTP 400" {
		t.Fatalf("unsafe daemon message was exposed: %v", err)
	}
}
