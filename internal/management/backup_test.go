package management

import (
	"context"
	"loki/internal/tools"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestBackupRestorePreservesDataAndEmptyComponents(t *testing.T) {
	s := Store{Root: t.TempDir()}
	state, err := s.Load()
	if err != nil {
		t.Fatal(err)
	}
	installation := ownedFixture(t, s, "browser")
	state.Installed["browser"] = installation
	state.Config.Tools = []tools.Selection{{ID: "browser", Enabled: true}}
	if err := s.Save(state); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(s.Root, "data")
	if err := os.MkdirAll(data, 0700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(data, "example")
	if err := os.WriteFile(path, []byte("original"), 0600); err != nil {
		t.Fatal(err)
	}
	backup, err := s.Backup(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("changed"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(s.Root, "auth"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(s.Root, "auth", "new"), []byte("new"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.RestoreBackup(context.Background(), backup.ID, nil); err != nil {
		t.Fatal(err)
	}
	actual, err := os.ReadFile(path)
	if err != nil || string(actual) != "original" {
		t.Fatalf("data not restored: %q %v", actual, err)
	}
	if _, err := os.Stat(filepath.Join(s.Root, "auth", "new")); !os.IsNotExist(err) {
		t.Fatal("new credentials survived exact restore")
	}
	after, err := s.Load()
	if err != nil || snapshotDigest(after) != snapshotDigest(state) {
		t.Fatalf("program selection not restored: %v", err)
	}
	// Corruption must fail before changing current user data.
	if err := os.WriteFile(filepath.Join(s.Root, "backups", backup.ID, "data", "example"), []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := s.RestoreBackup(context.Background(), backup.ID, nil); err == nil {
		t.Fatal("corrupt backup accepted")
	}
	actual, _ = os.ReadFile(path)
	if string(actual) != "original" {
		t.Fatal("corrupt restore changed current data")
	}
}

func TestBackupTreeDoesNotFollowSymlinks(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("symlink creation requires native developer permissions")
	}
	source := t.TempDir()
	outside := filepath.Join(t.TempDir(), "secret")
	if err := os.WriteFile(outside, []byte("external"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(source, "link")); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(t.TempDir(), "copy")
	if _, err := copyBackupTree(t.Context(), source, target); err != nil {
		t.Fatal(err)
	}
	link, err := os.Readlink(filepath.Join(target, "link"))
	if err != nil || link != outside {
		t.Fatalf("symlink not retained: %q %v", link, err)
	}
}

func TestPurgeDataRejectsLinkedNamespaces(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("native symlinks require developer permission")
	}
	s := Store{Root: t.TempDir()}
	state, _ := s.Load()
	if err := s.Save(state); err != nil {
		t.Fatal(err)
	}
	outside := t.TempDir()
	path := filepath.Join(outside, "user-file")
	if err := os.WriteFile(path, []byte("preserve"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(outside, filepath.Join(s.Root, "data")); err != nil {
		t.Fatal(err)
	}
	if err := s.PurgeData(t.Context()); err == nil {
		t.Fatal("linked namespace purged")
	}
	raw, err := os.ReadFile(path)
	if err != nil || string(raw) != "preserve" {
		t.Fatal("foreign data changed")
	}
}

func TestRestoreResumesPublishedTreeAndCleansPreviousData(t *testing.T) {
	s := Store{Root: t.TempDir()}
	state, _ := s.Load()
	if err := s.Save(state); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(s.Root, "data")
	if err := os.MkdirAll(data, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(data, "value"), []byte("backup"), 0600); err != nil {
		t.Fatal(err)
	}
	backup, err := s.Backup(t.Context(), nil)
	if err != nil {
		t.Fatal(err)
	}
	previous := data + ".loki-previous-" + backup.ID
	if err := os.Rename(data, previous); err != nil {
		t.Fatal(err)
	}
	if _, err := copyBackupTree(t.Context(), filepath.Join(s.Root, "backups", backup.ID, "data"), data); err != nil {
		t.Fatal(err)
	}
	if err := atomicJSON(filepath.Join(s.Root, "restore.json"), restoreJournal{Schema: 1, ID: backup.ID, Phase: "prepared"}); err != nil {
		t.Fatal(err)
	}
	if err := s.RestoreBackup(t.Context(), backup.ID, nil); err != nil {
		t.Fatal(err)
	}
	if pending, err := s.RestorePending(backup.ID); pending || err != nil {
		t.Fatalf("restore still pending: %v %v", pending, err)
	}
	if _, err := os.Stat(previous); !os.IsNotExist(err) {
		t.Fatalf("previous data remain after recovery: %v", err)
	}
}
