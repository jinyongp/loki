package lifecycle

import (
	"context"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"
)

type fakeLifecycleStore struct {
	snapshot Snapshot
	saved    []PreparedPlan
	err      error
}

func (s *fakeLifecycleStore) Snapshot(context.Context) (Snapshot, error) {
	if s.err != nil {
		return Snapshot{}, s.err
	}
	return s.snapshot, nil
}

func (s *fakeLifecycleStore) SavePrepared(_ context.Context, plan PreparedPlan) error {
	if s.err != nil {
		return s.err
	}
	s.saved = append(s.saved, plan)
	s.snapshot.Prepared = &s.saved[len(s.saved)-1]
	return nil
}

type fakeJobInventory struct {
	jobs  []string
	calls int
	err   error
}

func (i *fakeJobInventory) ActiveJobs(context.Context) ([]string, error) {
	i.calls++
	return append([]string(nil), i.jobs...), i.err
}

type fakeApplier struct {
	requests []ApplyRequest
	result   ApplyResult
	err      error
}

func (a *fakeApplier) Apply(_ context.Context, request ApplyRequest) (ApplyResult, error) {
	request.ActiveJobs = append([]string(nil), request.ActiveJobs...)
	a.requests = append(a.requests, request)
	if a.err != nil {
		return ApplyResult{}, a.err
	}
	result := a.result
	if result.PlanID == "" {
		result.PlanID = request.Plan.ID
	}
	return result, nil
}

type fakeComponentCall struct {
	name    string
	enabled bool
}

type fakeMaintainer struct {
	backupCalls    int
	restoreCalls   []string
	rollbackCalls  int
	uninstallCalls int
	componentCalls []fakeComponentCall
}

func (m *fakeMaintainer) Backup(context.Context) (BackupRecord, error) {
	m.backupCalls++
	return BackupRecord{}, nil
}

func (m *fakeMaintainer) Restore(_ context.Context, backupID string) error {
	m.restoreCalls = append(m.restoreCalls, backupID)
	return nil
}

func (m *fakeMaintainer) Rollback(context.Context) error {
	m.rollbackCalls++
	return nil
}

func (m *fakeMaintainer) Uninstall(context.Context) error {
	m.uninstallCalls++
	return nil
}

func (m *fakeMaintainer) SetComponent(_ context.Context, name string, enabled bool) error {
	m.componentCalls = append(m.componentCalls, fakeComponentCall{name: name, enabled: enabled})
	return nil
}

func managerFixture(t *testing.T) (Manager, *fakeLifecycleStore, *fakeJobInventory, *fakeApplier, time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 21, 7, 0, 0, 0, time.UTC)
	active := generationFixture(t, "1.0.0", now.Add(-48*time.Hour), 1)
	candidate := generationFixture(t, "1.1.0", now.Add(-time.Hour), 1)
	host := hostFixture(active, 1)
	store := &fakeLifecycleStore{snapshot: Snapshot{Installed: &active, Available: &candidate, Host: host}}
	jobs := &fakeJobInventory{}
	applier := &fakeApplier{}
	manager := Manager{Store: store, Jobs: jobs, Applier: applier, Now: func() time.Time { return now }}
	return manager, store, jobs, applier, now
}

func TestManagerStatusIsReadOnly(t *testing.T) {
	manager, store, jobs, applier, _ := managerFixture(t)
	status, err := manager.Status(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !status.UpdateAvailable || len(store.saved) != 0 || jobs.calls != 0 || len(applier.requests) != 0 {
		t.Fatalf("read-only status changed state: %#v saved=%d jobs=%d apply=%d", status, len(store.saved), jobs.calls, len(applier.requests))
	}
}

func TestManagerPreparePublishesInspectablePlanOnly(t *testing.T) {
	manager, store, jobs, applier, _ := managerFixture(t)
	plan, err := manager.Prepare(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !plan.Valid() || len(store.saved) != 1 || store.saved[0].ID != plan.ID {
		t.Fatalf("prepared plan = %#v saved=%#v", plan, store.saved)
	}
	if jobs.calls != 0 || len(applier.requests) != 0 {
		t.Fatalf("prepare touched runtime jobs/apply: jobs=%d apply=%d", jobs.calls, len(applier.requests))
	}
}

func TestManagerApplyRejectsStalePreparedPlanBeforeJobInspection(t *testing.T) {
	manager, store, jobs, applier, _ := managerFixture(t)
	if _, err := manager.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	store.snapshot.Host.Revision = "host-revision-8"
	if _, err := manager.Apply(t.Context(), ApplyOptions{}); err == nil || !strings.Contains(err.Error(), "stale") {
		t.Fatalf("stale apply error = %v", err)
	}
	if jobs.calls != 0 || len(applier.requests) != 0 {
		t.Fatalf("stale plan reached jobs/applier: jobs=%d apply=%d", jobs.calls, len(applier.requests))
	}
}

func TestManagerApplyBlocksActiveJobsByDefault(t *testing.T) {
	manager, _, jobs, applier, _ := managerFixture(t)
	if _, err := manager.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	jobs.jobs = []string{"job-b", "job-a", "job-a"}
	_, err := manager.Apply(t.Context(), ApplyOptions{})
	var blocked *BlockedJobsError
	if !errors.As(err, &blocked) {
		t.Fatalf("blocked apply error = %v", err)
	}
	if !reflect.DeepEqual(blocked.Jobs, []string{"job-a", "job-b"}) {
		t.Fatalf("blocked jobs = %#v", blocked.Jobs)
	}
	if len(applier.requests) != 0 {
		t.Fatal("blocked apply reached mutation engine")
	}
}

func TestManagerApplyExplicitInterruptionPassesExactPlanAndJobs(t *testing.T) {
	manager, _, jobs, applier, _ := managerFixture(t)
	plan, err := manager.Prepare(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	jobs.jobs = []string{"job-b", "job-a"}
	result, err := manager.Apply(t.Context(), ApplyOptions{InterruptActiveJobs: true})
	if err != nil {
		t.Fatal(err)
	}
	if result.PlanID != plan.ID || len(applier.requests) != 1 {
		t.Fatalf("apply result/requests = %#v / %#v", result, applier.requests)
	}
	request := applier.requests[0]
	if request.Plan.ID != plan.ID || !request.Options.InterruptActiveJobs ||
		!reflect.DeepEqual(request.ActiveJobs, []string{"job-a", "job-b"}) {
		t.Fatalf("apply request = %#v", request)
	}
}

func TestManagerApplyRequiresTransactionEngineAfterPreflight(t *testing.T) {
	manager, _, jobs, _, _ := managerFixture(t)
	if _, err := manager.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	manager.Applier = nil
	jobs.jobs = nil
	if _, err := manager.Apply(t.Context(), ApplyOptions{}); err == nil || !strings.Contains(err.Error(), "transaction engine") {
		t.Fatalf("missing applier error = %v", err)
	}
}

func TestManagerMaintenanceUsesSharedActiveJobPolicy(t *testing.T) {
	manager, _, jobs, _, _ := managerFixture(t)
	maintainer := &fakeMaintainer{}
	manager.Maintainer = maintainer
	jobs.jobs = []string{"job-b", "job-a", "job-a"}

	if _, err := manager.Backup(t.Context(), MutationOptions{}); err == nil {
		t.Fatal("backup with active jobs was accepted")
	}
	if err := manager.Restore(t.Context(), "sha256:"+strings.Repeat("a", 64), MutationOptions{}); err == nil {
		t.Fatal("restore with active jobs was accepted")
	}
	if err := manager.Rollback(t.Context(), MutationOptions{}); err == nil {
		t.Fatal("rollback with active jobs was accepted")
	}
	if err := manager.Uninstall(t.Context(), MutationOptions{}); err == nil {
		t.Fatal("uninstall with active jobs was accepted")
	}
	if err := manager.SetComponent(t.Context(), "browser", false, MutationOptions{}); err == nil {
		t.Fatal("component disable with active jobs was accepted")
	}
	if maintainer.backupCalls != 0 || len(maintainer.restoreCalls) != 0 ||
		maintainer.rollbackCalls != 0 || maintainer.uninstallCalls != 0 || len(maintainer.componentCalls) != 0 {
		t.Fatalf("blocked maintenance reached engine: %#v", maintainer)
	}

	options := MutationOptions{InterruptActiveJobs: true}
	if _, err := manager.Backup(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	if err := manager.Restore(t.Context(), "sha256:"+strings.Repeat("b", 64), options); err != nil {
		t.Fatal(err)
	}
	if err := manager.Rollback(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	if err := manager.Uninstall(t.Context(), options); err != nil {
		t.Fatal(err)
	}
	if err := manager.SetComponent(t.Context(), "browser", true, options); err != nil {
		t.Fatal(err)
	}
	if maintainer.backupCalls != 1 || !reflect.DeepEqual(maintainer.restoreCalls, []string{"sha256:" + strings.Repeat("b", 64)}) ||
		maintainer.rollbackCalls != 1 || maintainer.uninstallCalls != 1 ||
		!reflect.DeepEqual(maintainer.componentCalls, []fakeComponentCall{{name: "browser", enabled: true}}) {
		t.Fatalf("approved maintenance calls = %#v", maintainer)
	}
}
