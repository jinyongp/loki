package browser

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeInboxToken(t *testing.T, inbox, token, content string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(filepath.Join(inbox, token), []byte(content), mode); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(inbox, token), mode); err != nil {
		t.Fatal(err)
	}
}

func TestUploadStoreConsumesOwnedTokensAndPreservesNames(t *testing.T) {
	base := t.TempDir()
	inbox := filepath.Join(base, "socket", "uploads")
	if err := os.MkdirAll(inbox, 0o770); err != nil {
		t.Fatal(err)
	}
	stale := strings.Repeat("9", 32)
	writeInboxToken(t, inbox, stale, "stale", 0o640)
	store, err := newUploadStore(filepath.Join(base, "private", "uploads"), inbox, uint32(os.Getuid()), 2, 16)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Stat(filepath.Join(inbox, stale)); !os.IsNotExist(err) {
		t.Fatalf("stale upload token survived browser startup: %v", err)
	}
	one := strings.Repeat("a", 32)
	two := strings.Repeat("b", 32)
	writeInboxToken(t, inbox, one, "one", 0o640)
	writeInboxToken(t, inbox, two, "two", 0o640)

	prepared, err := store.Prepare([]stagedUploadRef{
		{Token: one, Name: "same.txt"},
		{Token: two, Name: "same.txt"},
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(prepared.paths) != 2 || prepared.paths[0] == prepared.paths[1] || prepared.bytes != 6 {
		t.Fatalf("prepared upload = %#v", prepared)
	}
	for index, want := range []string{"one", "two"} {
		if filepath.Base(prepared.paths[index]) != "same.txt" {
			t.Fatalf("staged basename = %q", filepath.Base(prepared.paths[index]))
		}
		data, err := os.ReadFile(prepared.paths[index])
		if err != nil || string(data) != want {
			t.Fatalf("staged[%d] = %q %v", index, data, err)
		}
		info, err := os.Stat(prepared.paths[index])
		if err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("staged mode = %#o %v", info.Mode().Perm(), err)
		}
	}
	for _, token := range []string{one, two} {
		if _, err := os.Stat(filepath.Join(inbox, token)); !os.IsNotExist(err) {
			t.Fatalf("consumed inbox token survived: %s %v", token, err)
		}
	}

	store.Commit(prepared)
	if store.retainedFiles != 2 || store.retainedBytes != 6 {
		t.Fatalf("retained counters = %d %d", store.retainedFiles, store.retainedBytes)
	}
	if _, err = store.Prepare([]stagedUploadRef{{Token: one, Name: "again.txt"}}); err == nil {
		t.Fatal("retained file-count limit was not enforced")
	}
	store.Clear()
	for _, path := range prepared.paths {
		if _, err := os.Stat(path); !os.IsNotExist(err) {
			t.Fatalf("private staging survived clear: %s %v", path, err)
		}
	}
}

func TestUploadStoreRejectsUnsafeTokensAndNames(t *testing.T) {
	base := t.TempDir()
	inbox := filepath.Join(base, "socket", "uploads")
	if err := os.MkdirAll(filepath.Dir(inbox), 0o750); err != nil {
		t.Fatal(err)
	}
	store, err := newUploadStore(filepath.Join(base, "private", "uploads"), inbox, uint32(os.Getuid()), 5, 8)
	if err != nil {
		t.Fatal(err)
	}

	validToken := strings.Repeat("c", 32)
	writeInboxToken(t, inbox, validToken, "0123456789", 0o640)
	if _, err = store.Prepare([]stagedUploadRef{{Token: validToken, Name: "big.bin"}}); err == nil {
		t.Fatal("oversized staged upload accepted")
	}

	worldReadable := strings.Repeat("d", 32)
	writeInboxToken(t, inbox, worldReadable, "x", 0o644)
	if _, err = store.Prepare([]stagedUploadRef{{Token: worldReadable, Name: "x.txt"}}); err == nil {
		t.Fatal("world-readable staged upload accepted")
	}

	target := filepath.Join(base, "target")
	if err = os.WriteFile(target, []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	symlinkToken := strings.Repeat("e", 32)
	if err = os.Symlink(target, filepath.Join(inbox, symlinkToken)); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Prepare([]stagedUploadRef{{Token: symlinkToken, Name: "x.txt"}}); err == nil {
		t.Fatal("symlink staged upload accepted")
	}

	for _, args := range []map[string]any{
		{},
		{"staged_files": []any{}},
		{"staged_files": []any{"token"}},
		{"staged_files": []any{map[string]any{"token": "bad", "name": "x.txt"}}},
		{"staged_files": []any{map[string]any{"token": strings.Repeat("f", 32), "name": "../x.txt"}}},
		{"staged_files": []any{map[string]any{"token": strings.Repeat("f", 32), "name": "x/y.txt"}}},
		{"staged_files": []any{
			map[string]any{"token": strings.Repeat("f", 32), "name": "one.txt"},
			map[string]any{"token": strings.Repeat("f", 32), "name": "two.txt"},
		}},
	} {
		if _, err := stagedUploadRefs(args); err == nil {
			t.Fatalf("invalid staged refs accepted: %#v", args)
		}
	}
}

func TestStagedUploadRefs(t *testing.T) {
	token := strings.Repeat("a", 32)
	refs, err := stagedUploadRefs(map[string]any{
		"staged_files": []any{map[string]any{"token": token, "name": "hello.txt"}},
	})
	if err != nil || len(refs) != 1 || refs[0].Token != token || refs[0].Name != "hello.txt" {
		t.Fatalf("staged refs = %#v %v", refs, err)
	}
}
