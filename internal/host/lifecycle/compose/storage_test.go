package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRuntimeSnapshotStorageAccountsListsAndDeletes(t *testing.T) {
	backend, err := New(Config{StateRoot: t.TempDir(), Runner: &fakeRunner{}})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := os.MkdirTemp(backend.snapshotRoot, ".snapshot-")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(ref, 0700); err != nil {
		t.Fatal(err)
	}
	if err = writePrivateJSON(filepath.Join(ref, "manifest.json"), snapshotManifest{
		Version: snapshotManifestVersion, Volumes: map[string]snapshotVolume{},
	}); err != nil {
		t.Fatal(err)
	}
	payload := strings.Repeat("x", 256)
	if err = os.WriteFile(filepath.Join(ref, "runtime-state.tar"), []byte(payload), 0600); err != nil {
		t.Fatal(err)
	}
	when := time.Date(2026, 9, 22, 1, 2, 3, 0, time.UTC)
	if err = os.Chtimes(filepath.Join(ref, "manifest.json"), when, when); err != nil {
		t.Fatal(err)
	}

	bytes, err := backend.RuntimeSnapshotUsage(t.Context(), ref)
	if err != nil {
		t.Fatal(err)
	}
	if bytes <= int64(len(payload)) {
		t.Fatalf("runtime snapshot bytes = %d, want manifest + payload", bytes)
	}
	snapshots, err := backend.ListRuntimeSnapshots(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if len(snapshots) != 1 || snapshots[0].Ref != ref || snapshots[0].Bytes != bytes ||
		!snapshots[0].CreatedAt.Equal(when) {
		t.Fatalf("runtime snapshots = %#v", snapshots)
	}
	if err = backend.DeleteRuntimeSnapshot(t.Context(), ref); err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(ref); !os.IsNotExist(err) {
		t.Fatalf("deleted runtime snapshot still exists: %v", err)
	}
	if err = backend.DeleteRuntimeSnapshot(t.Context(), ref); err != nil {
		t.Fatalf("repeated runtime snapshot deletion = %v", err)
	}
}

func TestRuntimeSnapshotStorageRejectsUnsafeTree(t *testing.T) {
	backend, err := New(Config{StateRoot: t.TempDir(), Runner: &fakeRunner{}})
	if err != nil {
		t.Fatal(err)
	}
	ref, err := os.MkdirTemp(backend.snapshotRoot, ".snapshot-")
	if err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(ref, 0700); err != nil {
		t.Fatal(err)
	}
	if err = writePrivateJSON(filepath.Join(ref, "manifest.json"), snapshotManifest{
		Version: snapshotManifestVersion, Volumes: map[string]snapshotVolume{},
	}); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink("/tmp", filepath.Join(ref, "unsafe")); err != nil {
		t.Fatal(err)
	}
	if _, err = backend.RuntimeSnapshotUsage(t.Context(), ref); err == nil ||
		!strings.Contains(err.Error(), "symlink") {
		t.Fatalf("unsafe snapshot accounting error = %v", err)
	}
	if _, err = backend.ListRuntimeSnapshots(t.Context()); err == nil ||
		!strings.Contains(err.Error(), "symlink") {
		t.Fatalf("unsafe snapshot listing error = %v", err)
	}
}
