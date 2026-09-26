package lifecycle

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

type fakeRuntimeState struct {
	current    string
	components map[string]bool
}

type fakeTransactionBackend struct {
	current        string
	components     map[string]bool
	snapshots      map[string]fakeRuntimeState
	nextSnapshot   int
	componentCalls int
	restartCalls   int
	healthCalls    int
	healthErr      error
	restoreErr     error
	stopped        bool
}

func (b *fakeTransactionBackend) Snapshot(_ context.Context, _ OperationKind, snapshot Snapshot) (RuntimeSnapshot, error) {
	if b.snapshots == nil {
		b.snapshots = map[string]fakeRuntimeState{}
	}
	b.nextSnapshot++
	ref := "runtime-snapshot-" + strings.Repeat("x", b.nextSnapshot)
	components := make(map[string]bool, len(b.components))
	for name, enabled := range b.components {
		components[name] = enabled
	}
	b.snapshots[ref] = fakeRuntimeState{current: b.current, components: components}
	return RuntimeSnapshot{
		Ref: ref,
		Coverage: BackupCoverage{
			RuntimeState: true, ConfigState: true, HostState: true, WorkspacePreserved: true,
			OptionalComponentState: append([]string(nil), snapshot.Host.EnabledComponents...),
			ExternalReferences:     []string{"host-config", "platform-credentials"},
		},
	}, nil
}

func (b *fakeTransactionBackend) Activate(_ context.Context, candidate Generation, _ InstallationState) error {
	b.current = candidate.ID
	b.stopped = false
	return nil
}

func (b *fakeTransactionBackend) SetComponent(_ context.Context, _ Generation, name string, enabled bool) error {
	if b.components == nil {
		b.components = map[string]bool{}
	}
	b.componentCalls++
	b.components[name] = enabled
	return nil
}

func (b *fakeTransactionBackend) Migrate(context.Context, []MigrationStep) error { return nil }
func (b *fakeTransactionBackend) Restart(context.Context) error {
	b.restartCalls++
	b.stopped = false
	return nil
}
func (b *fakeTransactionBackend) Health(context.Context) error {
	b.healthCalls++
	err := b.healthErr
	b.healthErr = nil
	return err
}
func (b *fakeTransactionBackend) Restore(_ context.Context, ref string) error {
	if b.restoreErr != nil {
		return b.restoreErr
	}
	value, ok := b.snapshots[ref]
	if !ok {
		return errors.New("runtime snapshot missing")
	}
	b.current = value.current
	b.components = make(map[string]bool, len(value.components))
	for name, enabled := range value.components {
		b.components[name] = enabled
	}
	b.stopped = false
	return nil
}
func (b *fakeTransactionBackend) Stop(context.Context) error {
	b.stopped = true
	return nil
}
func (b *fakeTransactionBackend) VerifyStopped(context.Context) error {
	if !b.stopped {
		return errors.New("runtime is still active")
	}
	return nil
}

func transactionFixture(t *testing.T) (*FileStore, *fakeTransactionBackend, Generation, Generation, time.Time, string) {
	t.Helper()
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	active := generationFixture(t, "1.0.0", now.Add(-48*time.Hour), 1)
	candidate := generationFixture(t, "1.1.0", now.Add(-time.Hour), 1)
	root := privateLifecycleRoot(t)
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "keep.txt"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	writeLifecycleJSON(t, root, "host.json", hostFixture(active, 1), 0600)
	writeLifecycleJSON(t, root, "installed.json", active, 0600)
	writeLifecycleJSON(t, root, "available.json", candidate, 0600)
	writeLifecycleJSON(t, root, "installation.json", InstallationState{Scope: "system", Workspace: workspace}, 0600)
	store, err := OpenFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	backend := &fakeTransactionBackend{
		current:    active.ID,
		components: map[string]bool{"browser": true},
		snapshots:  map[string]fakeRuntimeState{},
	}
	return store, backend, active, candidate, now, workspace
}

func preparedTransaction(t *testing.T, store *FileStore, backend *fakeTransactionBackend, now time.Time) (*TransactionEngine, PreparedPlan) {
	t.Helper()
	engine := &TransactionEngine{Store: store, Backend: backend, Now: func() time.Time { return now }}
	manager := Manager{Store: store, Jobs: &fakeJobInventory{}, Applier: engine, Now: func() time.Time { return now }}
	plan, err := manager.Prepare(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	return engine, plan
}

func TestBackupRecordRequiresExplicitCompleteCoverage(t *testing.T) {
	store, _, _, _, now, _ := transactionFixture(t)
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	_, err = NewBackupRecord(OperationBackup, snapshot, RuntimeSnapshot{
		Ref: "runtime-snapshot",
		Coverage: BackupCoverage{
			RuntimeState:       true,
			ConfigState:        true,
			HostState:          true,
			WorkspacePreserved: true,
		},
	}, now)
	if err == nil || !strings.Contains(err.Error(), "optional-component") {
		t.Fatalf("incomplete optional coverage error = %v", err)
	}
	_, err = NewBackupRecord(OperationBackup, snapshot, RuntimeSnapshot{
		Ref: "runtime-snapshot",
		Coverage: BackupCoverage{
			RuntimeState:           true,
			ConfigState:            true,
			HostState:              true,
			OptionalComponentState: []string{"browser"},
		},
	}, now)
	if err == nil || !strings.Contains(err.Error(), "coverage is incomplete") {
		t.Fatalf("workspace coverage error = %v", err)
	}
}

func TestTransactionApplyCommitsCandidateAndRecoveryBackup(t *testing.T) {
	store, backend, active, candidate, now, _ := transactionFixture(t)
	engine, plan := preparedTransaction(t, store, backend, now)
	result, err := engine.Apply(t.Context(), ApplyRequest{Plan: plan})
	if err != nil {
		t.Fatal(err)
	}
	if result.PlanID != plan.ID || backend.current != candidate.ID {
		t.Fatalf("apply result=%#v runtime=%q", result, backend.current)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Installed == nil || snapshot.Installed.ID != candidate.ID || snapshot.Host.ActiveGenerationID != candidate.ID {
		t.Fatalf("committed snapshot = %#v", snapshot)
	}
	backups, err := store.ListBackups(t.Context())
	if err != nil || len(backups) != 1 || backups[0].Installed == nil || backups[0].Installed.ID != active.ID {
		t.Fatalf("recovery backups = %#v err=%v", backups, err)
	}
}

func TestTransactionApplyHealthFailureRestoresPreviousRelease(t *testing.T) {
	store, backend, active, _, now, _ := transactionFixture(t)
	engine, plan := preparedTransaction(t, store, backend, now)
	backend.healthErr = errors.New("candidate unhealthy")
	if _, err := engine.Apply(t.Context(), ApplyRequest{Plan: plan}); err == nil || !strings.Contains(err.Error(), "candidate unhealthy") {
		t.Fatalf("apply error = %v", err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if backend.current != active.ID || snapshot.Installed == nil || snapshot.Installed.ID != active.ID ||
		snapshot.Host.ActiveGenerationID != active.ID {
		t.Fatalf("rollback runtime=%q snapshot=%#v", backend.current, snapshot)
	}
}

func TestTransactionApplyRecoveryFailureIsTruthful(t *testing.T) {
	store, backend, _, _, now, _ := transactionFixture(t)
	engine, plan := preparedTransaction(t, store, backend, now)
	backend.healthErr = errors.New("candidate unhealthy")
	backend.restoreErr = errors.New("restore failed")
	_, err := engine.Apply(t.Context(), ApplyRequest{Plan: plan})
	var failed *RecoveryFailedError
	if !errors.As(err, &failed) || failed.Original != "candidate unhealthy" || failed.Recovery != "restore failed" {
		t.Fatalf("recovery error = %#v", err)
	}
	lock, err := AcquireOperationLock(store.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	journal, err := OpenOperationJournal(store.Root, lock, OperationJournalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	records, err := journal.List()
	if err != nil || len(records) != 1 || records[0].State != OperationRecoveryFailed ||
		records[0].OriginalError != "candidate unhealthy" || records[0].RecoveryError != "restore failed" {
		t.Fatalf("operation records = %#v err=%v", records, err)
	}
}

func TestTransactionRollbackRestoresPreviousRelease(t *testing.T) {
	store, backend, active, candidate, now, _ := transactionFixture(t)
	tick := now
	nextNow := func() time.Time {
		tick = tick.Add(time.Millisecond)
		return tick
	}
	engine := &TransactionEngine{Store: store, Backend: backend, Now: nextNow}
	manager := Manager{Store: store, Jobs: &fakeJobInventory{}, Applier: engine, Now: nextNow}
	if _, err := manager.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := manager.Apply(t.Context(), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	if backend.current != candidate.ID {
		t.Fatalf("runtime after update = %q", backend.current)
	}
	if err := engine.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if backend.current != active.ID || snapshot.Installed == nil || snapshot.Installed.ID != active.ID ||
		snapshot.Host.ActiveGenerationID != active.ID {
		t.Fatalf("rolled back runtime=%q snapshot=%#v", backend.current, snapshot)
	}
}

func TestTransactionRollbackCanRecoverUninstall(t *testing.T) {
	store, backend, active, _, now, workspace := transactionFixture(t)
	tick := now
	engine := &TransactionEngine{
		Store: store, Backend: backend,
		Now: func() time.Time {
			tick = tick.Add(time.Millisecond)
			return tick
		},
	}
	if err := engine.Uninstall(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Installed != nil || snapshot.Installation == nil || snapshot.Installation.Workspace != workspace {
		t.Fatalf("uninstalled snapshot = %#v", snapshot)
	}
	if err = engine.Rollback(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot, err = store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if backend.current != active.ID || snapshot.Installed == nil || snapshot.Installed.ID != active.ID ||
		snapshot.Installation == nil || snapshot.Installation.Workspace != workspace {
		t.Fatalf("rollback after uninstall runtime=%q snapshot=%#v", backend.current, snapshot)
	}
}

func TestTransactionBackupRestartsAndHealthChecksRuntime(t *testing.T) {
	store, backend, _, _, now, _ := transactionFixture(t)
	engine := &TransactionEngine{Store: store, Backend: backend, Now: func() time.Time { return now }}
	if _, err := engine.Backup(t.Context()); err != nil {
		t.Fatal(err)
	}
	if backend.restartCalls != 1 || backend.healthCalls != 1 || backend.stopped {
		t.Fatalf("backup runtime restart=%d health=%d stopped=%v", backend.restartCalls, backend.healthCalls, backend.stopped)
	}
}

func TestTransactionBackupRestoreAndUninstallPreserveWorkspace(t *testing.T) {
	store, backend, active, candidate, now, workspace := transactionFixture(t)
	engine, plan := preparedTransaction(t, store, backend, now)
	backup, err := engine.Backup(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if backup.Installed == nil || backup.Installed.ID != active.ID ||
		!backup.Coverage.RuntimeState || !backup.Coverage.ConfigState || !backup.Coverage.HostState ||
		!backup.Coverage.WorkspacePreserved || len(backup.Coverage.OptionalComponentState) != 1 ||
		backup.Coverage.OptionalComponentState[0] != "browser" || len(backup.Coverage.ExternalReferences) != 2 {
		t.Fatalf("backup = %#v", backup)
	}
	if _, err = engine.Apply(t.Context(), ApplyRequest{Plan: plan}); err != nil {
		t.Fatal(err)
	}
	if backend.current != candidate.ID {
		t.Fatalf("runtime after apply = %q", backend.current)
	}
	if err = engine.Restore(t.Context(), backup.ID); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if backend.current != active.ID || snapshot.Installed == nil || snapshot.Installed.ID != active.ID {
		t.Fatalf("restored runtime=%q snapshot=%#v", backend.current, snapshot)
	}
	if err = engine.Uninstall(t.Context()); err != nil {
		t.Fatal(err)
	}
	snapshot, err = store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Installed != nil || snapshot.Host.ActiveGenerationID != "" || snapshot.Installation == nil ||
		snapshot.Installation.Workspace != workspace {
		t.Fatalf("uninstalled snapshot = %#v", snapshot)
	}
	if raw, err := os.ReadFile(filepath.Join(workspace, "keep.txt")); err != nil || string(raw) != "keep" {
		t.Fatalf("workspace content after uninstall = %q err=%v", raw, err)
	}
	if err = store.InitializeInstall(t.Context(), candidate, InstallationState{Scope: "system", Workspace: workspace}, now.Add(time.Minute)); err != nil {
		t.Fatalf("reinitialize after uninstall: %v", err)
	}
	snapshot, err = store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Installed != nil || snapshot.Available == nil || snapshot.Available.ID != candidate.ID ||
		snapshot.Installation == nil || snapshot.Installation.Workspace != workspace {
		t.Fatalf("reinitialized snapshot = %#v", snapshot)
	}
}

func TestTransactionComponentChangeUsesGenericTransaction(t *testing.T) {
	store, backend, _, _, now, _ := transactionFixture(t)
	tick := now
	engine := &TransactionEngine{
		Store: store, Backend: backend,
		Now: func() time.Time {
			tick = tick.Add(time.Millisecond)
			return tick
		},
	}

	if err := engine.SetComponent(t.Context(), "browser", false); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Host.EnabledComponents) != 0 || backend.components["browser"] || backend.componentCalls != 1 {
		t.Fatalf("disabled component state host=%#v backend=%#v calls=%d", snapshot.Host.EnabledComponents, backend.components, backend.componentCalls)
	}
	snapshotCount := backend.nextSnapshot
	if err = engine.SetComponent(t.Context(), "browser", false); err != nil {
		t.Fatal(err)
	}
	if backend.componentCalls != 1 || backend.nextSnapshot != snapshotCount {
		t.Fatalf("idempotent disable mutated runtime: calls=%d snapshots=%d", backend.componentCalls, backend.nextSnapshot)
	}

	if err = engine.SetComponent(t.Context(), "browser", true); err != nil {
		t.Fatal(err)
	}
	snapshot, err = store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Host.EnabledComponents) != 1 || snapshot.Host.EnabledComponents[0] != "browser" ||
		!backend.components["browser"] || backend.componentCalls != 2 {
		t.Fatalf("enabled component state host=%#v backend=%#v calls=%d", snapshot.Host.EnabledComponents, backend.components, backend.componentCalls)
	}

	lock, err := AcquireOperationLock(store.Root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	journal, err := OpenOperationJournal(store.Root, lock, OperationJournalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	records, err := journal.List()
	if err != nil {
		t.Fatal(err)
	}
	var componentRecords int
	for _, record := range records {
		if record.Kind != OperationEnableComponent && record.Kind != OperationDisableComponent {
			continue
		}
		componentRecords++
		if record.State != OperationSucceeded || record.RecoveryBackupID == "" {
			t.Fatalf("component operation = %#v", record)
		}
	}
	if componentRecords != 2 {
		t.Fatalf("component operation count = %d", componentRecords)
	}
}

func TestTransactionComponentFailureRestoresPreviousState(t *testing.T) {
	store, backend, _, _, now, _ := transactionFixture(t)
	engine := &TransactionEngine{Store: store, Backend: backend, Now: func() time.Time { return now }}
	backend.healthErr = errors.New("component unhealthy")

	if err := engine.SetComponent(t.Context(), "browser", false); err == nil || !strings.Contains(err.Error(), "component unhealthy") {
		t.Fatalf("component change error = %v", err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshot.Host.EnabledComponents) != 1 || snapshot.Host.EnabledComponents[0] != "browser" ||
		!backend.components["browser"] {
		t.Fatalf("recovered component state host=%#v backend=%#v", snapshot.Host.EnabledComponents, backend.components)
	}
}

func TestOptionalComponentTargetRejectsUnknownAndRequiredComponents(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	generation := generationFixture(t, "1.0.0", now.Add(-time.Hour), 1)
	if _, _, err := optionalComponentTarget(generation, nil, "unknown", true); err == nil {
		t.Fatal("unknown component was accepted")
	}

	spec := releaseSpec("1.0.0", now.Add(-time.Hour), 1)
	spec.Components = append(spec.Components, Component{Name: "required-addon", Digest: digest("e")})
	required, err := NewGeneration(spec)
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err = optionalComponentTarget(required, nil, "required-addon", true); err == nil ||
		!strings.Contains(err.Error(), "required") {
		t.Fatalf("required component error = %v", err)
	}
}

func TestInstallationStateMCPPortDefaultsAndValidation(t *testing.T) {
	legacy := InstallationState{Scope: "user", Workspace: "/workspace"}
	explicit := InstallationState{Scope: "user", Workspace: "/workspace", MCPPort: DefaultMCPPort}
	if !legacy.Valid() || legacy.EffectiveMCPPort() != DefaultMCPPort || !legacy.Equivalent(explicit) {
		t.Fatalf("legacy/default MCP port normalization failed: legacy=%#v explicit=%#v", legacy, explicit)
	}
	for _, port := range []int{1, 1023, 65536} {
		state := InstallationState{Scope: "user", Workspace: "/workspace", MCPPort: port}
		if state.Valid() {
			t.Fatalf("invalid MCP port %d accepted", port)
		}
	}
	if state := (InstallationState{Scope: "user", Workspace: "/workspace", MCPPort: 19000}); !state.Valid() ||
		state.EffectiveMCPPort() != 19000 || state.Equivalent(explicit) {
		t.Fatalf("custom MCP port state = %#v", state)
	}
}

func TestInitializeInstallUsesWorkspaceWithoutDeletingIt(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	candidate := generationFixture(t, "1.0.0", now.Add(-time.Hour), 1)
	root := filepath.Join(t.TempDir(), "lifecycle")
	store, err := EnsureFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err = os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(workspace, "keep.txt"), []byte("existing"), 0600); err != nil {
		t.Fatal(err)
	}
	installation := InstallationState{Scope: "user", Workspace: workspace}
	if err = store.InitializeInstall(t.Context(), candidate, installation, now); err != nil {
		t.Fatal(err)
	}
	backend := &fakeTransactionBackend{current: "uninstalled", snapshots: map[string]fakeRuntimeState{}}
	engine := &TransactionEngine{Store: store, Backend: backend, Now: func() time.Time { return now }}
	manager := Manager{Store: store, Jobs: &fakeJobInventory{}, Applier: engine, Now: func() time.Time { return now }}
	if _, err = manager.Prepare(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Apply(t.Context(), ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Installed == nil || snapshot.Installed.ID != candidate.ID || snapshot.Installation == nil ||
		snapshot.Installation.Workspace != workspace || snapshot.Installation.MCPPort != DefaultMCPPort {
		t.Fatalf("installed snapshot = %#v", snapshot)
	}
	if raw, err := os.ReadFile(filepath.Join(workspace, "keep.txt")); err != nil || string(raw) != "existing" {
		t.Fatalf("workspace content after install = %q err=%v", raw, err)
	}
}
