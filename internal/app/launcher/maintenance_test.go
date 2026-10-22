package launcher

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"
	"time"

	"loki/modules/execution/jobs"
)

func TestMaintenanceOperationStrictWireContract(t *testing.T) {
	l := lifecycleFixture(t, &fakeRunner{}, time.Second, time.Minute, 8)
	ops := l.operations()
	for _, raw := range []string{
		`{"operation":"maintenance_begin","operation_id":"11111111-1111-4111-8111-111111111111","interrupt":null}`,
		`{"operation":"maintenance_begin","operation_id":"11111111-1111-4111-8111-111111111111","interrupt":false,"interrupt":true}`,
		`{"operation":"maintenance_begin","operation_id":"11111111-1111-4111-8111-111111111111","root":"/tmp"}`,
	} {
		if _, err := ops["maintenance_begin"].Handle(t.Context(), json.RawMessage(raw)); err == nil {
			t.Fatal("invalid request accepted", raw)
		}
	}
	if _, err := ops["maintenance_begin"].Handle(t.Context(), json.RawMessage(`{"operation":"maintenance_begin","operation_id":"11111111-1111-4111-8111-111111111111","interrupt":false}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := ops["maintenance_end"].Handle(t.Context(), json.RawMessage(`{"operation":"maintenance_end","operation_id":"11111111-1111-4111-8111-111111111111"}`)); err != nil {
		t.Fatal(err)
	}
}

type blockedMaintenanceResolver struct {
	entered chan struct{}
	release chan struct{}
}

func (r *blockedMaintenanceResolver) Resolve(ctx context.Context, _ string, _ []jobs.ToolchainRef) (ResolvedToolchains, error) {
	close(r.entered)
	select {
	case <-r.release:
	case <-ctx.Done():
		return ResolvedToolchains{}, ctx.Err()
	}
	return ResolvedToolchains{}, errors.New("fixture resolution failure")
}
func (*blockedMaintenanceResolver) Cleanup(string, []jobs.ToolchainRef) error { return nil }

func TestMaintenanceWaitsForPreAdmissionAndBlocksLaterResolution(t *testing.T) {
	l := lifecycleFixture(t, &fakeRunner{}, time.Second, time.Minute, 8)
	resolver := &blockedMaintenanceResolver{entered: make(chan struct{}), release: make(chan struct{})}
	l.toolchains = resolver
	request, fp, err := jobs.NormalizeStartRequest(jobs.StartRequest{RequestID: "123e4567-e89b-12d3-a456-426614174399", CWD: ".", Argv: []string{"/bin/true"}, Toolchains: []jobs.ToolchainRef{{Family: "node", Version: "26.9.0", GenerationID: strings.Repeat("d", 64)}}})
	if err != nil {
		t.Fatal(err)
	}
	id, err := jobs.JobIDForRequestID(request.RequestID)
	if err != nil {
		t.Fatal(err)
	}
	workload := jobs.Workload{ID: id, RequestID: request.RequestID, RequestSHA256: fp, CWD: request.CWD, Argv: request.Argv, Toolchains: request.Toolchains, Network: request.Network}
	start := make(chan error, 1)
	go func() { _, err := l.startWorkload(t.Context(), workload, strings.Repeat("a", 64)); start <- err }()
	select {
	case <-resolver.entered:
	case <-time.After(time.Second):
		t.Fatal("pre-admission did not start")
	}
	defer func() {
		select {
		case <-resolver.release:
		default:
			close(resolver.release)
		}
	}()
	maintenance := make(chan error, 1)
	go func() {
		_, err := l.beginMaintenance(t.Context(), maintenanceRequest{OperationID: "11111111-1111-4111-8111-111111111111"})
		maintenance <- err
	}()
	select {
	case err := <-maintenance:
		t.Fatal("maintenance overtook pre-admission", err)
	case <-time.After(20 * time.Millisecond):
	}
	close(resolver.release)
	if err := <-start; err == nil {
		t.Fatal("fixture unexpectedly admitted")
	}
	select {
	case err := <-maintenance:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(time.Second):
		t.Fatal("maintenance did not finish")
	}
	// Calling the resolver again would close its entered channel twice. The
	// admission fence must reject this request before reaching that boundary.
	if _, err := l.startWorkload(t.Context(), workload, strings.Repeat("a", 64)); !errors.Is(err, jobs.ErrMaintenanceBusy) {
		t.Fatal(err)
	}
}

func TestMaintenanceDefaultBusyAndExplicitDrain(t *testing.T) {
	runner := &fakeRunner{blockObserve: true, observing: make(chan struct{}), canceled: make(chan struct{})}
	l := lifecycleFixture(t, runner, 5*time.Second, time.Minute, 8)
	workload := jobs.Workload{ID: strings.Repeat("a", 32), CWD: ".", Argv: []string{"/bin/true"}}
	if _, err := l.startWorkload(t.Context(), workload, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.observing:
	case <-time.After(time.Second):
		t.Fatal("Job did not start")
	}
	op := "11111111-1111-4111-8111-111111111111"
	if _, err := l.beginMaintenance(t.Context(), maintenanceRequest{OperationID: op}); !errors.Is(err, jobs.ErrJobsBusy) {
		t.Fatalf("default: %v", err)
	}
	if err := l.journal.AdmissionAllowed(); err != nil {
		t.Fatal(err)
	}
	if state, err := l.beginMaintenance(t.Context(), maintenanceRequest{OperationID: op, Interrupt: true}); err != nil || !state.Active {
		t.Fatalf("drain: %+v %v", state, err)
	}
	status, err := l.inspect(workload.ID)
	if err != nil || status.State != jobs.StateTerminal || status.Cleanup != jobs.CleanupComplete {
		t.Fatalf("cleanup: %+v %v", status, err)
	}
	workload.ID = strings.Repeat("c", 32)
	if _, err := l.startWorkload(t.Context(), workload, strings.Repeat("a", 64)); !errors.Is(err, jobs.ErrMaintenanceBusy) {
		t.Fatal("new admission escaped", err)
	}
	starts, _, _, _ := runner.counts()
	if starts != 1 {
		t.Fatalf("fenced Job reached OCI: %d", starts)
	}
	if _, err := l.journal.EndMaintenance(op); err != nil {
		t.Fatal(err)
	}
}

func TestMaintenanceCanceledWriterEventuallyReleases(t *testing.T) {
	l := lifecycleFixture(t, &fakeRunner{}, time.Second, time.Minute, 8)
	l.admission.RLock()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := l.lockAdmission(ctx); !errors.Is(err, context.Canceled) {
		t.Fatal(err)
	}
	l.admission.RUnlock()
	done := make(chan struct{})
	go func() { l.admission.Lock(); l.admission.Unlock(); close(done) }()
	select {
	case <-done:
	case <-time.After(time.Second):
		t.Fatal("canceled writer kept admission blocked")
	}
}

func TestCompletedDefaultMaintenanceRetryDoesNotTouchNewJob(t *testing.T) {
	runner := &fakeRunner{blockObserve: true, observing: make(chan struct{}), canceled: make(chan struct{})}
	l := lifecycleFixture(t, runner, 5*time.Second, time.Minute, 8)
	op := "11111111-1111-4111-8111-111111111111"
	if _, err := l.beginMaintenance(t.Context(), maintenanceRequest{OperationID: op}); err != nil {
		t.Fatal(err)
	}
	if _, err := l.journal.EndMaintenance(op); err != nil {
		t.Fatal(err)
	}
	if _, err := l.startWorkload(t.Context(), jobs.Workload{ID: strings.Repeat("d", 32), CWD: ".", Argv: []string{"/bin/true"}}, strings.Repeat("a", 64)); err != nil {
		t.Fatal(err)
	}
	select {
	case <-runner.observing:
	case <-time.After(time.Second):
		t.Fatal("Job did not start")
	}
	state, err := l.beginMaintenance(t.Context(), maintenanceRequest{OperationID: op})
	if err != nil || state.Active {
		t.Fatalf("completed retry: %+v %v", state, err)
	}
	select {
	case <-runner.canceled:
		t.Fatal("completed retry canceled a later Job")
	default:
	}
	if err := l.journal.AdmissionAllowed(); err != nil {
		t.Fatal("completed retry fenced later admission", err)
	}
}
