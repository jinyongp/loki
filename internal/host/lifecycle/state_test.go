package lifecycle

import (
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func privateLifecycleRoot(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "lifecycle")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	return root
}

func writeLifecycleJSON(t *testing.T, root, name string, value any, mode os.FileMode) {
	t.Helper()
	raw, err := json.Marshal(value)
	if err != nil {
		t.Fatal(err)
	}
	raw = append(raw, '\n')
	path := filepath.Join(root, name)
	if err = os.WriteFile(path, raw, mode); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(path, mode); err != nil {
		t.Fatal(err)
	}
}

func TestFileStoreSnapshotAndPreparedPublication(t *testing.T) {
	now := time.Date(2026, 9, 21, 7, 0, 0, 0, time.UTC)
	active := generationFixture(t, "1.0.0", now.Add(-48*time.Hour), 1)
	candidate := generationFixture(t, "1.1.0", now.Add(-time.Hour), 1)
	host := hostFixture(active, 1)
	root := privateLifecycleRoot(t)
	writeLifecycleJSON(t, root, "host.json", host, 0600)
	writeLifecycleJSON(t, root, "installed.json", active, 0600)
	writeLifecycleJSON(t, root, "available.json", candidate, 0600)

	store, err := OpenFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Prepared != nil || snapshot.Installed == nil || snapshot.Installed.ID != active.ID ||
		snapshot.Available == nil || snapshot.Available.ID != candidate.ID {
		t.Fatalf("initial snapshot = %#v", snapshot)
	}
	if _, err = os.Stat(filepath.Join(root, "prepared.json")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("read-only snapshot created prepared state: %v", err)
	}

	manager := Manager{Store: store, Jobs: &fakeJobInventory{}, Now: func() time.Time { return now }}
	plan, err := manager.Prepare(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(filepath.Join(root, "prepared.json"))
	if err != nil || info.Mode().Perm()&0077 != 0 {
		t.Fatalf("prepared state mode = %v, %v", info, err)
	}
	snapshot, err = store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Prepared == nil || snapshot.Prepared.ID != plan.ID {
		t.Fatalf("prepared snapshot = %#v", snapshot.Prepared)
	}
}

func TestFileStoreRejectsUnsafeOrStaleState(t *testing.T) {
	now := time.Date(2026, 9, 21, 7, 0, 0, 0, time.UTC)
	active := generationFixture(t, "1.0.0", now.Add(-48*time.Hour), 1)
	candidate := generationFixture(t, "1.1.0", now.Add(-time.Hour), 1)
	host := hostFixture(active, 1)

	t.Run("public-host-file", func(t *testing.T) {
		root := privateLifecycleRoot(t)
		writeLifecycleJSON(t, root, "host.json", host, 0644)
		store, err := OpenFileStore(root)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = store.Snapshot(t.Context()); err == nil {
			t.Fatal("public lifecycle state file was accepted")
		}
	})

	t.Run("stale-publication", func(t *testing.T) {
		root := privateLifecycleRoot(t)
		writeLifecycleJSON(t, root, "host.json", host, 0600)
		writeLifecycleJSON(t, root, "installed.json", active, 0600)
		writeLifecycleJSON(t, root, "available.json", candidate, 0600)
		store, err := OpenFileStore(root)
		if err != nil {
			t.Fatal(err)
		}
		plan, err := Prepare(&active, candidate, host, now)
		if err != nil {
			t.Fatal(err)
		}
		changed := host
		changed.Revision = "host-revision-8"
		writeLifecycleJSON(t, root, "host.json", changed, 0600)
		if err = store.SavePrepared(t.Context(), plan); err == nil {
			t.Fatal("stale prepared plan was published")
		}
	})
}
