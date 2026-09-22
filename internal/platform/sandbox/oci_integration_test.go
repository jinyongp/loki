package sandbox

import (
	"bufio"
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"testing"
	"time"

	"loki/internal/integrations/sharing/previews"
	mcptransport "loki/internal/transport/mcp"
	"loki/internal/work/jobs"
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
		Gateway: GatewayPolicyOptions{
			Image: image, Binary: "/opt/loki/bin/loki",
			ExecutionContract: "/usr/share/doc/loki/execution-contract.json",
			EgressPolicy:      "/usr/share/doc/loki/egress-policy.json", ProxyPort: 18766,
			MemoryBytes: 64 << 20, PIDs: 16, TmpfsBytes: 16 << 20,
		},
		Workspace:   workspace,
		UID:         workloadUID,
		GID:         workloadGID,
		Environment: []string{"PATH=/usr/bin:/bin"},
		MemoryBytes: 128 << 20,
		PIDs:        32,
		TmpfsBytes:  16 << 20,
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

func TestRealOCIJobRecoveryAndBoundedOutput(t *testing.T) {
	if os.Getenv("LOKI_REQUIRE_OCI_JOB_TESTS") != "1" {
		t.Skip("set LOKI_REQUIRE_OCI_JOB_TESTS=1 with explicit Docker socket, pinned image, and shared workspace fixtures")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("real OCI job recovery requires Linux")
	}
	socket := requiredOCIEnv(t, "LOKI_TEST_DOCKER_SOCKET")
	image := requiredOCIEnv(t, "LOKI_TEST_DOCKER_IMAGE")
	workspace := requiredOCIEnv(t, "LOKI_TEST_DOCKER_WORKSPACE")
	peerUID := optionalOCIUint32(t, "LOKI_TEST_DOCKER_PEER_UID", 0)
	workloadUID := optionalOCIUint32(t, "LOKI_TEST_WORKLOAD_UID", 65534)
	workloadGID := optionalOCIUint32(t, "LOKI_TEST_WORKLOAD_GID", 65534)
	policyDigest := strings.Repeat("b", 64)
	policy, err := NewPolicy(PolicyOptions{
		GenerationSHA256: policyDigest,
		Image:            image,
		Gateway: GatewayPolicyOptions{
			Image: image, Binary: "/opt/loki/bin/loki",
			ExecutionContract: "/usr/share/doc/loki/execution-contract.json",
			EgressPolicy:      "/usr/share/doc/loki/egress-policy.json", ProxyPort: 18766,
			MemoryBytes: 64 << 20, PIDs: 16, TmpfsBytes: 16 << 20,
		},
		Workspace:   workspace,
		UID:         workloadUID,
		GID:         workloadGID,
		Environment: []string{"PATH=/usr/bin:/bin"},
		MemoryBytes: 128 << 20,
		PIDs:        32,
		TmpfsBytes:  16 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}
	plan, err := policy.Plan(WorkloadSpec{
		ID:           randomOCIJobID(t),
		PolicySHA256: policyDigest,
		CWD:          ".",
		Argv:         []string{"/bin/sh", "-c", "printf recovered-output; sleep 1; exit 7"},
	})
	if err != nil {
		t.Fatal(err)
	}
	first, err := NewEngine(EngineOptions{
		Socket:              socket,
		ExpectedUID:         &peerUID,
		OutputBytes:         64,
		ControlTimeout:      10 * time.Second,
		CleanupTimeout:      10 * time.Second,
		GracefulStopTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	started, err := first.StartJob(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if !started.Created || !started.Started || started.InstanceRef == "" {
		t.Fatalf("start result = %#v", started)
	}

	recovered, err := NewEngine(EngineOptions{
		Socket:              socket,
		ExpectedUID:         &peerUID,
		OutputBytes:         64,
		ControlTimeout:      10 * time.Second,
		CleanupTimeout:      10 * time.Second,
		GracefulStopTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	result, err := recovered.ObserveJob(t.Context(), plan.Resource(), started.InstanceRef)
	if err != nil {
		t.Fatal(err)
	}
	if !result.ExitCodeKnown || result.ExitCode != 7 || result.Outcome != OutcomeExited ||
		string(result.Output) != "recovered-output" || result.OutputTruncated || result.Cleanup != CleanupPending {
		t.Fatalf("recovered result = %#v", result)
	}
	cleanup, err := recovered.CleanupJob(t.Context(), plan.Resource(), started.InstanceRef)
	if err != nil || cleanup != CleanupComplete {
		t.Fatalf("cleanup = %s, %v", cleanup, err)
	}
	state, err := recovered.Inspect(t.Context(), plan.Resource())
	if err != nil {
		t.Fatal(err)
	}
	if state.Exists {
		t.Fatalf("OCI resource remained after recovered cleanup: %#v", state)
	}
}

func TestRealOCIJobNetworkEndpointPreview(t *testing.T) {
	if os.Getenv("LOKI_REQUIRE_OCI_JOB_TESTS") != "1" {
		t.Skip("set LOKI_REQUIRE_OCI_JOB_TESTS=1 with explicit Docker socket, pinned Loki image, shared workspace, and allowlisted HTTPS authority fixtures")
	}
	if runtime.GOOS != "linux" {
		t.Fatal("real OCI job network acceptance requires Linux")
	}
	socket := requiredOCIEnv(t, "LOKI_TEST_DOCKER_SOCKET")
	image := requiredOCIEnv(t, "LOKI_TEST_DOCKER_IMAGE")
	workspace := requiredOCIEnv(t, "LOKI_TEST_DOCKER_WORKSPACE")
	allowedAuthority := requiredOCIEnv(t, "LOKI_TEST_EGRESS_ALLOWED_AUTHORITY")
	if !filepath.IsAbs(socket) || !filepath.IsAbs(workspace) {
		t.Fatal("OCI fixture paths must be absolute")
	}
	host, port, err := net.SplitHostPort(allowedAuthority)
	if err != nil || host == "" || port != "443" {
		t.Fatal("LOKI_TEST_EGRESS_ALLOWED_AUTHORITY must be an allowlisted host:443 authority")
	}
	info, err := os.Stat(workspace)
	if err != nil || !info.IsDir() {
		t.Fatalf("OCI workspace fixture must be an existing directory: %v", err)
	}

	peerUID := optionalOCIUint32(t, "LOKI_TEST_DOCKER_PEER_UID", 0)
	workloadUID := optionalOCIUint32(t, "LOKI_TEST_WORKLOAD_UID", 65534)
	workloadGID := optionalOCIUint32(t, "LOKI_TEST_WORKLOAD_GID", 65534)
	policyDigest := strings.Repeat("c", 64)
	policy, err := NewPolicy(PolicyOptions{
		GenerationSHA256: policyDigest,
		Image:            image,
		Gateway: GatewayPolicyOptions{
			Image: image, Binary: "/opt/loki/bin/loki",
			ExecutionContract: "/usr/share/doc/loki/execution-contract.json",
			EgressPolicy:      "/usr/share/doc/loki/egress-policy.json", ProxyPort: 18766,
			MemoryBytes: 64 << 20, PIDs: 32, TmpfsBytes: 16 << 20,
		},
		Workspace:   workspace,
		UID:         workloadUID,
		GID:         workloadGID,
		Environment: []string{"PATH=/usr/bin:/bin"},
		MemoryBytes: 128 << 20,
		PIDs:        64,
		TmpfsBytes:  16 << 20,
	})
	if err != nil {
		t.Fatal(err)
	}

	id := randomOCIJobID(t)
	fixtureBinary := buildOCIFixtureServer(t, workspace, id)
	endpoint := EndpointSpec{Name: "web", Port: 18080}
	plan, err := policy.Plan(WorkloadSpec{
		ID:           id,
		PolicySHA256: policyDigest,
		CWD:          ".",
		Argv: []string{
			fixtureBinary,
			"--port", strconv.Itoa(endpoint.Port),
			"--allowed-authority", allowedAuthority,
		},
		Network:   NetworkDependencyInstall,
		Endpoints: []EndpointSpec{endpoint},
	})
	if err != nil {
		t.Fatal(err)
	}
	engine := realOCIEngine(t, socket, peerUID)
	started, err := engine.StartJob(t.Context(), plan)
	if err != nil {
		t.Fatal(err)
	}
	if !started.Created || !started.Started || started.InstanceRef == "" || len(started.EndpointBindings) != 1 {
		t.Fatalf("network start result = %#v", started)
	}
	binding := started.EndpointBindings[0]
	if binding.Name != endpoint.Name || binding.Port != endpoint.Port || binding.HostPort < 1024 || binding.HostPort > 65535 {
		t.Fatalf("endpoint binding = %#v", binding)
	}
	cleaned := false
	t.Cleanup(func() {
		if cleaned {
			return
		}
		_, _ = engine.CleanupJob(context.Background(), plan.Resource(), started.InstanceRef)
	})

	recovered := realOCIEngine(t, socket, peerUID)
	state, err := recovered.InspectJob(t.Context(), plan.Resource(), started.InstanceRef)
	if err != nil {
		t.Fatal(err)
	}
	if !state.Exists || !state.Running {
		t.Fatalf("recovered domain state = %#v", state)
	}
	recoveredBindings, err := recovered.EndpointBindings(
		t.Context(), plan.Resource(), started.InstanceRef, []EndpointSpec{endpoint},
	)
	if err != nil {
		t.Fatal(err)
	}
	if len(recoveredBindings) != 1 || recoveredBindings[0] != binding {
		t.Fatalf("recovered endpoint bindings = %#v, want %#v", recoveredBindings, binding)
	}

	output := waitForOCIFixtureOutput(t, recovered, plan.Resource(), started.InstanceRef, []string{
		"egress-auth=ok",
		"egress-denied=ok",
		"egress-allowed=ok",
		"egress-direct-denied=ok",
		"ready=1",
	})
	t.Logf("OCI network fixture output: %q", output)

	now := time.Now().UTC().Format(time.RFC3339Nano)
	controller := &realOCIJobController{status: jobs.Status{
		JobID: id, State: jobs.StateRunning, Network: jobs.NetworkDependencyInstall,
		CreatedAt: now, UpdatedAt: now, DeadlineAt: time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano),
		Endpoints: []jobs.EndpointLease{{
			ID: strings.Repeat("c", 32), JobID: id, Name: endpoint.Name,
			Port: endpoint.Port, HostPort: binding.HostPort, State: jobs.EndpointLeaseActive,
			CreatedAt: now, UpdatedAt: now,
		}},
	}}
	store := previews.New("preview.test", 0, nil)
	previewController := &mcptransport.PreviewController{Store: store, Jobs: controller}
	handlers := mcptransport.PreviewHandlers(previewController, nil)
	requestID := "70000000-0000-4000-8000-000000000099"
	published, err := handlers["preview_publish"](t.Context(), map[string]any{
		"action": "job", "request_id": requestID, "job_id": id, "endpoint": endpoint.Name,
	})
	if err != nil {
		t.Fatal(err)
	}
	value, ok := published.StructuredContent.(map[string]any)
	if !ok {
		t.Fatalf("preview result = %#v", published.StructuredContent)
	}
	publicURL, ok := value["url"].(string)
	if !ok || !strings.HasPrefix(publicURL, "https://") {
		t.Fatalf("preview URL = %#v", value["url"])
	}
	publicHost := strings.TrimPrefix(publicURL, "https://")

	proxy := previews.NewProxy(store, previewController.RouteAllowed)
	defer proxy.Close()
	server := httptest.NewServer(proxy)
	defer server.Close()

	req, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	req.Host = publicHost
	response, err := server.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	body, readErr := io.ReadAll(response.Body)
	_ = response.Body.Close()
	if readErr != nil {
		t.Fatal(readErr)
	}
	if response.StatusCode != http.StatusOK || string(body) != "loki-oci-preview-ok" {
		t.Fatalf("preview HTTP status=%d body=%q", response.StatusCode, body)
	}
	assertPreviewWebSocket(t, server.Listener.Addr().String(), publicHost)

	controller.replaceLease(strings.Repeat("d", 32))
	staleRequest, err := http.NewRequestWithContext(t.Context(), http.MethodGet, server.URL+"/", nil)
	if err != nil {
		t.Fatal(err)
	}
	staleRequest.Host = publicHost
	staleResponse, err := server.Client().Do(staleRequest)
	if err != nil {
		t.Fatal(err)
	}
	_, _ = io.Copy(io.Discard, staleResponse.Body)
	_ = staleResponse.Body.Close()
	if staleResponse.StatusCode != http.StatusGone {
		t.Fatalf("stale preview status = %d", staleResponse.StatusCode)
	}
	if _, err = handlers["preview_publish"](t.Context(), map[string]any{
		"action": "job", "request_id": requestID, "job_id": id, "endpoint": endpoint.Name,
	}); err == nil {
		t.Fatal("same request_id replayed after endpoint lease replacement")
	}

	cleanup, err := recovered.CleanupJob(t.Context(), plan.Resource(), started.InstanceRef)
	if err != nil || cleanup != CleanupComplete {
		t.Fatalf("network fixture cleanup = %s, %v", cleanup, err)
	}
	cleaned = true
	state, err = recovered.InspectJob(t.Context(), plan.Resource(), started.InstanceRef)
	if err != nil {
		t.Fatal(err)
	}
	if state.Exists {
		t.Fatalf("network fixture resource remained after cleanup: %#v", state)
	}
}

type realOCIJobController struct {
	mu     sync.RWMutex
	status jobs.Status
}

func (c *realOCIJobController) Start(context.Context, jobs.StartRequest) (jobs.StartResult, error) {
	return jobs.StartResult{}, errors.New("fixture controller does not start jobs")
}

func (c *realOCIJobController) Inspect(_ context.Context, id string) (jobs.Status, error) {
	c.mu.RLock()
	defer c.mu.RUnlock()
	if id != c.status.JobID {
		return jobs.Status{}, errors.New("fixture job is not registered")
	}
	status := c.status
	status.Endpoints = append([]jobs.EndpointLease(nil), c.status.Endpoints...)
	return status, nil
}

func (c *realOCIJobController) Output(context.Context, string) (jobs.OutputSnapshot, error) {
	return jobs.OutputSnapshot{}, errors.New("fixture controller does not expose output")
}

func (c *realOCIJobController) Cancel(context.Context, string) (jobs.CancelResult, error) {
	return jobs.CancelResult{}, errors.New("fixture controller does not cancel jobs")
}

func (c *realOCIJobController) replaceLease(id string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.status.Endpoints[0].ID = id
	c.status.Endpoints[0].UpdatedAt = time.Now().UTC().Format(time.RFC3339Nano)
}

func realOCIEngine(t *testing.T, socket string, peerUID uint32) *Engine {
	t.Helper()
	engine, err := NewEngine(EngineOptions{
		Socket:              socket,
		ExpectedUID:         &peerUID,
		OutputBytes:         64 << 10,
		ControlTimeout:      10 * time.Second,
		CleanupTimeout:      10 * time.Second,
		GracefulStopTimeout: time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	return engine
}

func buildOCIFixtureServer(t *testing.T, workspace, id string) string {
	t.Helper()
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	name := ".loki-oci-fixture-" + id
	target := filepath.Join(workspace, name)
	command := exec.Command(
		"go", "build", "-trimpath", "-o", target,
		"./internal/platform/sandbox/testdata/oci-fixture-server",
	)
	command.Dir = root
	command.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH="+runtime.GOARCH)
	if output, buildErr := command.CombinedOutput(); buildErr != nil {
		t.Fatalf("build OCI fixture server: %v\n%s", buildErr, output)
	}
	if err = os.Chmod(target, 0755); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.Remove(target) })
	return "/workspace/" + name
}

func waitForOCIFixtureOutput(
	t *testing.T, engine *Engine, resource Resource, instanceRef string, markers []string,
) string {
	t.Helper()
	deadline := time.Now().Add(30 * time.Second)
	last := ""
	for {
		output, _, err := engine.OutputJob(t.Context(), resource, instanceRef)
		if err == nil {
			last = string(output)
			complete := true
			for _, marker := range markers {
				if !strings.Contains(last, marker) {
					complete = false
					break
				}
			}
			if complete {
				return last
			}
		}
		state, inspectErr := engine.InspectJob(t.Context(), resource, instanceRef)
		if inspectErr != nil {
			t.Fatalf("inspect OCI network fixture: %v; output=%q", inspectErr, last)
		}
		if state.Terminal {
			t.Fatalf("OCI network fixture exited before readiness: state=%#v output=%q", state, last)
		}
		if time.Now().After(deadline) {
			t.Fatalf("OCI network fixture did not become ready; output=%q last_error=%v", last, err)
		}
		time.Sleep(100 * time.Millisecond)
	}
}

func assertPreviewWebSocket(t *testing.T, address, publicHost string) {
	t.Helper()
	conn, err := net.DialTimeout("tcp", address, 5*time.Second)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	_ = conn.SetDeadline(time.Now().Add(10 * time.Second))
	key := "dGhlIHNhbXBsZSBub25jZQ=="
	if _, err = fmt.Fprintf(
		conn,
		"GET /ws HTTP/1.1\r\nHost: %s\r\nConnection: Upgrade\r\nUpgrade: websocket\r\nSec-WebSocket-Version: 13\r\nSec-WebSocket-Key: %s\r\n\r\n",
		publicHost, key,
	); err != nil {
		t.Fatal(err)
	}
	reader := bufio.NewReader(conn)
	status, err := reader.ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(status, " 101 ") {
		t.Fatalf("websocket status = %q", strings.TrimSpace(status))
	}
	for {
		line, readErr := reader.ReadString('\n')
		if readErr != nil {
			t.Fatal(readErr)
		}
		if line == "\r\n" {
			break
		}
	}

	payload := []byte("preview-websocket")
	mask := [4]byte{1, 2, 3, 4}
	frame := []byte{0x81, 0x80 | byte(len(payload)), mask[0], mask[1], mask[2], mask[3]}
	for index, value := range payload {
		frame = append(frame, value^mask[index%len(mask)])
	}
	if _, err = conn.Write(frame); err != nil {
		t.Fatal(err)
	}

	var header [2]byte
	if _, err = io.ReadFull(reader, header[:]); err != nil {
		t.Fatal(err)
	}
	if header[0] != 0x81 || header[1]&0x80 != 0 || int(header[1]&0x7f) != len(payload) {
		t.Fatalf("websocket response header = %#v", header)
	}
	echo := make([]byte, len(payload))
	if _, err = io.ReadFull(reader, echo); err != nil {
		t.Fatal(err)
	}
	if string(echo) != string(payload) {
		t.Fatalf("websocket echo = %q", echo)
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
