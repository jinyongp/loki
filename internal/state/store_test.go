package state

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sync"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func newStore(t *testing.T) Store {
	t.Helper()
	s := Store{Dir: filepath.Join(t.TempDir(), "runtime")}
	if _, err := s.Initialize(context.Background(), json.RawMessage(`{"count":0}`)); err != nil {
		t.Fatal(err)
	}
	return s
}

func TestTransactionsAndConflicts(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	before, err := s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	if before.Revision != 1 {
		t.Fatal(before.Revision)
	}
	result, err := s.Update(ctx, &before.Revision, func(json.RawMessage) (json.RawMessage, error) { return json.RawMessage(`{"count":1}`), nil })
	if err != nil || result.Revision != 2 {
		t.Fatalf("update: %v %v", result, err)
	}
	called := false
	_, err = s.Update(ctx, &before.Revision, func(data json.RawMessage) (json.RawMessage, error) { called = true; return data, nil })
	if !errors.Is(err, ErrConflict) || called {
		t.Fatalf("stale update called=%v err=%v", called, err)
	}
	_, err = s.Update(ctx, nil, func(json.RawMessage) (json.RawMessage, error) { return nil, errors.New("reject") })
	if err == nil {
		t.Fatal("ignored mutation error")
	}
	after, _ := s.Load(ctx)
	if after.Revision != 2 {
		t.Fatal("failed update changed revision")
	}
	changed, err := s.Initialize(ctx, json.RawMessage(`{"count":0}`))
	if err != nil || changed {
		t.Fatalf("reinitialization %v %v", changed, err)
	}
	after, _ = s.Load(ctx)
	if after.Revision != 2 {
		t.Fatal("reinitialization overwrote data")
	}
}

func TestConcurrentStoresNoLostUpdates(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	var wg sync.WaitGroup
	for range 32 {
		wg.Go(func() {
			independent := Store{Dir: s.Dir}
			_, err := independent.Update(ctx, nil, func(data json.RawMessage) (json.RawMessage, error) {
				var d map[string]int
				if err := json.Unmarshal(data, &d); err != nil {
					return nil, err
				}
				d["count"]++
				return json.Marshal(d)
			})
			if err != nil {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	result, err := s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	var d map[string]int
	json.Unmarshal(result.Data, &d)
	if result.Revision != 33 || d["count"] != 32 {
		t.Fatalf("lost update: %v", result)
	}
}

func TestValidationRollsBackAndCiphertextIsPrivate(t *testing.T) {
	s := newStore(t)
	ctx := context.Background()
	s.Validate = func(data json.RawMessage) error {
		if bytes.Contains(data, []byte("reject")) {
			return errors.New("invalid domain state")
		}
		return nil
	}
	_, err := s.Update(ctx, nil, func(json.RawMessage) (json.RawMessage, error) { return json.RawMessage(`{"reject":true}`), nil })
	if err == nil {
		t.Fatal("ignored validator")
	}
	result, _ := s.Load(ctx)
	if result.Revision != 1 {
		t.Fatal("validation changed state")
	}
	_, err = s.Update(ctx, nil, func(json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage(`{"value":"synthetic-confidential-value"}`), nil
	})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"master.key", "store.json", "state.lock"} {
		info, err := os.Stat(filepath.Join(s.Dir, name))
		if err != nil {
			t.Fatal(err)
		}
		if info.Mode().Perm() != 0600 {
			t.Errorf("mode %s = %o", name, info.Mode().Perm())
		}
	}
	encoded, _ := os.ReadFile(filepath.Join(s.Dir, "store.json"))
	if bytes.Contains(encoded, []byte("synthetic-confidential-value")) {
		t.Fatal("plaintext on disk")
	}
	var envelope Envelope
	json.Unmarshal(encoded, &envelope)
	envelope.Ciphertext = "AAAA"
	bad, _ := json.Marshal(envelope)
	os.WriteFile(filepath.Join(s.Dir, "store.json"), bad, 0600)
	if _, err := s.Load(ctx); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("tampered store: %v", err)
	}
}

func TestLockCancellationAndPartialInitialization(t *testing.T) {
	s := newStore(t)
	release, err := lock(context.Background(), s.Dir)
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()
	if _, err = s.Load(ctx); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("lock wait: %v", err)
	}
	release()
	other := Store{Dir: filepath.Join(t.TempDir(), "partial")}
	os.Mkdir(other.Dir, 0700)
	os.WriteFile(filepath.Join(other.Dir, "master.key"), make([]byte, 32), 0600)
	if _, err = other.Initialize(context.Background(), json.RawMessage(`{}`)); err == nil {
		t.Fatal("overwrote partial state")
	}
}

func legacyFixture(t *testing.T) ([]byte, []byte, json.RawMessage) {
	t.Helper()
	data, err := os.ReadFile("testdata/python-v1.json")
	if err != nil {
		t.Fatal(err)
	}
	var f struct {
		Key       string          `json:"key_hex"`
		Envelope  json.RawMessage `json:"envelope"`
		Plaintext json.RawMessage `json:"plaintext"`
	}
	if err = json.Unmarshal(data, &f); err != nil {
		t.Fatal(err)
	}
	key, err := hex.DecodeString(f.Key)
	if err != nil {
		t.Fatal(err)
	}
	return key, f.Envelope, f.Plaintext
}

func TestPythonAESGCMFixtureAndMigration(t *testing.T) {
	key, encoded, want := legacyFixture(t)
	got, version, err := decrypt(key, encoded)
	if err != nil || version != 1 {
		t.Fatalf("python fixture: %v", err)
	}
	var a, b any
	json.Unmarshal(got.Data, &a)
	json.Unmarshal(want, &b)
	if !reflect.DeepEqual(a, b) {
		t.Fatal("Python plaintext mismatch")
	}
	parent := t.TempDir()
	source := filepath.Join(parent, "python")
	target := filepath.Join(parent, "go")
	os.Mkdir(source, 0700)
	os.WriteFile(filepath.Join(source, "master.key"), key, 0600)
	os.WriteFile(filepath.Join(source, "store.json"), encoded, 0600)
	ctx := context.Background()
	if err = ImportLegacy(ctx, source, target, nil); err != nil {
		t.Fatal(err)
	}
	if err = ImportLegacy(ctx, source, target, nil); err != nil {
		t.Fatalf("idempotent retry: %v", err)
	}
	s := Store{Dir: target}
	migrated, err := s.Load(ctx)
	if err != nil {
		t.Fatal(err)
	}
	json.Unmarshal(migrated.Data, &a)
	if migrated.Revision != 1 || !reflect.DeepEqual(a, b) {
		t.Fatal("migration changed data")
	}
	for _, dir := range []string{source, filepath.Join(target, "legacy")} {
		actual, _ := os.ReadFile(filepath.Join(dir, "store.json"))
		if !bytes.Equal(encoded, actual) {
			t.Fatalf("rollback/source changed: %s", dir)
		}
	}
	if _, err = (Store{Dir: source}).Load(ctx); err == nil {
		t.Fatal("loaded legacy store without explicit migration")
	}
	_, err = s.Update(ctx, nil, func(data json.RawMessage) (json.RawMessage, error) { return data, nil })
	if err != nil {
		t.Fatal(err)
	}
	if err = ImportLegacy(ctx, source, target, nil); err != nil {
		t.Fatal(err)
	}
	migrated, _ = s.Load(ctx)
	if migrated.Revision != 2 {
		t.Fatal("retry reset migrated state")
	}
}

func TestMigrationRejectsCorruptionExistingAndWeakPermissions(t *testing.T) {
	key, encoded, _ := legacyFixture(t)
	parent := t.TempDir()
	source := filepath.Join(parent, "python")
	os.Mkdir(source, 0700)
	os.WriteFile(filepath.Join(source, "master.key"), key, 0600)
	os.WriteFile(filepath.Join(source, "store.json"), encoded, 0600)
	ctx := context.Background()
	existing := filepath.Join(parent, "existing")
	os.Mkdir(existing, 0700)
	if err := ImportLegacy(ctx, source, existing, nil); err == nil {
		t.Fatal("overwrote existing directory")
	}
	if err := ImportLegacy(ctx, source, source, nil); err == nil {
		t.Fatal("allowed in-place migration")
	}
	os.Chmod(filepath.Join(source, "master.key"), 0644)
	if err := ImportLegacy(ctx, source, filepath.Join(parent, "bad"), nil); err == nil {
		t.Fatal("read world-readable key")
	}
	os.Chmod(filepath.Join(source, "master.key"), 0600)
	os.WriteFile(filepath.Join(source, "store.json"), []byte(`{"version":1,"nonce":"AAAA","ciphertext":"bad"}`), 0600)
	if err := ImportLegacy(ctx, source, filepath.Join(parent, "bad"), nil); !errors.Is(err, ErrDecrypt) {
		t.Fatalf("corruption: %v", err)
	}
	if _, err := os.Stat(filepath.Join(parent, "bad")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed migration created destination")
	}
}

func TestLegacyMigrationTransformsAndRestoresWithoutChangingSource(t *testing.T) {
	key, encoded, _ := legacyFixture(t)
	parent := t.TempDir()
	source := filepath.Join(parent, "python-copy")
	target := filepath.Join(parent, "go")
	restored := filepath.Join(parent, "restored-python")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "master.key"), key, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "store.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	var before unix.Stat_t
	if err := unix.Stat(filepath.Join(source, "store.json"), &before); err != nil {
		t.Fatal(err)
	}
	transform := func(json.RawMessage) (json.RawMessage, error) {
		return json.RawMessage("{\"version\":1,\"profiles\":{\"web\":{\"secrets\":{\"TOKEN\":\"synthetic\"}}}}"), nil
	}
	validator := func(data json.RawMessage) error {
		if !bytes.Contains(data, []byte("\"profiles\"")) {
			return errors.New("profiles missing")
		}
		return nil
	}
	if err := ImportLegacyWithTransform(t.Context(), source, target, validator, transform); err != nil {
		t.Fatal(err)
	}
	loaded, err := (Store{Dir: target, Validate: validator}).Load(t.Context())
	if err != nil || !bytes.Contains(loaded.Data, []byte("synthetic")) {
		t.Fatal("transformed readback failed:", err)
	}
	if err = RestoreLegacy(t.Context(), target, restored); err != nil {
		t.Fatal(err)
	}
	if err = RestoreLegacy(t.Context(), target, restored); err != nil {
		t.Fatal("idempotent restore:", err)
	}
	for _, name := range []string{"master.key", "store.json"} {
		want, readErr := os.ReadFile(filepath.Join(source, name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		got, readErr := os.ReadFile(filepath.Join(restored, name))
		if readErr != nil || !bytes.Equal(got, want) {
			t.Fatalf("restored %s differs: %v", name, readErr)
		}
	}
	var after unix.Stat_t
	if err = unix.Stat(filepath.Join(source, "store.json"), &after); err != nil {
		t.Fatal(err)
	}
	if before.Ino != after.Ino {
		t.Fatal("migration replaced source inode")
	}
	actual, err := os.ReadFile(filepath.Join(source, "store.json"))
	if err != nil || !bytes.Equal(actual, encoded) {
		t.Fatal("migration changed source digest")
	}
}

func TestLegacyMigrationCancellationAndCorruptRestoreAreAtomic(t *testing.T) {
	key, encoded, _ := legacyFixture(t)
	parent := t.TempDir()
	source := filepath.Join(parent, "python-copy")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "master.key"), key, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, "store.json"), encoded, 0600); err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	cancelled := filepath.Join(parent, "cancelled")
	if err := ImportLegacy(ctx, source, cancelled, nil); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled migration = %v", err)
	}
	if _, err := os.Lstat(cancelled); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("cancelled migration published a destination")
	}
	migration := filepath.Join(parent, "migration")
	if err := ImportLegacy(t.Context(), source, migration, nil); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(migration, "legacy", "store.json"), []byte("corrupt"), 0600); err != nil {
		t.Fatal(err)
	}
	restored := filepath.Join(parent, "restored")
	if err := RestoreLegacy(t.Context(), migration, restored); err == nil {
		t.Fatal("corrupt backup was restored")
	}
	if _, err := os.Lstat(restored); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("failed restore published a destination")
	}
}

func TestAtomicCreatePreservesExisting(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state")
	if err := AtomicWrite(path, []byte("old"), false); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWrite(path, []byte("new"), false); !errors.Is(err, os.ErrExist) {
		t.Fatalf("overwrite: %v", err)
	}
	actual, _ := os.ReadFile(path)
	if string(actual) != "old" {
		t.Fatal("overwrote existing file")
	}
}

func TestLockRejectsSpecialFilesAndPublicPermissions(t *testing.T) {
	dir := t.TempDir()
	fifo := filepath.Join(dir, "fifo")
	if err := unix.Mkfifo(fifo, 0600); err != nil {
		t.Fatal(err)
	}
	public := filepath.Join(dir, "public")
	if err := os.WriteFile(public, []byte{}, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(public, 0666); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(dir, "link")
	if err := os.Symlink(public, link); err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{fifo, public, link, dir} {
		ctx, cancel := context.WithTimeout(t.Context(), time.Second)
		release, err := LockFile(ctx, path)
		cancel()
		if err == nil {
			release()
			t.Errorf("unsafe lock accepted: %s", path)
		}
	}
}

func TestStorePreservesLargeJSONIntegers(t *testing.T) {
	s := Store{Dir: filepath.Join(t.TempDir(), "state")}
	want := json.RawMessage(`{"large":9007199254740993,"text":"<synthetic>"}`)
	if _, err := s.Initialize(t.Context(), want); err != nil {
		t.Fatal(err)
	}
	got, err := s.Load(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var decoded map[string]json.RawMessage
	if err = json.Unmarshal(got.Data, &decoded); err != nil {
		t.Fatal(err)
	}
	if string(decoded["large"]) != "9007199254740993" {
		t.Fatal("state integer precision lost")
	}
}

func TestUninitializedReadAndUpdate(t *testing.T) {
	s := Store{Dir: filepath.Join(t.TempDir(), "missing")}
	if _, err := s.Load(t.Context()); !errors.Is(err, ErrUninitialized) {
		t.Fatalf("missing state read: %v", err)
	}
	if _, err := s.Update(t.Context(), nil, func(json.RawMessage) (json.RawMessage, error) { t.Fatal("uninitialized mutation ran"); return nil, nil }); !errors.Is(err, ErrUninitialized) {
		t.Fatalf("missing state update: %v", err)
	}
}
