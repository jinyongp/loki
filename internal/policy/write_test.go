package policy

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestCheckedWriteRejectsBeforePublicationAndCleansTemporary(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "file")
	if err := os.WriteFile(target, []byte("external content"), 0600); err != nil {
		t.Fatal(err)
	}
	w, err := New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	conflict := errors.New("changed")
	err = w.AtomicWriteChecked("file", []byte("replacement"), 0600, func(current *os.File) error {
		prepared, err := filepath.Glob(filepath.Join(root, ".loki-write-*"))
		if err != nil || len(prepared) != 1 {
			t.Fatalf("replacement not prepared before check: %v, %v", prepared, err)
		}
		data, err := os.ReadFile(prepared[0])
		if err != nil || string(data) != "replacement" {
			t.Fatalf("prepared bytes: %q, %v", data, err)
		}
		data = make([]byte, 16)
		if _, err := current.Read(data); err != nil || string(data) != "external content" {
			t.Fatalf("current bytes: %q, %v", data, err)
		}
		return conflict
	})
	if !errors.Is(err, conflict) {
		t.Fatalf("check error lost: %v", err)
	}
	data, err := os.ReadFile(target)
	if err != nil || string(data) != "external content" {
		t.Fatalf("external write lost: %q, %v", data, err)
	}
	entries, err := os.ReadDir(root)
	if err != nil || len(entries) != 1 {
		t.Fatalf("temporary not cleaned: %v, %v", entries, err)
	}
}

func TestCheckedWriteDoesNotRecreateMissingParents(t *testing.T) {
	w, err := New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	defer w.Close()
	if err := w.AtomicWriteChecked("gone/file", []byte("replacement"), 0600, func(*os.File) error {
		t.Fatal("missing target checked")
		return nil
	}); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing parent: %v", err)
	}
	if _, err := os.Stat(filepath.Join(w.Root(), "gone")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing parent recreated: %v", err)
	}
}
