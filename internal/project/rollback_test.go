package project

import (
	"os"
	"path/filepath"
	"testing"
)

func TestLegacyRollbackPreservesBothGenerations(t *testing.T) {
	store, repo, _ := fixture(t)
	source := legacyFixture(t, repo, true)
	if _, err := store.MigrateLegacy(t.Context(), repo); err != nil {
		t.Fatal(err)
	}
	id, err := store.Resolve(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(id.StateDirectory, "taskwarrior", "data", "taskchampion.sqlite3"), []byte("new central changes"), 0600)
	result, err := store.RollbackLegacy(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	backup := result["central_state_archive"].(string)
	for path, want := range map[string]string{filepath.Join(source, "data", "taskchampion.sqlite3"): "synthetic database bytes", filepath.Join(backup, "taskwarrior", "data", "taskchampion.sqlite3"): "new central changes", filepath.Join(source, "verification", "latest.json"): `{"status":"passed"}`} {
		raw, err := os.ReadFile(path)
		if err != nil || string(raw) != want {
			t.Fatalf("rollback data %s %q %v", path, raw, err)
		}
	}
	if _, err = os.Stat(id.StateDirectory); !os.IsNotExist(err) {
		t.Fatal("central state remains active")
	}
	if _, err = store.RollbackLegacy(t.Context(), repo); err != nil {
		t.Fatal("idempotent rollback", err)
	}
	if _, err = store.MigrateLegacy(t.Context(), repo); err != nil {
		t.Fatal("second migration", err)
	}
	second, err := store.RollbackLegacy(t.Context(), repo)
	if err != nil || second["central_state_archive"] == backup {
		t.Fatal("second generation reused backup", second, err)
	}
}

func TestLegacyRollbackResumesWithoutReplacingUserFiles(t *testing.T) {
	store, repo, _ := fixture(t)
	source := legacyFixture(t, repo, false)
	if _, err := store.MigrateLegacy(t.Context(), repo); err != nil {
		t.Fatal(err)
	}
	id, err := store.Resolve(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	record, err := readMigration(id.StateDirectory, id)
	if err != nil {
		t.Fatal(err)
	}
	backupRoot := filepath.Join(store.StateRoot, "rollback")
	os.Mkdir(backupRoot, 0700)
	backup := filepath.Join(backupRoot, id.ProjectID+"-"+record.Generation)
	record.RollbackStarted = true
	writeJSON(filepath.Join(id.StateDirectory, "migration.json"), record, true)
	if err = os.Rename(id.StateDirectory, backup); err != nil {
		t.Fatal(err)
	}
	os.Mkdir(source, 0700)
	if err = os.Rename(filepath.Join(backup, "legacy", "data"), filepath.Join(source, "data")); err != nil {
		t.Fatal(err)
	}
	os.WriteFile(filepath.Join(source, "taskrc"), []byte("user replacement"), 0600)
	if _, err = store.RollbackLegacy(t.Context(), repo); err == nil {
		t.Fatal("user file overwritten")
	}
	raw, _ := os.ReadFile(filepath.Join(source, "taskrc"))
	if string(raw) != "user replacement" {
		t.Fatal("user file changed")
	}
	os.Rename(filepath.Join(source, "taskrc"), filepath.Join(source, "taskrc.user"))
	if _, err = store.RollbackLegacy(t.Context(), repo); err != nil {
		t.Fatal("resume", err)
	}
	if _, err = os.Stat(filepath.Join(source, "items", "existing-test-workstream", "plan.md")); err != nil {
		t.Fatal("original not restored")
	}
}
