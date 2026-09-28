package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"loki/internal/host/lifecycle"
)

type fakeLegacyVaultMaintenance struct {
	calls          *[]string
	backup         lifecycle.BackupRecord
	backupErr      error
	restoreErr     error
	restoreID      string
	restoreOptions lifecycle.MutationOptions
	restoreCtxErr  error
}

func (m *fakeLegacyVaultMaintenance) Backup(_ context.Context, options lifecycle.MutationOptions) (lifecycle.BackupRecord, error) {
	*m.calls = append(*m.calls, "backup")
	if m.backupErr != nil {
		return lifecycle.BackupRecord{}, m.backupErr
	}
	return m.backup, nil
}

func (m *fakeLegacyVaultMaintenance) Restore(ctx context.Context, id string, options lifecycle.MutationOptions) error {
	*m.calls = append(*m.calls, "restore")
	m.restoreID = id
	m.restoreOptions = options
	m.restoreCtxErr = ctx.Err()
	return m.restoreErr
}

type fakeLegacyVaultImporter struct {
	calls *[]string
	raw   []byte
	err   error
}

func (i *fakeLegacyVaultImporter) ImportLegacyVault(_ context.Context, source string) ([]byte, error) {
	*i.calls = append(*i.calls, "import:"+source)
	return append([]byte(nil), i.raw...), i.err
}

func validLegacyVaultImportJSON(reused bool) []byte {
	return []byte(`{"migrated":true,"reused":` +
		map[bool]string{true: "true", false: "false"}[reused] +
		`,"existing_destination":true,"source_fingerprint":"` +
		strings.Repeat("a", 64) +
		`","profiles":2,"secrets":3,"target_revision":7}`)
}

func TestImportLegacyVaultWithBackupSucceedsAndRetainsBackup(t *testing.T) {
	calls := []string{}
	maintenance := &fakeLegacyVaultMaintenance{
		calls:  &calls,
		backup: lifecycle.BackupRecord{ID: "sha256:" + strings.Repeat("b", 64)},
	}
	importer := &fakeLegacyVaultImporter{
		calls: &calls,
		raw:   validLegacyVaultImportJSON(true),
	}
	result, err := importLegacyVaultWithBackup(
		t.Context(), maintenance, importer, "/var/backups/loki/python-copy", lifecycle.MutationOptions{},
	)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(calls, []string{"backup", "import:/var/backups/loki/python-copy"}) {
		t.Fatalf("call order = %#v", calls)
	}
	if !result.Migrated || !result.Reused || result.Profiles != 2 || result.Secrets != 3 ||
		result.TargetRevision != 7 || result.PreImportBackupID != maintenance.backup.ID {
		t.Fatalf("result = %#v", result)
	}
	if maintenance.restoreID != "" {
		t.Fatal("successful import unexpectedly restored the backup")
	}
}

func TestImportLegacyVaultWithBackupRestoresOnFailureUsingIndependentContext(t *testing.T) {
	calls := []string{}
	maintenance := &fakeLegacyVaultMaintenance{
		calls:  &calls,
		backup: lifecycle.BackupRecord{ID: "sha256:" + strings.Repeat("c", 64)},
	}
	importer := &fakeLegacyVaultImporter{calls: &calls, err: context.Canceled}
	_, err := importLegacyVaultWithBackup(
		t.Context(), maintenance, importer, "/var/backups/loki/python-copy",
		lifecycle.MutationOptions{InterruptActiveJobs: false},
	)
	if err == nil || !strings.Contains(err.Error(), "restored pre-import host backup") {
		t.Fatalf("failure result = %v", err)
	}
	if !reflect.DeepEqual(calls, []string{"backup", "import:/var/backups/loki/python-copy", "restore"}) {
		t.Fatalf("call order = %#v", calls)
	}
	if maintenance.restoreCtxErr != nil {
		t.Fatalf("restore inherited a cancelled context: %v", maintenance.restoreCtxErr)
	}
	if maintenance.restoreID != maintenance.backup.ID || !maintenance.restoreOptions.InterruptActiveJobs {
		t.Fatalf("restore = id %q options %#v", maintenance.restoreID, maintenance.restoreOptions)
	}
}

func TestImportLegacyVaultWithBackupRestoresOnInvalidResult(t *testing.T) {
	calls := []string{}
	maintenance := &fakeLegacyVaultMaintenance{
		calls:  &calls,
		backup: lifecycle.BackupRecord{ID: "sha256:" + strings.Repeat("d", 64)},
	}
	importer := &fakeLegacyVaultImporter{
		calls: &calls,
		raw:   []byte(`{"migrated":true,"secret_value":"must-not-pass"}`),
	}
	_, err := importLegacyVaultWithBackup(
		t.Context(), maintenance, importer, "/var/backups/loki/python-copy", lifecycle.MutationOptions{},
	)
	if err == nil || maintenance.restoreID != maintenance.backup.ID {
		t.Fatalf("invalid import result was not restored: err=%v restore=%q", err, maintenance.restoreID)
	}
}

func TestImportLegacyVaultWithBackupReportsRestoreFailure(t *testing.T) {
	calls := []string{}
	maintenance := &fakeLegacyVaultMaintenance{
		calls:      &calls,
		backup:     lifecycle.BackupRecord{ID: "sha256:" + strings.Repeat("e", 64)},
		restoreErr: errors.New("synthetic restore failure"),
	}
	importer := &fakeLegacyVaultImporter{calls: &calls, err: errors.New("synthetic import failure")}
	_, err := importLegacyVaultWithBackup(
		t.Context(), maintenance, importer, "/var/backups/loki/python-copy", lifecycle.MutationOptions{},
	)
	if err == nil || !strings.Contains(err.Error(), "synthetic import failure") ||
		!strings.Contains(err.Error(), "synthetic restore failure") {
		t.Fatalf("combined failure = %v", err)
	}
}

func TestDecodeLegacyVaultImportReportIsStrictAndBounded(t *testing.T) {
	report, err := decodeLegacyVaultImportReport(validLegacyVaultImportJSON(false))
	if err != nil || report.Reused || report.TargetRevision != 7 {
		t.Fatalf("report=%#v err=%v", report, err)
	}
	for _, raw := range [][]byte{
		nil,
		[]byte(`{"migrated":true,"existing_destination":false,"source_fingerprint":"` + strings.Repeat("a", 64) + `","target_revision":1}`),
		append(validLegacyVaultImportJSON(false), []byte("{}")...),
		make([]byte, legacyVaultImportResultLimit+1),
	} {
		if _, err = decodeLegacyVaultImportReport(raw); err == nil {
			t.Fatalf("invalid report accepted: %q", raw)
		}
	}
}

func TestValidateLegacyVaultSourceCopyRejectsLiveOverlapAndUnsafePaths(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "python-copy")
	if err := os.Mkdir(source, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(source, 0700); err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(root, "go-state")
	if err := os.Mkdir(stateRoot, 0700); err != nil {
		t.Fatal(err)
	}
	if err := validateLegacyVaultSourceCopy(source, stateRoot); err != nil {
		t.Fatalf("private offline copy rejected: %v", err)
	}
	if err := validateLegacyVaultSourceCopy(legacyVaultLivePath, stateRoot); err == nil {
		t.Fatal("live Python vault was accepted")
	}
	if err := validateLegacyVaultSourceCopy(filepath.Join(stateRoot, "copy"), stateRoot); err == nil {
		t.Fatal("Go lifecycle state overlap was accepted")
	}
	public := filepath.Join(root, "public-copy")
	if err := os.Mkdir(public, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(public, 0755); err != nil {
		t.Fatal(err)
	}
	if err := validateLegacyVaultSourceCopy(public, stateRoot); err == nil {
		t.Fatal("non-private source was accepted")
	}
	link := filepath.Join(root, "link-copy")
	if err := os.Symlink(source, link); err != nil {
		t.Fatal(err)
	}
	if err := validateLegacyVaultSourceCopy(link, stateRoot); err == nil {
		t.Fatal("symlink source was accepted")
	}
	realParent := filepath.Join(root, "real-parent")
	if err := os.Mkdir(realParent, 0700); err != nil {
		t.Fatal(err)
	}
	nested := filepath.Join(realParent, "nested-copy")
	if err := os.Mkdir(nested, 0700); err != nil {
		t.Fatal(err)
	}
	parentLink := filepath.Join(root, "parent-link")
	if err := os.Symlink(realParent, parentLink); err != nil {
		t.Fatal(err)
	}
	if err := validateLegacyVaultSourceCopy(filepath.Join(parentLink, "nested-copy"), stateRoot); err == nil {
		t.Fatal("source through a symlinked parent was accepted")
	}
}

func TestParseHostLegacyVaultImportOptions(t *testing.T) {
	var stderr strings.Builder
	options, err := parseHostLegacyVaultImportOptions([]string{
		"--system", "--state-root", "/var/lib/loki/lifecycle",
		"--source-copy", "/var/backups/loki/python-20260928", "--interrupt-active-jobs",
	}, &stderr)
	if err != nil || !options.System || !options.InterruptJobs ||
		options.StateRoot != "/var/lib/loki/lifecycle" ||
		options.SourceCopy != "/var/backups/loki/python-20260928" {
		t.Fatalf("options=%#v err=%v stderr=%q", options, err, stderr.String())
	}
	if _, err = parseHostLegacyVaultImportOptions(nil, &stderr); err == nil {
		t.Fatal("missing source copy was accepted")
	}
}
