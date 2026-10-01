package lifecycle

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestManagedIntegrationFilesArePrivateAndNoFollow(t *testing.T) {
	root := privateLifecycleRoot(t)
	store, err := OpenFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	secret := []byte("private-github-key-material")
	digest, err := store.WriteManagedIntegrationFile(t.Context(), ManagedGitHubCredentialFile, secret)
	if err != nil {
		t.Fatal(err)
	}
	if digest != ManagedIntegrationDigest(secret) {
		t.Fatalf("digest=%q", digest)
	}
	path, err := store.ManagedIntegrationFilePath(ManagedGitHubCredentialFile)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(path)
	if err != nil {
		t.Fatal(err)
	}
	if !info.Mode().IsRegular() || info.Mode().Perm() != 0600 {
		t.Fatalf("credential mode=%v", info.Mode())
	}
	raw, err := store.ReadManagedIntegrationFile(t.Context(), ManagedGitHubCredentialFile, true)
	if err != nil || !bytes.Equal(raw, secret) {
		t.Fatalf("credential read=%q err=%v", raw, err)
	}

	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReadManagedIntegrationFile(t.Context(), ManagedGitHubCredentialFile, true); err == nil {
		t.Fatal("public credential file was accepted")
	}

	if err = os.Remove(path); err != nil {
		t.Fatal(err)
	}
	outside := filepath.Join(t.TempDir(), "outside-key")
	if err = os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(outside, path); err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReadManagedIntegrationFile(t.Context(), ManagedGitHubCredentialFile, true); err == nil {
		t.Fatal("symlink credential file was accepted")
	}
}

func TestManagedIntegrationSnapshotRoundTripAndLegacyRestore(t *testing.T) {
	root := privateLifecycleRoot(t)
	store, err := OpenFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	files := map[string][]byte{
		ManagedIntegrationStateFile:  []byte(`{"version":1,"browser":{"configured":true,"enabled":false},"signing":{"configured":false,"enabled":false},"github":{"configured":false,"enabled":false}}` + "\n"),
		ManagedGitHubConfigFile:      []byte("github_app_id = 123\n"),
		ManagedGitHubCredentialFile:  []byte("github-secret-A"),
		ManagedSigningPublicInfoFile: []byte(`{"public_key":"ssh-ed25519 AAAA"}` + "\n"),
		ManagedSigningCredentialFile: []byte("signing-secret-A"),
	}
	for name, raw := range files {
		if _, err = store.WriteManagedIntegrationFile(t.Context(), name, raw); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	ref, err := store.CaptureManagedIntegrationSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !validManagedIntegrationSnapshotRef(ref) {
		t.Fatalf("snapshot ref=%q", ref)
	}
	for name := range files {
		if _, err = store.WriteManagedIntegrationFile(t.Context(), name, []byte("mutated-"+name)); err != nil {
			t.Fatal(err)
		}
	}
	if err = store.RestoreManagedIntegrationSnapshot(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	for name, want := range files {
		got, readErr := store.ReadManagedIntegrationFile(t.Context(), name, true)
		if readErr != nil || !bytes.Equal(got, want) {
			t.Fatalf("restored %s=%q want=%q err=%v", name, got, want, readErr)
		}
	}

	if err = store.RestoreManagedIntegrationSnapshot(t.Context(), ""); err != nil {
		t.Fatal(err)
	}
	for name := range files {
		got, readErr := store.ReadManagedIntegrationFile(t.Context(), name, false)
		if readErr != nil || len(got) != 0 {
			t.Fatalf("legacy restore retained %s=%q err=%v", name, got, readErr)
		}
	}
}

func TestManagedIntegrationRecoveryPreservesOtherInterruptedOperations(t *testing.T) {
	store, backend, _, _, now, _ := transactionFixture(t)
	engine := &TransactionEngine{Store: store, Backend: backend, Now: func() time.Time { return now }}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	plan, err := maintenancePlan(snapshot, snapshot.Installed.ID, now)
	if err != nil {
		t.Fatal(err)
	}
	lock, journal, err := engine.openJournal(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = lock.Close() })
	record, err := journal.Begin(OperationApply, plan, now)
	if err != nil {
		t.Fatal(err)
	}
	if err = lock.Close(); err != nil {
		t.Fatal(err)
	}
	manager := Manager{Store: store, Maintainer: engine}
	for _, recover := range []func(context.Context) error{
		engine.RecoverManagedIntegration,
		func(ctx context.Context) error { return manager.RecoverManagedIntegration(ctx, MutationOptions{}) },
	} {
		if err = recover(t.Context()); err == nil || !strings.Contains(err.Error(), "requires recovery") {
			t.Fatalf("integration setup tried to recover a release operation: %v", err)
		}
	}
	records, err := ReadOperationSnapshot(store.Root)
	if err != nil || len(records) != 1 || records[0].ID != record.ID || records[0].State != OperationApplying {
		t.Fatal("integration recovery changed the release journal")
	}
	if backend.restartCalls != 0 || backend.healthCalls != 0 {
		t.Fatal("integration recovery changed the release runtime")
	}
}

func TestManagedIntegrationTransactionRestoresCredentialAndStateOnHealthFailure(t *testing.T) {
	store, backend, _, _, now, _ := transactionFixture(t)
	oldCredential := []byte("old-platform-private-key")
	oldDigest, err := store.WriteManagedIntegrationFile(t.Context(), ManagedGitHubCredentialFile, oldCredential)
	if err != nil {
		t.Fatal(err)
	}
	oldConfig := []byte("github_app_id = 123\n")
	oldConfigDigest, err := store.WriteManagedIntegrationFile(t.Context(), ManagedGitHubConfigFile, oldConfig)
	if err != nil {
		t.Fatal(err)
	}
	state := DefaultManagedIntegrationState()
	state.GitHub = ManagedIntegrationToggle{
		Configured: true, Enabled: true,
		CredentialSHA256: oldDigest, ConfigSHA256: oldConfigDigest,
	}
	if err = store.CommitManagedIntegrations(t.Context(), state, "fixture", now.Add(-time.Minute)); err != nil {
		t.Fatal(err)
	}
	before, err := store.ReadManagedIntegrations(t.Context())
	if err != nil {
		t.Fatal(err)
	}

	backend.healthErr = errors.New("integration health failed")
	engine := &TransactionEngine{Store: store, Backend: backend, Now: func() time.Time { return now }}
	newCredential := []byte("new-platform-private-key-that-must-rollback")
	err = engine.UpdateManagedIntegration(t.Context(), "github", func(ctx context.Context, store *FileStore) error {
		digest, writeErr := store.WriteManagedIntegrationFile(ctx, ManagedGitHubCredentialFile, newCredential)
		if writeErr != nil {
			return writeErr
		}
		next, readErr := store.ReadManagedIntegrations(ctx)
		if readErr != nil {
			return readErr
		}
		next.GitHub.CredentialSHA256 = digest
		return store.CommitManagedIntegrations(ctx, next, "rotate-github", now)
	})
	if err == nil || !strings.Contains(err.Error(), "integration health failed") {
		t.Fatalf("transaction error=%v", err)
	}

	gotCredential, err := store.ReadManagedIntegrationFile(t.Context(), ManagedGitHubCredentialFile, true)
	if err != nil || !bytes.Equal(gotCredential, oldCredential) {
		t.Fatalf("credential after rollback=%q err=%v", gotCredential, err)
	}
	after, err := store.ReadManagedIntegrations(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if after.GitHub.CredentialSHA256 != before.GitHub.CredentialSHA256 ||
		after.GitHub.ConfigSHA256 != before.GitHub.ConfigSHA256 ||
		after.GitHub.Enabled != before.GitHub.Enabled {
		t.Fatalf("integration state was not restored: before=%#v after=%#v", before.GitHub, after.GitHub)
	}

	backups, err := store.ListBackups(t.Context())
	if err != nil || len(backups) == 0 {
		t.Fatalf("backups=%#v err=%v", backups, err)
	}
	backup := backups[len(backups)-1]
	if backup.ManagedIntegrationRef == "" {
		t.Fatal("backup omitted managed integration snapshot reference")
	}
	metaPath, err := store.backupPath(backup.ID)
	if err != nil {
		t.Fatal(err)
	}
	meta, err := os.ReadFile(metaPath)
	if err != nil {
		t.Fatal(err)
	}
	if bytes.Contains(meta, oldCredential) || bytes.Contains(meta, newCredential) {
		t.Fatal("backup metadata contains raw platform credential bytes")
	}
}

func TestManagedIntegrationOrphanSnapshotIsCollected(t *testing.T) {
	store, _, _, _, now, _ := transactionFixture(t)
	ref, err := store.CaptureManagedIntegrationSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	backend := newStorageTransactionBackend()
	engine := &TransactionEngine{Store: store, Backend: backend, Now: func() time.Time { return now }}
	result, err := engine.CollectStorage(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if result.Before.OrphanManagedIntegrationSnapshots != 1 ||
		result.After.OrphanManagedIntegrationSnapshots != 0 ||
		!slices.Contains(result.RemovedOrphans, ref) {
		t.Fatalf("managed orphan collection=%#v ref=%s", result, ref)
	}
	if _, err = os.Stat(filepath.Join(store.managedIntegrationSnapshotRoot(), strings.TrimPrefix(ref, managedIntegrationSnapshotPrefix))); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("managed orphan snapshot still exists: %v", err)
	}
}
