package sandbox

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
	"time"
)

func TestEngineClassifiesOOMOutcome(t *testing.T) {
	const version = "1.44"
	plan := validPlan(t)
	resource := plan.Resource()
	containerID := strings.Repeat("a", 64)
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
			_ = json.NewEncoder(w).Encode(map[string]any{"StatusCode": 137})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/json"):
			if removed {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":     containerID,
				"Config": map[string]any{"Labels": resource.labels()},
				"State":  map[string]any{"Status": "exited", "Running": false, "OOMKilled": true, "ExitCode": 137},
			})
		case r.Method == http.MethodDelete:
			removed = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
	result, err := engineForSocket(t, fakeDockerSocket(t, handler), nil).Run(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if result.ExitCode != 137 || result.Outcome != OutcomeOOMKilled || result.Cleanup != CleanupComplete || !removed {
		t.Fatalf("result = %#v, removed = %v", result, removed)
	}
}

func TestEngineClassifiesDeadlineAndCleansUp(t *testing.T) {
	const version = "1.44"
	plan := validPlan(t)
	resource := plan.Resource()
	containerID := strings.Repeat("b", 64)
	waitEntered := make(chan struct{})
	var once sync.Once
	var mu sync.Mutex
	stopped := false
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
			once.Do(func() { close(waitEntered) })
			<-r.Context().Done()
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/json"):
			mu.Lock()
			isStopped, isRemoved := stopped, removed
			mu.Unlock()
			if isRemoved {
				http.NotFound(w, r)
				return
			}
			status, running, exitCode := "running", true, int64(0)
			if isStopped {
				status, running, exitCode = "exited", false, 143
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":     containerID,
				"Config": map[string]any{"Labels": resource.labels()},
				"State":  map[string]any{"Status": status, "Running": running, "OOMKilled": false, "ExitCode": exitCode},
			})
		case strings.HasSuffix(r.URL.Path, "/stop"):
			mu.Lock()
			stopped = true
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodDelete:
			mu.Lock()
			removed = true
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
	engine := engineForSocket(t, fakeDockerSocket(t, handler), func(options *EngineOptions) {
		options.GracefulStopTimeout = time.Second
		options.CleanupTimeout = time.Second
	})
	ctx, cancel := context.WithTimeout(t.Context(), 25*time.Millisecond)
	defer cancel()
	result, err := engine.Run(ctx, plan)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("deadline error = %v", err)
	}
	if result.Outcome != OutcomeTimedOut || result.Cleanup != CleanupComplete {
		t.Fatalf("result = %#v", result)
	}
	select {
	case <-waitEntered:
	default:
		t.Fatal("wait request was not reached")
	}
}

func TestEngineCleanupRejectsForeignSameNameResource(t *testing.T) {
	const version = "1.44"
	plan := validPlan(t)
	resource := plan.Resource()
	containerID := strings.Repeat("c", 64)
	waitEntered := make(chan struct{})
	var once sync.Once
	mutated := false
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
			once.Do(func() { close(waitEntered) })
			<-r.Context().Done()
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/json"):
			labels := resource.labels()
			labels[resourcePolicyLabel] = strings.Repeat("f", 64)
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":     containerID,
				"Config": map[string]any{"Labels": labels},
				"State":  map[string]any{"Status": "running", "Running": true, "OOMKilled": false, "ExitCode": 0},
			})
		case strings.HasSuffix(r.URL.Path, "/stop"), strings.HasSuffix(r.URL.Path, "/kill"), r.Method == http.MethodDelete:
			mutated = true
			w.WriteHeader(http.StatusNoContent)
		default:
			http.NotFound(w, r)
		}
	})
	engine := engineForSocket(t, fakeDockerSocket(t, handler), nil)
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
	case <-time.After(time.Second):
		t.Fatal("wait request was not reached")
	}
	cancel()
	got := <-done
	if !errors.Is(got.err, context.Canceled) || !strings.Contains(got.err.Error(), "ownership does not match") {
		t.Fatalf("cleanup error = %v", got.err)
	}
	if got.result.Outcome != OutcomeCanceled || got.result.Cleanup != CleanupFailed || mutated {
		t.Fatalf("result = %#v, mutated = %v", got.result, mutated)
	}
}
