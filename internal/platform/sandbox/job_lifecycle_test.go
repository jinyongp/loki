package sandbox

import (
	"encoding/binary"
	"encoding/json"
	"errors"
	"net/http"
	"strings"
	"sync"
	"testing"
)

func dockerLogFrame(stream byte, data string) []byte {
	frame := make([]byte, dockerRawStreamHeaderBytes+len(data))
	frame[0] = stream
	binary.BigEndian.PutUint32(frame[4:8], uint32(len(data)))
	copy(frame[dockerRawStreamHeaderBytes:], data)
	return frame
}

func TestDurableJobLifecycleCapturesBoundedLogsBeforeCleanup(t *testing.T) {
	const version = "1.44"
	plan := validPlan(t)
	resource := plan.Resource()
	containerID := strings.Repeat("a", 64)
	var mu sync.Mutex
	removed := false

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.Method == http.MethodGet && r.URL.Path == "/version":
			_ = json.NewEncoder(w).Encode(map[string]any{"ApiVersion": version, "Version": "fixture"})
		case r.Method == http.MethodPost && r.URL.Path == "/v"+version+"/containers/create":
			var create dockerCreateRequest
			if err := json.NewDecoder(r.Body).Decode(&create); err != nil {
				t.Errorf("decode create: %v", err)
				w.WriteHeader(http.StatusBadRequest)
				return
			}
			if create.HostConfig.LogConfig.Type != "local" ||
				create.HostConfig.LogConfig.Config["max-size"] != "65536" ||
				create.HostConfig.LogConfig.Config["max-file"] != "2" {
				t.Errorf("log config = %#v", create.HostConfig.LogConfig)
			}
			w.WriteHeader(http.StatusCreated)
			_ = json.NewEncoder(w).Encode(map[string]any{"Id": containerID})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/start"):
			w.WriteHeader(http.StatusNoContent)
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/json"):
			mu.Lock()
			isRemoved := removed
			mu.Unlock()
			if isRemoved {
				http.NotFound(w, r)
				return
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":     containerID,
				"Config": map[string]any{"Labels": resource.labels()},
				"State":  map[string]any{"Status": "exited", "Running": false, "OOMKilled": false, "ExitCode": 0},
			})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/logs"):
			_, _ = w.Write(append(dockerLogFrame(1, "hello"), dockerLogFrame(2, "!")...))
		case r.Method == http.MethodDelete:
			mu.Lock()
			removed = true
			mu.Unlock()
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected Docker request: %s %s", r.Method, r.URL.RequestURI())
			http.NotFound(w, r)
		}
	})

	engine := engineForSocket(t, fakeDockerSocket(t, handler), func(options *EngineOptions) {
		options.OutputBytes = 5
	})
	started, err := engine.StartJob(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if !started.Created || !started.Started || started.Resource != resource ||
		started.InstanceRef != instanceReference(containerID) {
		t.Fatalf("start = %#v", started)
	}

	live, liveTruncated, err := engine.OutputJob(t.Context(), resource, started.InstanceRef)
	if err != nil || string(live) != "hello" || !liveTruncated {
		t.Fatalf("live output = %q, %v, %v", live, liveTruncated, err)
	}

	result, err := engine.ObserveJob(t.Context(), resource, started.InstanceRef)
	if err != nil {
		t.Fatal(err)
	}
	if !result.ExitCodeKnown || result.ExitCode != 0 || result.Outcome != OutcomeExited ||
		string(result.Output) != "hello" || !result.OutputTruncated || result.Cleanup != CleanupPending {
		t.Fatalf("result = %#v", result)
	}
	mu.Lock()
	removedBeforeCleanup := removed
	mu.Unlock()
	if removedBeforeCleanup {
		t.Fatal("observation removed resource before durable caller could record result")
	}

	status, err := engine.CleanupJob(t.Context(), resource, started.InstanceRef)
	if err != nil || status != CleanupComplete {
		t.Fatalf("cleanup = %s, %v", status, err)
	}
}

func TestObserveJobResumesOwnedRunningResource(t *testing.T) {
	const version = "1.44"
	resource := validPlan(t).Resource()
	containerID := strings.Repeat("b", 64)
	var mu sync.Mutex
	waited := false

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/version":
			_ = json.NewEncoder(w).Encode(map[string]any{"ApiVersion": version})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/json"):
			mu.Lock()
			isWaited := waited
			mu.Unlock()
			status, running := "running", true
			exitCode := int64(0)
			if isWaited {
				status, running, exitCode = "exited", false, 9
			}
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":     containerID,
				"Config": map[string]any{"Labels": resource.labels()},
				"State":  map[string]any{"Status": status, "Running": running, "OOMKilled": false, "ExitCode": exitCode},
			})
		case r.Method == http.MethodPost && strings.HasSuffix(r.URL.Path, "/wait"):
			mu.Lock()
			waited = true
			mu.Unlock()
			_ = json.NewEncoder(w).Encode(map[string]any{"StatusCode": 9})
		case r.Method == http.MethodGet && strings.HasSuffix(r.URL.Path, "/logs"):
			_, _ = w.Write(dockerLogFrame(1, "recovered"))
		default:
			t.Errorf("unexpected Docker request: %s %s", r.Method, r.URL.RequestURI())
			http.NotFound(w, r)
		}
	})

	result, err := engineForSocket(t, fakeDockerSocket(t, handler), nil).ObserveJob(
		t.Context(), resource, instanceReference(containerID),
	)
	if err != nil {
		t.Fatal(err)
	}
	if !result.ExitCodeKnown || result.ExitCode != 9 || result.Outcome != OutcomeExited ||
		string(result.Output) != "recovered" || result.OutputTruncated || result.Cleanup != CleanupPending {
		t.Fatalf("recovered result = %#v", result)
	}
}

func TestObserveJobDistinguishesAbsentFromBackendFailure(t *testing.T) {
	const version = "1.44"
	resource := validPlan(t).Resource()
	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/version":
			_ = json.NewEncoder(w).Encode(map[string]any{"ApiVersion": version})
		case "/v" + version + "/containers/" + resource.Name() + "/json":
			http.NotFound(w, r)
		default:
			http.NotFound(w, r)
		}
	})
	result, err := engineForSocket(t, fakeDockerSocket(t, handler), nil).ObserveJob(
		t.Context(), resource, instanceReference(strings.Repeat("c", 64)),
	)
	if err != nil {
		t.Fatal(err)
	}
	if result.Outcome != OutcomeUnknown || result.Cleanup != CleanupComplete || result.ExitCodeKnown {
		t.Fatalf("absent result = %#v", result)
	}
}

func TestExactInstanceRecoveryRejectsSameLabelReplacement(t *testing.T) {
	const version = "1.44"
	resource := validPlan(t).Resource()
	originalID := strings.Repeat("d", 64)
	replacementID := strings.Repeat("e", 64)
	mutated := false

	handler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch {
		case r.URL.Path == "/version":
			_ = json.NewEncoder(w).Encode(map[string]any{"ApiVersion": version})
		case r.Method == http.MethodGet && r.URL.Path == "/v"+version+"/containers/"+resource.Name()+"/json":
			_ = json.NewEncoder(w).Encode(map[string]any{
				"Id":     replacementID,
				"Config": map[string]any{"Labels": resource.labels()},
				"State":  map[string]any{"Status": "running", "Running": true, "OOMKilled": false, "ExitCode": 0},
			})
		case r.Method == http.MethodDelete || r.Method == http.MethodPost:
			mutated = true
			w.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected Docker request: %s %s", r.Method, r.URL.RequestURI())
			http.NotFound(w, r)
		}
	})
	engine := engineForSocket(t, fakeDockerSocket(t, handler), nil)
	instanceRef := instanceReference(originalID)

	if _, err := engine.InspectJob(t.Context(), resource, instanceRef); !errors.Is(err, ErrInstanceMismatch) {
		t.Fatalf("inspect mismatch error = %v", err)
	}
	if _, _, err := engine.OutputJob(t.Context(), resource, instanceRef); !errors.Is(err, ErrInstanceMismatch) {
		t.Fatalf("output mismatch error = %v", err)
	}
	result, err := engine.ObserveJob(t.Context(), resource, instanceRef)
	if !errors.Is(err, ErrInstanceMismatch) || result.Outcome != OutcomeUnknown || result.Cleanup != CleanupFailed {
		t.Fatalf("observe replacement = %#v, %v", result, err)
	}
	status, err := engine.CleanupJob(t.Context(), resource, instanceRef)
	if !errors.Is(err, ErrInstanceMismatch) || status != CleanupFailed {
		t.Fatalf("cleanup replacement = %s, %v", status, err)
	}
	if mutated {
		t.Fatal("same-label replacement resource was mutated")
	}
}

func TestReadDockerRawStreamRejectsMalformedFrames(t *testing.T) {
	valid := append(dockerLogFrame(1, "ab"), dockerLogFrame(2, "cd")...)
	output, truncated, err := readDockerRawStream(strings.NewReader(string(valid)), 3)
	if err != nil || string(output) != "abc" || !truncated {
		t.Fatalf("bounded stream = %q, %v, %v", output, truncated, err)
	}

	bad := dockerLogFrame(3, "bad")
	if _, _, err = readDockerRawStream(strings.NewReader(string(bad)), 32); err == nil {
		t.Fatal("invalid stream byte was accepted")
	}
	if _, _, err = readDockerRawStream(strings.NewReader(string(valid[:len(valid)-1])), 32); err == nil {
		t.Fatal("truncated frame was accepted")
	}
}
