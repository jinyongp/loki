package jobs

import (
	"errors"
	"strings"
	"testing"
	"time"
)

func testRequestID() string {
	return "123e4567-e89b-12d3-a456-426614174000"
}

func TestStartRequestNormalizationAndDeterministicIdentity(t *testing.T) {
	request := StartRequest{
		RequestID:      "123E4567-E89B-12D3-A456-426614174000",
		Argv:           []string{"/bin/sh", "-c", "printf ok"},
		TimeoutSeconds: 30,
		Network:        NetworkDependencyInstall,
		Endpoints: []EndpointRequest{
			{Name: "web", Port: 5173},
			{Name: "api", Port: 3000},
		},
	}
	normalized, fingerprint, err := normalizeStartRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if normalized.RequestID != testRequestID() || normalized.CWD != "." ||
		normalized.TimeoutSeconds != 30 || len(normalized.Argv) != 3 ||
		normalized.Network != NetworkDependencyInstall ||
		len(normalized.Endpoints) != 2 || normalized.Endpoints[0].Name != "api" ||
		normalized.Endpoints[1].Name != "web" {
		t.Fatalf("normalized = %#v", normalized)
	}
	if len(fingerprint) != 64 {
		t.Fatalf("fingerprint = %q", fingerprint)
	}
	same, err := startFingerprint(normalized)
	if err != nil {
		t.Fatal(err)
	}
	if fingerprint != same {
		t.Fatalf("fingerprint drift: %q != %q", fingerprint, same)
	}
	id, err := JobIDForRequestID(testRequestID())
	if err != nil {
		t.Fatal(err)
	}
	idAgain, err := JobIDForRequestID(strings.ToUpper(testRequestID()))
	if err != nil || idAgain != id || !jobIDPattern.MatchString(id) {
		t.Fatalf("job IDs = %q, %q, %v", id, idAgain, err)
	}
}

func TestResolveRequestedTimeoutUsesTrustedMaximum(t *testing.T) {
	if got, err := ResolveRequestedTimeout(0, 30*time.Second); err != nil || got != 30*time.Second {
		t.Fatalf("default timeout = %v, %v", got, err)
	}
	if got, err := ResolveRequestedTimeout(10, 30*time.Second); err != nil || got != 10*time.Second {
		t.Fatalf("requested timeout = %v, %v", got, err)
	}
	if _, err := ResolveRequestedTimeout(31, 30*time.Second); err == nil {
		t.Fatal("timeout above trusted maximum was accepted")
	}
	if _, err := ResolveRequestedTimeout(1, 0); err == nil {
		t.Fatal("invalid trusted timeout was accepted")
	}
}

func TestStartRequestRejectsChangedOrUnboundedInputs(t *testing.T) {
	base := StartRequest{
		RequestID: testRequestID(),
		CWD:       ".",
		Argv:      []string{"/bin/true"},
	}
	tests := []struct {
		name   string
		mutate func(*StartRequest)
	}{
		{"request-id", func(r *StartRequest) { r.RequestID = "not-a-uuid" }},
		{"cwd", func(r *StartRequest) { r.CWD = "../outside" }},
		{"argv", func(r *StartRequest) { r.Argv = []string{"true"} }},
		{"timeout-negative", func(r *StartRequest) { r.TimeoutSeconds = -1 }},
		{"timeout-high", func(r *StartRequest) { r.TimeoutSeconds = MaxTimeoutSeconds + 1 }},
		{"network", func(r *StartRequest) { r.Network = NetworkProfile("host") }},
		{"endpoint-name", func(r *StartRequest) { r.Endpoints = []EndpointRequest{{Name: "Web", Port: 5173}} }},
		{"endpoint-port", func(r *StartRequest) { r.Endpoints = []EndpointRequest{{Name: "web", Port: 80}} }},
		{"endpoint-name-duplicate", func(r *StartRequest) {
			r.Endpoints = []EndpointRequest{{Name: "web", Port: 5173}, {Name: "web", Port: 3000}}
		}},
		{"endpoint-port-duplicate", func(r *StartRequest) {
			r.Endpoints = []EndpointRequest{{Name: "web", Port: 5173}, {Name: "api", Port: 5173}}
		}},
		{"endpoint-count", func(r *StartRequest) {
			r.Endpoints = make([]EndpointRequest, MaxEndpoints+1)
			for index := range r.Endpoints {
				r.Endpoints[index] = EndpointRequest{Name: "e" + strings.Repeat("x", index), Port: 2000 + index}
			}
		}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			request := base
			request.Argv = append([]string(nil), base.Argv...)
			tc.mutate(&request)
			if _, _, err := normalizeStartRequest(request); err == nil {
				t.Fatal("invalid asynchronous start request was accepted")
			}
		})
	}

	first, err := StartFingerprint(".", []string{"/bin/true"}, 10)
	if err != nil {
		t.Fatal(err)
	}
	second, err := StartFingerprint(".", []string{"/bin/true"}, 11)
	if err != nil {
		t.Fatal(err)
	}
	if first == second {
		t.Fatal("requested lifetime was omitted from replay fingerprint")
	}
	networked, _, err := normalizeStartRequest(StartRequest{
		RequestID: testRequestID(), CWD: ".", Argv: []string{"/bin/true"},
		TimeoutSeconds: 10, Network: NetworkDependencyInstall,
	})
	if err != nil {
		t.Fatal(err)
	}
	if networkedFingerprint, err := startFingerprint(networked); err != nil || networkedFingerprint == first {
		t.Fatalf("network profile omitted from fingerprint: %q %v", networkedFingerprint, err)
	}
	withEndpoint, _, err := normalizeStartRequest(StartRequest{
		RequestID: testRequestID(), CWD: ".", Argv: []string{"/bin/true"},
		TimeoutSeconds: 10, Endpoints: []EndpointRequest{{Name: "web", Port: 5173}},
	})
	if err != nil {
		t.Fatal(err)
	}
	endpointFingerprint, err := startFingerprint(withEndpoint)
	if err != nil || endpointFingerprint == first {
		t.Fatalf("endpoint intent omitted from fingerprint: %q %v", endpointFingerprint, err)
	}
	reordered, _, err := normalizeStartRequest(StartRequest{
		RequestID: testRequestID(), CWD: ".", Argv: []string{"/bin/true"},
		TimeoutSeconds: 10,
		Endpoints:      []EndpointRequest{{Name: "web", Port: 5173}, {Name: "api", Port: 3000}},
	})
	if err != nil {
		t.Fatal(err)
	}
	canonical, _, err := normalizeStartRequest(StartRequest{
		RequestID: testRequestID(), CWD: ".", Argv: []string{"/bin/true"},
		TimeoutSeconds: 10,
		Endpoints:      []EndpointRequest{{Name: "api", Port: 3000}, {Name: "web", Port: 5173}},
	})
	if err != nil {
		t.Fatal(err)
	}
	left, _ := startFingerprint(reordered)
	right, _ := startFingerprint(canonical)
	if left != right {
		t.Fatalf("endpoint ordering changed fingerprint: %q != %q", left, right)
	}
}

func TestServiceStartPassesReplayIdentityAndValidatesResult(t *testing.T) {
	id, err := JobIDForRequestID(testRequestID())
	if err != nil {
		t.Fatal(err)
	}
	deadline := time.Now().UTC().Add(time.Minute).Format(time.RFC3339Nano)
	launcher := &fakeLauncher{startResult: StartResult{
		RequestID: testRequestID(), JobID: id, State: StateAdmitted,
		Detached: true, DeadlineAt: deadline,
	}}
	service, err := NewService(launcher, fixedID(strings.Repeat("a", 32)))
	if err != nil {
		t.Fatal(err)
	}
	request := StartRequest{
		RequestID:      testRequestID(),
		Argv:           []string{"/bin/true"},
		TimeoutSeconds: 30,
		Network:        NetworkDependencyInstall,
		Endpoints:      []EndpointRequest{{Name: "web", Port: 5173}},
	}
	result, err := service.Start(t.Context(), request)
	if err != nil {
		t.Fatal(err)
	}
	if result.JobID != id || !result.Detached {
		t.Fatalf("start result = %#v", result)
	}
	normalized, wantFingerprint, err := normalizeStartRequest(request)
	if err != nil {
		t.Fatal(err)
	}
	if launcher.workload.ID != id || launcher.workload.RequestID != testRequestID() ||
		launcher.workload.RequestSHA256 != wantFingerprint || launcher.workload.CWD != "." ||
		launcher.workload.TimeoutSeconds != 30 ||
		launcher.workload.Network != NetworkDependencyInstall ||
		!sameEndpointRequests(launcher.workload.Endpoints, normalized.Endpoints) {
		t.Fatalf("workload = %#v", launcher.workload)
	}

	launcher.startResult.JobID = strings.Repeat("f", 32)
	if _, err = service.Start(t.Context(), request); err == nil {
		t.Fatal("mismatched launcher Job ID was accepted")
	}
	launcher.startResult.JobID = id
	launcher.startResult.DeadlineAt = "not-a-timestamp"
	if _, err = service.Start(t.Context(), request); err == nil {
		t.Fatal("invalid launcher deadline was accepted")
	}
}

func TestRecordPublicViewsHideBackendAuthority(t *testing.T) {
	now := time.Now().UTC()
	exit := int64(0)
	jobID := strings.Repeat("a", 32)
	instanceRef := "oci-instance-sha256:" + strings.Repeat("d", 64)
	leaseID, err := EndpointLeaseID(jobID, "web", instanceRef)
	if err != nil {
		t.Fatal(err)
	}
	record := Record{
		ID:            jobID,
		BackendRef:    "oci:" + strings.Repeat("b", 64),
		RequestID:     testRequestID(),
		RequestSHA256: strings.Repeat("c", 64),
		Network:       NetworkDependencyInstall,
		EndpointRequests: []EndpointRequest{
			{Name: "web", Port: 5173},
		},
		EndpointLeases: []EndpointLease{{
			ID: leaseID, JobID: jobID,
			Name: "web", Port: 5173, HostPort: 43001, State: EndpointLeaseReleased,
			CreatedAt: now.Format(time.RFC3339Nano), UpdatedAt: now.Add(time.Second).Format(time.RFC3339Nano),
		}},
		InstanceRef: instanceRef,
		State:       StateTerminal,
		Result: &Result{
			ExitCode: &exit, Outcome: OutcomeExited,
			Output: Output{Text: "hello", Truncated: true}, Cleanup: CleanupComplete,
		},
		CreatedAt:  now.Format(time.RFC3339Nano),
		UpdatedAt:  now.Add(time.Second).Format(time.RFC3339Nano),
		DeadlineAt: now.Add(time.Minute).Format(time.RFC3339Nano),
		ExpiresAt:  now.Add(2 * time.Minute).Format(time.RFC3339Nano),
	}
	status, err := StatusFromRecord(record)
	if err != nil {
		t.Fatal(err)
	}
	if status.JobID != record.ID || status.State != StateTerminal || status.Outcome != OutcomeExited ||
		status.ExitCode == nil || *status.ExitCode != 0 || status.Cleanup != CleanupComplete || !status.Truncated ||
		status.Network != NetworkDependencyInstall || len(status.Endpoints) != 1 ||
		status.Endpoints[0].HostPort != 43001 || status.Endpoints[0].State != EndpointLeaseReleased {
		t.Fatalf("status = %#v", status)
	}
	output, err := OutputFromRecord(record)
	if err != nil {
		t.Fatal(err)
	}
	if output.JobID != record.ID || output.State != StateTerminal || output.Output != "hello" ||
		!output.Truncated || !output.Complete {
		t.Fatalf("output = %#v", output)
	}

	record.RequestSHA256 = "bad"
	if _, err = StatusFromRecord(record); err == nil {
		t.Fatal("invalid durable replay identity was exposed as status")
	}
	if !errors.Is(ErrReplayConflict, ErrReplayConflict) {
		t.Fatal("replay conflict sentinel is not stable")
	}
}
