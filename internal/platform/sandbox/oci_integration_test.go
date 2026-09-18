package sandbox

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestRealOCIJobLifecycle(t *testing.T) {
	if os.Getenv("LOKI_REQUIRE_OCI_JOB_TESTS") != "1" {
		t.Skip("set LOKI_REQUIRE_OCI_JOB_TESTS=1 with explicit Docker socket, pinned image, and shared workspace fixtures")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("real OCI job lifecycle requires Linux")
	}
	socket := requiredOCIEnv(t, "LOKI_TEST_DOCKER_SOCKET")
	image := requiredOCIEnv(t, "LOKI_TEST_DOCKER_IMAGE")
	workspace := requiredOCIEnv(t, "LOKI_TEST_DOCKER_WORKSPACE")
	if !filepath.IsAbs(socket) || !filepath.IsAbs(workspace) {
		t.Fatal("OCI fixture paths must be absolute")
	}
	info, err := os.Stat(workspace)
	if err != nil || !info.IsDir() {
		t.Fatalf("OCI workspace fixture must be an existing directory: %v", err)
	}

	peerUID := optionalOCIUint32(t, "LOKI_TEST_DOCKER_PEER_UID", 0)
	workloadUID := optionalOCIUint32(t, "LOKI_TEST_WORKLOAD_UID", 65534)
	workloadGID := optionalOCIUint32(t, "LOKI_TEST_WORKLOAD_GID", 65534)
	policyDigest := strings.Repeat("a", 64)
	policy, err := NewPolicy(PolicyOptions{
		GenerationSHA256: policyDigest,
		Image:            image,
		Workspace:        workspace,
		UID:              workloadUID,
		GID:              workloadGID,
		Environment:      []string{"PATH=/usr/bin:/bin"},
		MemoryBytes:      128 << 20,
		PIDs:             32,
		TmpfsBytes:       16 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	id := randomOCIJobID(t)
	plan, err := policy.Plan(WorkloadSpec{
		ID:           id,
		PolicySHA256: policyDigest,
		CWD:          ".",
		Argv:         []string{"/bin/sh", "-c", "sleep 30"},
	})
	if err != nil {
		t.Fatal(err)
	}
	engine, err := NewEngine(EngineOptions{
		Socket:              socket,
		ExpectedUID:         &peerUID,
		ControlTimeout:      10 * time.Second,
		CleanupTimeout:      10 * time.Second,
		GracefulStopTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}

	ctx, cancel := context.WithCancel(t.Context())
	type runResult struct {
		result Result
		err    error
	}
	done := make(chan runResult, 1)
	go func() {
		result, runErr := engine.Run(ctx, plan)
		done <- runResult{result: result, err: runErr}
	}()

	resource := plan.Resource()
	deadline := time.Now().Add(15 * time.Second)
	for {
		state, inspectErr := engine.Inspect(t.Context(), resource)
		if inspectErr != nil {
			cancel()
			t.Fatal(inspectErr)
		}
		if state.Exists && state.Running {
			break
		}
		select {
		case got := <-done:
			cancel()
			t.Fatalf("OCI workload exited before cancellation: result=%#v err=%v", got.result, got.err)
		default:
		}
		if time.Now().After(deadline) {
			cancel()
			t.Fatal("OCI workload did not reach running state")
		}
		time.Sleep(50 * time.Millisecond)
	}

	cancel()
	var got runResult
	select {
	case got = <-done:
	case <-time.After(20 * time.Second):
		t.Fatal("OCI workload did not terminate after cancellation")
	}
	if !errors.Is(got.err, context.Canceled) || got.result.Outcome != OutcomeCanceled || got.result.Cleanup != CleanupComplete {
		t.Fatalf("OCI cancellation result=%#v err=%v", got.result, got.err)
	}
	state, err := engine.Inspect(t.Context(), resource)
	if err != nil {
		t.Fatal(err)
	}
	if state.Exists {
		t.Fatalf("OCI resource remained after cleanup: %#v", state)
	}
}

func requiredOCIEnv(t *testing.T, name string) string {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		t.Fatalf("%s is required when LOKI_REQUIRE_OCI_JOB_TESTS=1", name)
	}
	return value
}

func optionalOCIUint32(t *testing.T, name string, fallback uint32) uint32 {
	t.Helper()
	value := os.Getenv(name)
	if value == "" {
		return fallback
	}
	parsed, err := strconv.ParseUint(value, 10, 32)
	if err != nil {
		t.Fatalf("%s must be an unsigned 32-bit integer", name)
	}
	return uint32(parsed)
}

func randomOCIJobID(t *testing.T) string {
	t.Helper()
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		t.Fatal(err)
	}
	return hex.EncodeToString(raw[:])
}
