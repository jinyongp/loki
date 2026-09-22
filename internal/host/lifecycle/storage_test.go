package lifecycle

import (
	"context"
	"errors"
	"sort"
	"strings"
	"testing"
	"time"
)

type storageTransactionBackend struct {
	*fakeTransactionBackend
	storage       map[string]RuntimeSnapshotInfo
	snapshotBytes int64
}

func newStorageTransactionBackend() *storageTransactionBackend {
	return &storageTransactionBackend{
		fakeTransactionBackend: &fakeTransactionBackend{},
		storage:                map[string]RuntimeSnapshotInfo{},
		snapshotBytes:          128,
	}
}

func (b *storageTransactionBackend) Snapshot(ctx context.Context, kind OperationKind, snapshot Snapshot) (RuntimeSnapshot, error) {
	runtime, err := b.fakeTransactionBackend.Snapshot(ctx, kind, snapshot)
	if err != nil {
		return RuntimeSnapshot{}, err
	}
	b.storage[runtime.Ref] = RuntimeSnapshotInfo{
		Ref: runtime.Ref, Bytes: b.snapshotBytes, CreatedAt: time.Now().UTC(),
	}
	return runtime, nil
}

func (b *storageTransactionBackend) RuntimeSnapshotUsage(_ context.Context, ref string) (int64, error) {
	info, ok := b.storage[ref]
	if !ok {
		return 0, errors.New("runtime snapshot missing")
	}
	return info.Bytes, nil
}

func (b *storageTransactionBackend) ListRuntimeSnapshots(ctx context.Context) ([]RuntimeSnapshotInfo, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	result := make([]RuntimeSnapshotInfo, 0, len(b.storage))
	for _, info := range b.storage {
		result = append(result, info)
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].CreatedAt.Equal(result[j].CreatedAt) {
			return result[i].Ref < result[j].Ref
		}
		return result[i].CreatedAt.Before(result[j].CreatedAt)
	})
	return result, nil
}

func (b *storageTransactionBackend) DeleteRuntimeSnapshot(ctx context.Context, ref string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, ok := b.storage[ref]; !ok {
		return errors.New("runtime snapshot missing")
	}
	delete(b.storage, ref)
	return nil
}

func lifecycleBackupFixture(
	t *testing.T,
	store *FileStore,
	backend *storageTransactionBackend,
	reason OperationKind,
	generation *Generation,
	host HostState,
	installation *InstallationState,
	ref string,
	bytes int64,
	created time.Time,
) BackupRecord {
	t.Helper()
	backend.storage[ref] = RuntimeSnapshotInfo{Ref: ref, Bytes: bytes, CreatedAt: created}
	backup, err := NewBackupRecord(reason, Snapshot{
		Installed: generation, Host: host, Installation: installation,
	}, RuntimeSnapshot{
		Ref: ref,
		Coverage: BackupCoverage{
			RuntimeState: true, ConfigState: true, HostState: true, WorkspacePreserved: true,
			OptionalComponentState: append([]string(nil), host.EnabledComponents...),
			ExternalReferences:     []string{"host-config"},
		},
	}, created)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.SaveBackup(t.Context(), backup); err != nil {
		t.Fatal(err)
	}
	return backup
}

func TestLifecycleStorageCollectPreservesRollbackAndActiveRecoveryRoots(t *testing.T) {
	now := time.Date(2026, 9, 22, 2, 0, 0, 0, time.UTC)
	active := generationFixture(t, "1.0.0", now.Add(-72*time.Hour), 1)
	candidate := generationFixture(t, "1.1.0", now.Add(-48*time.Hour), 1)
	root := privateLifecycleRoot(t)
	store, err := OpenFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	store.StorageLimits = LifecycleStorageLimits{
		MaxBytes: 1 << 20, MaxBackups: 8, Retention: time.Hour,
	}
	backend := newStorageTransactionBackend()
	installation := InstallationState{Scope: "user", Workspace: "/workspace"}
	host := hostFixture(active, 1)

	recovery := lifecycleBackupFixture(
		t, store, backend, OperationApply, &active, host, &installation,
		"runtime-recovery", 128, now.Add(-5*time.Hour),
	)
	expired := lifecycleBackupFixture(
		t, store, backend, OperationBackup, &active, host, &installation,
		"runtime-expired", 128, now.Add(-4*time.Hour),
	)
	rollback := lifecycleBackupFixture(
		t, store, backend, OperationApply, &candidate, hostFixture(candidate, 1), &installation,
		"runtime-rollback", 128, now.Add(-3*time.Hour),
	)
	backend.storage["runtime-orphan"] = RuntimeSnapshotInfo{
		Ref: "runtime-orphan", Bytes: 64, CreatedAt: now.Add(-6 * time.Hour),
	}

	lock, err := AcquireOperationLock(root)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := OpenOperationJournal(root, lock, OperationJournalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		// The storage fixture intentionally has no normal lifecycle state files.
		// Build a valid plan directly from the same host identity instead.
		snapshot = Snapshot{
			Installed: &active, Available: &candidate, Host: host,
			Installation: &installation,
		}
	}
	plan, err := maintenancePlan(snapshot, candidate.ID, now.Add(-2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	record, err := journal.Begin(OperationApply, plan, now.Add(-2*time.Hour))
	if err != nil {
		t.Fatal(err)
	}
	if _, err = journal.RecordSnapshot(record.ID, recovery.ID, now.Add(-2*time.Hour)); err != nil {
		t.Fatal(err)
	}
	if err = lock.Close(); err != nil {
		t.Fatal(err)
	}

	engine := &TransactionEngine{
		Store: store, Backend: backend, Now: func() time.Time { return now },
	}
	result, err := engine.CollectStorage(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !result.Supported || len(result.RemovedBackups) != 1 || result.RemovedBackups[0] != expired.ID {
		t.Fatalf("lifecycle storage collection = %#v", result)
	}
	if len(result.RemovedOrphans) != 1 || result.RemovedOrphans[0] != "runtime-orphan" {
		t.Fatalf("orphan collection = %#v", result)
	}
	for _, id := range []string{recovery.ID, rollback.ID} {
		if _, err = store.LoadBackup(t.Context(), id); err != nil {
			t.Fatalf("protected backup %s was reclaimed: %v", id, err)
		}
	}
	if _, err = store.LoadBackup(t.Context(), expired.ID); err == nil {
		t.Fatal("expired unreferenced backup survived collection")
	}
	if _, ok := backend.storage["runtime-recovery"]; !ok {
		t.Fatal("active recovery runtime snapshot was reclaimed")
	}
	if _, ok := backend.storage["runtime-rollback"]; !ok {
		t.Fatal("rollback runtime snapshot was reclaimed")
	}
}

func TestLifecycleStorageQuotaFailsWhenOnlyProtectedBackupsRemain(t *testing.T) {
	now := time.Date(2026, 9, 22, 3, 0, 0, 0, time.UTC)
	active := generationFixture(t, "1.0.0", now.Add(-48*time.Hour), 1)
	root := privateLifecycleRoot(t)
	store, err := OpenFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	store.StorageLimits = LifecycleStorageLimits{
		MaxBytes: 64, MaxBackups: 1, Retention: time.Hour,
	}
	backend := newStorageTransactionBackend()
	installation := InstallationState{Scope: "user", Workspace: "/workspace"}
	host := hostFixture(active, 1)
	backup := lifecycleBackupFixture(
		t, store, backend, OperationApply, &active, host, &installation,
		"runtime-protected", 256, now.Add(-2*time.Hour),
	)

	engine := &TransactionEngine{Store: store, Backend: backend, Now: func() time.Time { return now }}
	result, err := engine.CollectStorage(t.Context())
	if !errors.Is(err, ErrLifecycleStorageQuotaExceeded) {
		t.Fatalf("protected lifecycle quota error = %v result=%#v", err, result)
	}
	if _, err = store.LoadBackup(t.Context(), backup.ID); err != nil {
		t.Fatalf("protected rollback backup removed under quota pressure: %v", err)
	}
}

func TestTransactionQuotaFailureRemovesUnpublishedBackupAndRuntimeSnapshot(t *testing.T) {
	store, baseBackend, _, _, now, _ := transactionFixture(t)
	store.StorageLimits = LifecycleStorageLimits{
		MaxBytes: 64, MaxBackups: 1, Retention: time.Hour,
	}
	backend := &storageTransactionBackend{
		fakeTransactionBackend: baseBackend,
		storage:                map[string]RuntimeSnapshotInfo{},
		snapshotBytes:          256,
	}
	engine := &TransactionEngine{Store: store, Backend: backend, Now: func() time.Time { return now }}
	manager := Manager{
		Store: store, Jobs: &fakeJobInventory{}, Applier: engine, Now: func() time.Time { return now },
	}
	plan, err := manager.Prepare(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = engine.Apply(t.Context(), ApplyRequest{Plan: plan}); !errors.Is(err, ErrLifecycleStorageQuotaExceeded) {
		t.Fatalf("quota-limited apply error = %v", err)
	}
	backups, err := store.ListBackups(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(backups) != 0 || len(backend.storage) != 0 {
		t.Fatalf("quota failure retained unpublished state: backups=%#v snapshots=%#v", backups, backend.storage)
	}
}

func TestLifecycleStorageCollectIsNoopForBackendWithoutStorageCapability(t *testing.T) {
	store, backend, _, _, now, _ := transactionFixture(t)
	engine := &TransactionEngine{Store: store, Backend: backend, Now: func() time.Time { return now }}
	result, err := engine.CollectStorage(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.Supported || len(result.RemovedBackups) != 0 || len(result.RemovedOrphans) != 0 {
		t.Fatalf("unsupported storage collection = %#v", result)
	}
}

func TestNewestRollbackBackupIgnoresManualBackups(t *testing.T) {
	now := time.Date(2026, 9, 22, 4, 0, 0, 0, time.UTC)
	active := generationFixture(t, "1.0.0", now.Add(-48*time.Hour), 1)
	host := hostFixture(active, 1)
	installation := InstallationState{Scope: "user", Workspace: "/workspace"}
	makeBackup := func(reason OperationKind, created time.Time, ref string) BackupRecord {
		backup, err := NewBackupRecord(reason, Snapshot{
			Installed: &active, Host: host, Installation: &installation,
		}, RuntimeSnapshot{
			Ref: ref,
			Coverage: BackupCoverage{
				RuntimeState: true, ConfigState: true, HostState: true, WorkspacePreserved: true,
				OptionalComponentState: append([]string(nil), host.EnabledComponents...),
				ExternalReferences:     []string{"host-config"},
			},
		}, created)
		if err != nil {
			t.Fatal(err)
		}
		return backup
	}
	rollback := makeBackup(OperationApply, now.Add(-time.Hour), "rollback")
	manual := makeBackup(OperationBackup, now, "manual")
	if got := newestRollbackBackup([]BackupRecord{rollback, manual}); got != rollback.ID {
		t.Fatalf("rollback root = %q, want %q", got, rollback.ID)
	}
	if strings.TrimSpace(gotOrEmpty(newestRollbackBackup(nil))) != "" {
		t.Fatal("empty rollback set returned a root")
	}
}

func gotOrEmpty(value string) string { return value }
