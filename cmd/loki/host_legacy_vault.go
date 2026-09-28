package main

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"loki/internal/host/lifecycle"
)

const (
	legacyVaultLivePath          = "/var/lib/loki/runtime"
	legacyVaultImportResultLimit = 16 << 10
	legacyVaultRestoreTimeout    = 2 * time.Minute
)

type hostLegacyVaultImportOptions struct {
	System        bool
	StateRoot     string
	SourceCopy    string
	InterruptJobs bool
}

type legacyVaultMaintenance interface {
	Backup(context.Context, lifecycle.MutationOptions) (lifecycle.BackupRecord, error)
	Restore(context.Context, string, lifecycle.MutationOptions) error
}

type legacyVaultImporter interface {
	ImportLegacyVault(context.Context, string) ([]byte, error)
}

type legacyVaultImportReport struct {
	Migrated            bool   `json:"migrated"`
	Reused              bool   `json:"reused"`
	ExistingDestination bool   `json:"existing_destination"`
	SourceFingerprint   string `json:"source_fingerprint"`
	Profiles            int    `json:"profiles"`
	Secrets             int    `json:"secrets"`
	TargetRevision      uint64 `json:"target_revision"`
}

type hostLegacyVaultImportResult struct {
	Migrated          bool   `json:"migrated"`
	Reused            bool   `json:"reused"`
	SourceFingerprint string `json:"source_fingerprint"`
	Profiles          int    `json:"profiles"`
	Secrets           int    `json:"secrets"`
	TargetRevision    uint64 `json:"target_revision"`
	PreImportBackupID string `json:"pre_import_backup_id"`
}

func parseHostLegacyVaultImportOptions(args []string, stderr io.Writer) (hostLegacyVaultImportOptions, error) {
	flags := flag.NewFlagSet("host import-legacy-vault", flag.ContinueOnError)
	flags.SetOutput(stderr)
	system := flags.Bool("system", false, "operate on the system-wide host installation")
	stateRoot := flags.String("state-root", "", "host lifecycle state root")
	sourceCopy := flags.String("source-copy", "", "private offline copy of the Python v1 vault")
	interrupt := flags.Bool("interrupt-active-jobs", false, "explicitly approve interrupting active jobs")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return hostLegacyVaultImportOptions{}, errors.New("usage: loki host import-legacy-vault --source-copy PATH [--system] [--state-root PATH] [--interrupt-active-jobs]")
	}
	result := hostLegacyVaultImportOptions{
		System: *system, StateRoot: strings.TrimSpace(*stateRoot),
		SourceCopy: strings.TrimSpace(*sourceCopy), InterruptJobs: *interrupt,
	}
	if result.SourceCopy == "" {
		return hostLegacyVaultImportOptions{}, errors.New("--source-copy is required")
	}
	if result.StateRoot != "" && !cleanHostMigrationPath(result.StateRoot) {
		return hostLegacyVaultImportOptions{}, errors.New("--state-root must be a clean absolute non-root path")
	}
	return result, nil
}

func runHostLegacyVaultImport(args []string, stdout, stderr io.Writer) int {
	options, err := parseHostLegacyVaultImportOptions(args, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if options.System && os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "system host legacy vault import requires root")
		return 1
	}
	if options.StateRoot == "" {
		options.StateRoot, err = defaultHostStateRoot(options.System)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if err = validateLegacyVaultSourceCopy(options.SourceCopy, options.StateRoot); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	store, err := lifecycle.OpenFileStore(options.StateRoot)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	backend, err := newHostComposeBackend(store)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	engine := &lifecycle.TransactionEngine{Store: store, Backend: backend, Now: lifecycleTimeNow}
	manager := lifecycle.Manager{
		Store: store, Jobs: backend, Maintainer: engine, Now: lifecycleTimeNow,
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	result, err := importLegacyVaultWithBackup(
		ctx,
		manager,
		backend,
		options.SourceCopy,
		lifecycle.MutationOptions{InterruptActiveJobs: options.InterruptJobs},
	)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err = json.NewEncoder(stdout).Encode(result); err != nil {
		fmt.Fprintln(stderr, "encode legacy vault import result:", err)
		return 1
	}
	return 0
}

func importLegacyVaultWithBackup(
	ctx context.Context,
	maintenance legacyVaultMaintenance,
	importer legacyVaultImporter,
	source string,
	mutationOptions lifecycle.MutationOptions,
) (hostLegacyVaultImportResult, error) {
	if maintenance == nil || importer == nil {
		return hostLegacyVaultImportResult{}, errors.New("legacy vault import adapters are not configured")
	}
	backup, err := maintenance.Backup(ctx, mutationOptions)
	if err != nil {
		return hostLegacyVaultImportResult{}, fmt.Errorf("create pre-import host backup: %w", err)
	}
	if strings.TrimSpace(backup.ID) == "" {
		return hostLegacyVaultImportResult{}, errors.New("pre-import host backup returned no identity")
	}

	raw, importErr := importer.ImportLegacyVault(ctx, source)
	var report legacyVaultImportReport
	if importErr == nil {
		report, importErr = decodeLegacyVaultImportReport(raw)
	}
	if importErr != nil {
		recoveryCtx, cancel := context.WithTimeout(context.Background(), legacyVaultRestoreTimeout)
		defer cancel()
		restoreErr := maintenance.Restore(
			recoveryCtx,
			backup.ID,
			lifecycle.MutationOptions{InterruptActiveJobs: true},
		)
		if restoreErr != nil {
			return hostLegacyVaultImportResult{}, errors.Join(
				fmt.Errorf("legacy vault import failed: %w", importErr),
				fmt.Errorf("restore pre-import host backup %s: %w", backup.ID, restoreErr),
			)
		}
		return hostLegacyVaultImportResult{}, fmt.Errorf(
			"legacy vault import failed; restored pre-import host backup %s: %w",
			backup.ID, importErr,
		)
	}

	return hostLegacyVaultImportResult{
		Migrated: report.Migrated, Reused: report.Reused,
		SourceFingerprint: report.SourceFingerprint,
		Profiles:          report.Profiles, Secrets: report.Secrets,
		TargetRevision: report.TargetRevision, PreImportBackupID: backup.ID,
	}, nil
}

func decodeLegacyVaultImportReport(raw []byte) (legacyVaultImportReport, error) {
	var report legacyVaultImportReport
	if len(raw) == 0 || len(raw) > legacyVaultImportResultLimit {
		return report, errors.New("legacy vault import returned an invalid bounded result")
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.DisallowUnknownFields()
	if err := decoder.Decode(&report); err != nil {
		return report, errors.New("legacy vault import returned invalid JSON")
	}
	var trailing any
	if err := decoder.Decode(&trailing); !errors.Is(err, io.EOF) {
		return report, errors.New("legacy vault import returned trailing data")
	}
	fingerprint, err := hex.DecodeString(report.SourceFingerprint)
	if err != nil || len(fingerprint) != 32 || !report.Migrated || !report.ExistingDestination ||
		report.Profiles < 0 || report.Secrets < 0 || report.TargetRevision == 0 {
		return report, errors.New("legacy vault import result does not match the host migration contract")
	}
	return report, nil
}

func validateLegacyVaultSourceCopy(source, stateRoot string) error {
	source = strings.TrimSpace(source)
	if !cleanHostMigrationPath(source) || strings.Contains(source, ",") {
		return errors.New("legacy vault source must be a clean absolute private path")
	}
	if pathContains(legacyVaultLivePath, source) {
		return errors.New("legacy vault source-copy must not read from the live Python vault")
	}
	if cleanHostMigrationPath(stateRoot) && pathsOverlap(source, stateRoot) {
		return errors.New("legacy vault source-copy must not overlap the active Go lifecycle state")
	}
	resolved, err := filepath.EvalSymlinks(source)
	if err != nil {
		return err
	}
	if resolved != source {
		return errors.New("legacy vault source-copy path must not traverse symlinks")
	}
	info, err := os.Lstat(source)
	if err != nil {
		return err
	}
	if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return errors.New("legacy vault source must be a private real directory")
	}
	return nil
}

func cleanHostMigrationPath(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value &&
		value != string(filepath.Separator) && !strings.ContainsAny(value, "\r\n\x00")
}
