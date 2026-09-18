package sandbox

import (
	"context"
	"encoding/json"
	"errors"
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
	recorder := &requestRecorder{}
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		recorder.add(r)
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/version":
			_ = json.NewEncoder(w).Encode(map[string]any{"ApiVersion": version, "Version": "fixture"})
		case r.Method == http.MethodPost && r.URL.Path == "/v"+version+"/containers/create":
			if r.URL.Query().Get("name") != "loki-job-"+validWorkloadSpec().ID {
				t.Errorf("container name = %q", r.URL.Query().Get("name"))
			}
			var create dockerCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&create); err != nil {
				t.Errorf("decode create request: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if !create.HostConfig.ReadonlyRootfs || create.HostConfig.NetworkMode != "none" || create.Image == "" {
				t.Errorf("create security envelope = %#v", create)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": containerID})
		case r.Method == http.MethodPost && r.URL.Path == "/v"+version+"/containers/"+containerID+"/start":
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodPost && r.URL.Path == "/v"+version+"/containers/"+containerID+"/wait":
			_ = json.NewEncoder(w).Encode(map[string]any{"StatusCode": 7})
		case r.Method == http.MethodDelete && r.URL.Path == "/v"+version+"/containers/"+containerID:
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected Docker request: %s %s", r.Method, r.URL.RequestURI())
			http.NotFound(w, r)
		}
	})
	socket := fakeDockerSocket(t, handler)
	result, err := engineForSocket(t, socket, nil).Run(t.Context(), validPlan(t))
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 7 {
		t.Fatalf("exit code = %d", result.ExitCode)
	}
	want := []string{
		"GET /version",
		"POST /v" + version + "/containers/create?name=loki-job-" + validWorkloadSpec().ID,
		"POST /v" + version + "/containers/" + containerID + "/start",
		"POST /v" + version + "/containers/" + containerID + "/wait?condition=not-running",
		"DELETE /v" + version + "/containers/" + containerID + "?force=1&v=1",
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
		removed := false
		handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.URL.Path == "/version" {
				_ = json.NewEncoder(w).Encode(map[string]any{"ApiVersion": "1.44"})
				return
			}
			if r.URL.Path == "/v1.44/containers/create" {
				w.WriteHeader(http.StatusCreated)
				_ = json.NewEncoder(w).Encode(map[string]any{"Id": "../../unsafe"})
				return
			}
			if r.Method == http.MethodDelete && r.URL.Path == "/v1.44/containers/loki-job-"+validWorkloadSpec().ID {
				removed = true
				w.WriteHeader(http.StatusNoContent)
				return
			}
			http.NotFound(w, r)
		})
		socket := fakeDockerSocket(t, handler)
		if _, err := engineForSocket(t, socket, nil).Run(t.Context(), validPlan(t)); err == nil || !strings.Contains(err.Error(), "invalid container ID") {
			t.Fatalf("container ID error = %v", err)
		}
		if !removed {
			t.Fatal("created container was not removed by its fixed job name")
		}
	})
	t.Run("invalid-exit-code", func(t *testing.T) {
		const version = "1.44"
		containerID := strings.Repeat("f", 64)
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
			case strings.HasSuffix(r.URL.Path, "/kill"):
				w.WriteHeader(http.StatusConflict)
			case r.Method == http.MethodDelete:
				w.WriteHeader(http.StatusNoContent)
			default:
				http.NotFound(w, r)
			}
		})
		socket := fakeDockerSocket(t, handler)
		if _, err := engineForSocket(t, socket, nil).Run(t.Context(), validPlan(t)); err == nil || !strings.Contains(err.Error(), "invalid exit code") {
			t.Fatalf("exit-code error = %v", err)
		}
	})
}

func TestEngineCancellationKillsAndRemoves(t *testing.T) {
	for _, tc := range []struct {
		name            string
		killStatus      int
		blockKill       bool
		wantCleanupText string
	}{
		{name: "cleanup-success", killStatus: http.StatusNoContent},
		{name: "cleanup-error", killStatus: http.StatusInternalServerError, wantCleanupText: "HTTP 500"},
		{name: "cleanup-timeout", blockKill: true, wantCleanupText: "context deadline exceeded"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			const version = "1.44"
			containerID := strings.Repeat("e", 64)
			recorder := &requestRecorder{}
			waitEntered := make(chan struct{})
			var once sync.Once
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
				case strings.HasSuffix(r.URL.Path, "/kill"):
					if tc.blockKill {
						<-r.Context().Done()
						return
					}
					w.WriteHeader(tc.killStatus)
				case r.Method == http.MethodDelete:
					w.WriteHeader(http.StatusNoContent)
				default:
					http.NotFound(w, r)
				}
			})
			socket := fakeDockerSocket(t, handler)
			engine := engineForSocket(t, socket, func(options *EngineOptions) {
				if tc.blockKill {
					options.CleanupTimeout = time.Second
				}
			})
			plan := validPlan(t)
			ctx, cancel := context.WithCancel(t.Context())
			result := make(chan error, 1)
			go func() {
				_, err := engine.Run(ctx, plan)
				result <- err
			}()
			select {
			case <-waitEntered:
			case <-time.After(2 * time.Second):
				t.Fatal("wait request was not reached")
			}
			cancel()
			var err error
			select {
			case err = <-result:
			case <-time.After(3 * time.Second):
				t.Fatal("canceled sandbox run did not finish")
			}
			if !errors.Is(err, context.Canceled) {
				t.Fatalf("cancellation error = %v", err)
			}
			if tc.wantCleanupText != "" && !strings.Contains(err.Error(), tc.wantCleanupText) {
				t.Fatalf("cleanup error = %v, want %q", err, tc.wantCleanupText)
			}
			events := strings.Join(recorder.snapshot(), "\n")
			if !strings.Contains(events, "/kill?signal=KILL") || !strings.Contains(events, "DELETE /v"+version+"/containers/"+containerID+"?force=1&v=1") {
				t.Fatalf("cleanup sequence = %s", events)
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
	} {
		if _, err := NewEngine(options); err == nil {
			t.Fatalf("invalid EngineOptions accepted: %#v", options)
		}
	}
}
