package project

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"golang.org/x/sys/unix"
)

func TestMigrationTreeCopiesAndVerifiesBinaryState(t *testing.T) {
	source := filepath.Join(t.TempDir(), "legacy")
	os.MkdirAll(filepath.Join(source, "empty"), 0700)
	os.WriteFile(filepath.Join(source, "taskchampion.sqlite3"), []byte{0, 1, 255, 2, 0}, 0600)
	destination := filepath.Join(t.TempDir(), "copy")
	manifest, err := migrationTree(t.Context(), source, destination)
	if err != nil {
		t.Fatal(err)
	}
	for _, path := range []string{source, destination} {
		actual, err := migrationTree(t.Context(), path, "")
		if err != nil || !reflect.DeepEqual(manifest, actual) {
			t.Fatalf("verification %v %v", actual, err)
		}
	}
	os.WriteFile(filepath.Join(source, "taskchampion.sqlite3"), []byte("changed"), 0600)
	actual, err := migrationTree(t.Context(), source, "")
	if err != nil || reflect.DeepEqual(manifest, actual) {
		t.Fatal("source mutation undetected")
	}
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err = migrationTree(ctx, source, ""); err == nil {
		t.Fatal("canceled migration continued")
	}
}

func TestMigrationTreeRejectsLinksAndSpecialFiles(t *testing.T) {
	for _, kind := range []string{"symlink", "hardlink", "fifo"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			source := filepath.Join(root, "legacy")
			os.Mkdir(source, 0700)
			outside := filepath.Join(root, "outside")
			os.WriteFile(outside, []byte("must stay outside"), 0600)
			path := filepath.Join(source, "entry")
			switch kind {
			case "symlink":
				os.Symlink(outside, path)
			case "hardlink":
				os.Link(outside, path)
			case "fifo":
				unix.Mkfifo(path, 0600)
			}
			if _, err := migrationTree(t.Context(), source, filepath.Join(root, "copy")); err == nil {
				t.Fatal("unsafe entry copied")
			}
			data, _ := os.ReadFile(outside)
			if string(data) != "must stay outside" {
				t.Fatal("outside file changed")
			}
		})
	}
}
