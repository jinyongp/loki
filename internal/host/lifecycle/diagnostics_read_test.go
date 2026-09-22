package lifecycle

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiagnosticSnapshotsDoNotCleanInterruptedPublications(t *testing.T) {
	root := privateLifecycleRoot(t)

	backupDir := filepath.Join(root, "backups")
	if err := os.Mkdir(backupDir, 0700); err != nil {
		t.Fatal(err)
	}
	backupTemp := filepath.Join(backupDir, ".loki-private-diagnostic")
	if err := os.WriteFile(backupTemp, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	store, err := OpenFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = store.ReadBackupSnapshot(t.Context()); err == nil ||
		!strings.Contains(err.Error(), "interrupted publication") {
		t.Fatalf("backup snapshot error = %v", err)
	}
	if _, err = os.Stat(backupTemp); err != nil {
		t.Fatalf("diagnostic backup read mutated interrupted publication: %v", err)
	}

	operationDir := filepath.Join(root, "operations")
	if err = os.Mkdir(operationDir, 0700); err != nil {
		t.Fatal(err)
	}
	operationTemp := filepath.Join(operationDir, ".loki-private-diagnostic")
	if err = os.WriteFile(operationTemp, []byte("partial"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = ReadOperationSnapshot(root); err == nil ||
		!strings.Contains(err.Error(), "interrupted publication") {
		t.Fatalf("operation snapshot error = %v", err)
	}
	if _, err = os.Stat(operationTemp); err != nil {
		t.Fatalf("diagnostic operation read mutated interrupted publication: %v", err)
	}
}
