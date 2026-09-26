package lifecycle

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"loki/internal/platform/safeio"
)

const DefaultMCPPort = 18765

type InstallationState struct {
	Scope        string `json:"scope"`
	Workspace    string `json:"workspace"`
	DockerAccess string `json:"docker_access,omitempty"`
	MCPPort      int    `json:"mcp_port,omitempty"`
}

func (s InstallationState) EffectiveMCPPort() int {
	if s.MCPPort == 0 {
		return DefaultMCPPort
	}
	return s.MCPPort
}

func (s InstallationState) Normalized() InstallationState {
	s.MCPPort = s.EffectiveMCPPort()
	return s
}

func (s InstallationState) Equivalent(other InstallationState) bool {
	return s.Normalized() == other.Normalized()
}

func (s InstallationState) Valid() bool {
	if s.Scope != "user" && s.Scope != "system" {
		return false
	}
	if s.DockerAccess != "" && s.DockerAccess != "direct" && s.DockerAccess != "sudo" {
		return false
	}
	if s.MCPPort != 0 && (s.MCPPort < 1024 || s.MCPPort > 65535) {
		return false
	}
	return filepath.IsAbs(s.Workspace) &&
		filepath.Clean(s.Workspace) == s.Workspace &&
		s.Workspace != string(filepath.Separator) &&
		!strings.ContainsRune(s.Workspace, 0)
}

type BackupCoverage struct {
	RuntimeState           bool     `json:"runtime_state"`
	ConfigState            bool     `json:"config_state"`
	HostState              bool     `json:"host_state"`
	WorkspacePreserved     bool     `json:"workspace_preserved"`
	OptionalComponentState []string `json:"optional_component_state,omitempty"`
	ExternalReferences     []string `json:"external_references,omitempty"`
}

type RuntimeSnapshot struct {
	Ref      string         `json:"ref"`
	Coverage BackupCoverage `json:"coverage"`
}

type BackupRecord struct {
	ID           string             `json:"id"`
	Reason       OperationKind      `json:"reason"`
	CreatedAt    time.Time          `json:"created_at"`
	Installed    *Generation        `json:"installed,omitempty"`
	Host         HostState          `json:"host"`
	Installation *InstallationState `json:"installation,omitempty"`
	RuntimeRef   string             `json:"runtime_ref"`
	Coverage     BackupCoverage     `json:"coverage"`
}

func NewBackupRecord(reason OperationKind, snapshot Snapshot, runtime RuntimeSnapshot, now time.Time) (BackupRecord, error) {
	runtime.Ref = strings.TrimSpace(runtime.Ref)
	if !reason.Valid() || runtime.Ref == "" || len(runtime.Ref) > 4096 || strings.ContainsAny(runtime.Ref, "\r\n\x00") {
		return BackupRecord{}, errors.New("host lifecycle backup identity is invalid")
	}
	host, err := snapshot.Host.normalized()
	if err != nil {
		return BackupRecord{}, err
	}
	if snapshot.Installed != nil && !snapshot.Installed.Valid() {
		return BackupRecord{}, errors.New("host lifecycle backup installed generation is invalid")
	}
	if snapshot.Installation != nil && !snapshot.Installation.Valid() {
		return BackupRecord{}, errors.New("host lifecycle backup installation state is invalid")
	}
	coverage, err := normalizeBackupCoverage(runtime.Coverage, host.EnabledComponents)
	if err != nil {
		return BackupRecord{}, err
	}
	now = now.UTC()
	if now.IsZero() {
		return BackupRecord{}, errors.New("host lifecycle backup timestamp is required")
	}
	record := BackupRecord{
		Reason: reason, CreatedAt: now, Installed: snapshot.Installed,
		Host: host, Installation: snapshot.Installation, RuntimeRef: runtime.Ref, Coverage: coverage,
	}
	raw, err := json.Marshal(struct {
		Version      int                `json:"version"`
		Reason       OperationKind      `json:"reason"`
		CreatedAt    time.Time          `json:"created_at"`
		Installed    *Generation        `json:"installed,omitempty"`
		Host         HostState          `json:"host"`
		Installation *InstallationState `json:"installation,omitempty"`
		RuntimeRef   string             `json:"runtime_ref"`
		Coverage     BackupCoverage     `json:"coverage"`
	}{
		Version: 1, Reason: record.Reason, CreatedAt: record.CreatedAt, Installed: record.Installed,
		Host: record.Host, Installation: record.Installation, RuntimeRef: record.RuntimeRef, Coverage: record.Coverage,
	})
	if err != nil {
		return BackupRecord{}, err
	}
	sum := sha256.Sum256(raw)
	record.ID = "sha256:" + hex.EncodeToString(sum[:])
	return record, nil
}

func (b BackupRecord) Valid() bool {
	if !digestPattern.MatchString(b.ID) {
		return false
	}
	copy := b
	copy.ID = ""
	expected, err := NewBackupRecord(copy.Reason, Snapshot{
		Installed: copy.Installed, Host: copy.Host, Installation: copy.Installation,
	}, RuntimeSnapshot{Ref: copy.RuntimeRef, Coverage: copy.Coverage}, copy.CreatedAt)
	return err == nil && expected.ID == b.ID
}

func normalizeBackupCoverage(coverage BackupCoverage, enabled []string) (BackupCoverage, error) {
	if !coverage.RuntimeState || !coverage.ConfigState || !coverage.HostState || !coverage.WorkspacePreserved {
		return BackupCoverage{}, errors.New("host lifecycle backup coverage is incomplete")
	}
	optional := append([]string(nil), coverage.OptionalComponentState...)
	for index := range optional {
		optional[index] = strings.TrimSpace(optional[index])
		if !releaseNamePattern.MatchString(optional[index]) {
			return BackupCoverage{}, errors.New("host lifecycle backup optional-component coverage is invalid")
		}
	}
	sort.Strings(optional)
	optional = compactStrings(optional)
	if !coversOptionalState(optional, enabled) {
		return BackupCoverage{}, errors.New("host lifecycle backup omits enabled optional-component state")
	}
	external := append([]string(nil), coverage.ExternalReferences...)
	for index := range external {
		external[index] = strings.TrimSpace(external[index])
		if external[index] == "" || len(external[index]) > 256 || strings.ContainsAny(external[index], "\r\n\x00") {
			return BackupCoverage{}, errors.New("host lifecycle backup external reference is invalid")
		}
	}
	sort.Strings(external)
	external = compactStrings(external)
	coverage.OptionalComponentState = optional
	coverage.ExternalReferences = external
	return coverage, nil
}

func compactStrings(values []string) []string {
	if len(values) < 2 {
		return values
	}
	result := values[:1]
	for _, value := range values[1:] {
		if value != result[len(result)-1] {
			result = append(result, value)
		}
	}
	return result
}

type TransactionBackend interface {
	Snapshot(context.Context, OperationKind, Snapshot) (RuntimeSnapshot, error)
	Activate(context.Context, Generation, InstallationState) error
	SetComponent(context.Context, Generation, string, bool) error
	Migrate(context.Context, []MigrationStep) error
	Restart(context.Context) error
	Health(context.Context) error
	Restore(context.Context, string) error
	Stop(context.Context) error
	VerifyStopped(context.Context) error
}

type TransactionEngine struct {
	Store   *FileStore
	Backend TransactionBackend
	Now     func() time.Time
}

func (e *TransactionEngine) now() time.Time {
	if e != nil && e.Now != nil {
		return e.Now().UTC()
	}
	return time.Now().UTC()
}

func (e *TransactionEngine) Apply(ctx context.Context, request ApplyRequest) (ApplyResult, error) {
	if e == nil || e.Store == nil || e.Backend == nil {
		return ApplyResult{}, errors.New("host lifecycle transaction engine is not configured")
	}
	if err := ctx.Err(); err != nil {
		return ApplyResult{}, err
	}
	snapshot, err := e.Store.Snapshot(ctx)
	if err != nil {
		return ApplyResult{}, err
	}
	if snapshot.Prepared == nil || snapshot.Available == nil || snapshot.Prepared.ID != request.Plan.ID ||
		snapshot.Available.ID != request.Plan.CandidateGenerationID {
		return ApplyResult{}, errors.New("host lifecycle apply request is stale")
	}
	if snapshot.Installation == nil || !snapshot.Installation.Valid() {
		return ApplyResult{}, errors.New("host lifecycle installation state is missing")
	}
	lock, journal, err := e.openJournal(ctx)
	if err != nil {
		return ApplyResult{}, err
	}
	defer lock.Close()
	if err = e.recoverInterrupted(ctx, journal); err != nil {
		return ApplyResult{}, err
	}

	kind := OperationApply
	if snapshot.Installed == nil {
		kind = OperationInstall
	}
	record, err := journal.Begin(kind, request.Plan, e.now())
	if err != nil {
		return ApplyResult{}, err
	}
	backup, err := e.captureBackup(ctx, journal, record, snapshot)
	if err != nil {
		return ApplyResult{}, e.recoverFailure(ctx, journal, record.ID, nil, err)
	}
	record, _ = journal.require(record.ID)

	if err = e.Backend.Activate(ctx, *snapshot.Available, *snapshot.Installation); err != nil {
		return ApplyResult{}, e.recoverFailure(ctx, journal, record.ID, &backup, err)
	}
	if _, err = journal.Advance(record.ID, PhaseSwitch, e.now()); err != nil {
		return ApplyResult{}, e.recoverFailure(ctx, journal, record.ID, &backup, err)
	}
	if err = e.Backend.Migrate(ctx, request.Plan.Impact.Migration); err != nil {
		return ApplyResult{}, e.recoverFailure(ctx, journal, record.ID, &backup, err)
	}
	if _, err = journal.Advance(record.ID, PhaseMigrate, e.now()); err != nil {
		return ApplyResult{}, e.recoverFailure(ctx, journal, record.ID, &backup, err)
	}
	if err = e.Backend.Restart(ctx); err != nil {
		return ApplyResult{}, e.recoverFailure(ctx, journal, record.ID, &backup, err)
	}
	if _, err = journal.Advance(record.ID, PhaseRestart, e.now()); err != nil {
		return ApplyResult{}, e.recoverFailure(ctx, journal, record.ID, &backup, err)
	}
	if err = e.Backend.Health(ctx); err != nil {
		return ApplyResult{}, e.recoverFailure(ctx, journal, record.ID, &backup, err)
	}
	if _, err = journal.Advance(record.ID, PhaseHealth, e.now()); err != nil {
		return ApplyResult{}, e.recoverFailure(ctx, journal, record.ID, &backup, err)
	}
	if err = e.Store.CommitGeneration(ctx, *snapshot.Available, e.now()); err != nil {
		return ApplyResult{}, e.recoverFailure(ctx, journal, record.ID, &backup, err)
	}
	if _, err = journal.MarkSucceeded(record.ID, e.now()); err != nil {
		return ApplyResult{}, err
	}
	return ApplyResult{PlanID: request.Plan.ID, InterruptedJobs: append([]string(nil), request.ActiveJobs...)}, nil
}

func (e *TransactionEngine) SetComponent(ctx context.Context, name string, enabled bool) error {
	if e == nil || e.Store == nil || e.Backend == nil {
		return errors.New("host lifecycle transaction engine is not configured")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	lock, journal, err := e.openJournal(ctx)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = e.recoverInterrupted(ctx, journal); err != nil {
		return err
	}
	snapshot, err := e.Store.Snapshot(ctx)
	if err != nil {
		return err
	}
	if snapshot.Installed == nil || snapshot.Installation == nil {
		return errors.New("host lifecycle component change requires an installed release")
	}
	target, changed, err := optionalComponentTarget(*snapshot.Installed, snapshot.Host.EnabledComponents, name, enabled)
	if err != nil {
		return err
	}
	if !changed {
		return nil
	}
	plan, err := maintenancePlan(snapshot, snapshot.Installed.ID, e.now())
	if err != nil {
		return err
	}
	kind := OperationDisableComponent
	if enabled {
		kind = OperationEnableComponent
	}
	record, err := journal.Begin(kind, plan, e.now())
	if err != nil {
		return err
	}
	backup, err := e.captureBackup(ctx, journal, record, snapshot)
	if err != nil {
		return e.recoverFailure(ctx, journal, record.ID, nil, err)
	}
	if err = e.Backend.SetComponent(ctx, *snapshot.Installed, name, enabled); err != nil {
		return e.recoverFailure(ctx, journal, record.ID, &backup, err)
	}
	if _, err = journal.Advance(record.ID, PhaseSwitch, e.now()); err != nil {
		return e.recoverFailure(ctx, journal, record.ID, &backup, err)
	}
	if _, err = journal.Advance(record.ID, PhaseMigrate, e.now()); err != nil {
		return e.recoverFailure(ctx, journal, record.ID, &backup, err)
	}
	if err = e.Backend.Restart(ctx); err != nil {
		return e.recoverFailure(ctx, journal, record.ID, &backup, err)
	}
	if _, err = journal.Advance(record.ID, PhaseRestart, e.now()); err != nil {
		return e.recoverFailure(ctx, journal, record.ID, &backup, err)
	}
	if err = e.Backend.Health(ctx); err != nil {
		return e.recoverFailure(ctx, journal, record.ID, &backup, err)
	}
	if _, err = journal.Advance(record.ID, PhaseHealth, e.now()); err != nil {
		return e.recoverFailure(ctx, journal, record.ID, &backup, err)
	}
	if err = e.Store.CommitComponents(ctx, target, e.now()); err != nil {
		return e.recoverFailure(ctx, journal, record.ID, &backup, err)
	}
	_, err = journal.MarkSucceeded(record.ID, e.now())
	return err
}

func (e *TransactionEngine) Backup(ctx context.Context) (BackupRecord, error) {
	if e == nil || e.Store == nil || e.Backend == nil {
		return BackupRecord{}, errors.New("host lifecycle transaction engine is not configured")
	}
	snapshot, err := e.Store.Snapshot(ctx)
	if err != nil {
		return BackupRecord{}, err
	}
	if snapshot.Installed == nil || snapshot.Installation == nil {
		return BackupRecord{}, errors.New("host lifecycle backup requires an installed release")
	}
	lock, journal, err := e.openJournal(ctx)
	if err != nil {
		return BackupRecord{}, err
	}
	defer lock.Close()
	if err = e.recoverInterrupted(ctx, journal); err != nil {
		return BackupRecord{}, err
	}
	plan, err := maintenancePlan(snapshot, snapshot.Installed.ID, e.now())
	if err != nil {
		return BackupRecord{}, err
	}
	record, err := journal.Begin(OperationBackup, plan, e.now())
	if err != nil {
		return BackupRecord{}, err
	}
	backup, err := e.captureBackup(ctx, journal, record, snapshot)
	if err != nil {
		return BackupRecord{}, e.recoverFailure(ctx, journal, record.ID, nil, err)
	}
	if err = e.Backend.Restart(ctx); err != nil {
		return BackupRecord{}, e.recoverFailure(ctx, journal, record.ID, &backup, err)
	}
	if err = e.Backend.Health(ctx); err != nil {
		return BackupRecord{}, e.recoverFailure(ctx, journal, record.ID, &backup, err)
	}
	if _, err = journal.MarkSucceeded(record.ID, e.now()); err != nil {
		return BackupRecord{}, err
	}
	return backup, nil
}

func (e *TransactionEngine) Restore(ctx context.Context, backupID string) error {
	if e == nil || e.Store == nil || e.Backend == nil {
		return errors.New("host lifecycle transaction engine is not configured")
	}
	target, err := e.Store.LoadBackup(ctx, backupID)
	if err != nil {
		return err
	}
	if target.Installed == nil {
		return errors.New("host lifecycle restore target has no installed release")
	}
	return e.restoreTo(ctx, OperationRestore, target)
}

func (e *TransactionEngine) Rollback(ctx context.Context) error {
	if e == nil || e.Store == nil || e.Backend == nil {
		return errors.New("host lifecycle transaction engine is not configured")
	}
	backups, err := e.Store.ListBackups(ctx)
	if err != nil {
		return err
	}
	sort.Slice(backups, func(i, j int) bool {
		if backups[i].CreatedAt.Equal(backups[j].CreatedAt) {
			return backups[i].ID > backups[j].ID
		}
		return backups[i].CreatedAt.After(backups[j].CreatedAt)
	})
	for _, backup := range backups {
		if backup.Installed == nil {
			continue
		}
		switch backup.Reason {
		case OperationApply, OperationInstall, OperationRestore, OperationRollback, OperationUninstall:
			return e.restoreTo(ctx, OperationRollback, backup)
		}
	}
	return errors.New("host lifecycle rollback has no recovery backup")
}

func (e *TransactionEngine) Uninstall(ctx context.Context) error {
	if e == nil || e.Store == nil || e.Backend == nil {
		return errors.New("host lifecycle transaction engine is not configured")
	}
	snapshot, err := e.Store.Snapshot(ctx)
	if err != nil {
		return err
	}
	if snapshot.Installed == nil || snapshot.Installation == nil {
		return errors.New("host lifecycle uninstall requires an installed release")
	}
	lock, journal, err := e.openJournal(ctx)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = e.recoverInterrupted(ctx, journal); err != nil {
		return err
	}
	plan, err := maintenancePlan(snapshot, snapshot.Installed.ID, e.now())
	if err != nil {
		return err
	}
	record, err := journal.Begin(OperationUninstall, plan, e.now())
	if err != nil {
		return err
	}
	backup, err := e.captureBackup(ctx, journal, record, snapshot)
	if err != nil {
		return e.recoverFailure(ctx, journal, record.ID, nil, err)
	}
	if err = e.Backend.Stop(ctx); err != nil {
		return e.recoverFailure(ctx, journal, record.ID, &backup, err)
	}
	if _, err = journal.Advance(record.ID, PhaseSwitch, e.now()); err != nil {
		return e.recoverFailure(ctx, journal, record.ID, &backup, err)
	}
	if err = e.Store.CommitUninstall(ctx, e.now()); err != nil {
		return e.recoverFailure(ctx, journal, record.ID, &backup, err)
	}
	if _, err = journal.Advance(record.ID, PhaseMigrate, e.now()); err != nil {
		return e.recoverFailure(ctx, journal, record.ID, &backup, err)
	}
	if _, err = journal.Advance(record.ID, PhaseRestart, e.now()); err != nil {
		return e.recoverFailure(ctx, journal, record.ID, &backup, err)
	}
	if err = e.Backend.VerifyStopped(ctx); err != nil {
		return e.recoverFailure(ctx, journal, record.ID, &backup, err)
	}
	if _, err = journal.Advance(record.ID, PhaseHealth, e.now()); err != nil {
		return e.recoverFailure(ctx, journal, record.ID, &backup, err)
	}
	_, err = journal.MarkSucceeded(record.ID, e.now())
	return err
}

func (e *TransactionEngine) restoreTo(ctx context.Context, kind OperationKind, target BackupRecord) error {
	snapshot, err := e.Store.Snapshot(ctx)
	if err != nil {
		return err
	}
	lock, journal, err := e.openJournal(ctx)
	if err != nil {
		return err
	}
	defer lock.Close()
	if err = e.recoverInterrupted(ctx, journal); err != nil {
		return err
	}
	plan, err := maintenancePlan(snapshot, target.Installed.ID, e.now())
	if err != nil {
		return err
	}
	record, err := journal.Begin(kind, plan, e.now())
	if err != nil {
		return err
	}
	safety, err := e.captureBackup(ctx, journal, record, snapshot)
	if err != nil {
		return e.recoverFailure(ctx, journal, record.ID, nil, err)
	}
	if err = e.Backend.Restore(ctx, target.RuntimeRef); err != nil {
		return e.recoverFailure(ctx, journal, record.ID, &safety, err)
	}
	if _, err = journal.Advance(record.ID, PhaseSwitch, e.now()); err != nil {
		return e.recoverFailure(ctx, journal, record.ID, &safety, err)
	}
	if err = e.Store.RestoreBackup(ctx, target); err != nil {
		return e.recoverFailure(ctx, journal, record.ID, &safety, err)
	}
	if _, err = journal.Advance(record.ID, PhaseMigrate, e.now()); err != nil {
		return e.recoverFailure(ctx, journal, record.ID, &safety, err)
	}
	if err = e.Backend.Restart(ctx); err != nil {
		return e.recoverFailure(ctx, journal, record.ID, &safety, err)
	}
	if _, err = journal.Advance(record.ID, PhaseRestart, e.now()); err != nil {
		return e.recoverFailure(ctx, journal, record.ID, &safety, err)
	}
	if err = e.Backend.Health(ctx); err != nil {
		return e.recoverFailure(ctx, journal, record.ID, &safety, err)
	}
	if _, err = journal.Advance(record.ID, PhaseHealth, e.now()); err != nil {
		return e.recoverFailure(ctx, journal, record.ID, &safety, err)
	}
	_, err = journal.MarkSucceeded(record.ID, e.now())
	return err
}

func (e *TransactionEngine) captureBackup(ctx context.Context, journal *OperationJournal, record OperationRecord, snapshot Snapshot) (BackupRecord, error) {
	if _, ok := e.Backend.(RuntimeSnapshotStorage); ok {
		if _, err := e.collectStorageLocked(ctx, journal, nil); err != nil {
			return BackupRecord{}, err
		}
	}
	runtimeSnapshot, err := e.Backend.Snapshot(ctx, record.Kind, snapshot)
	if err != nil {
		return BackupRecord{}, err
	}
	backup, err := NewBackupRecord(record.Kind, snapshot, runtimeSnapshot, e.now())
	if err != nil {
		if storage, ok := e.Backend.(RuntimeSnapshotStorage); ok {
			err = errors.Join(err, storage.DeleteRuntimeSnapshot(ctx, runtimeSnapshot.Ref))
		}
		return BackupRecord{}, err
	}
	if err = e.Store.SaveBackup(ctx, backup); err != nil {
		if storage, ok := e.Backend.(RuntimeSnapshotStorage); ok {
			err = errors.Join(err, storage.DeleteRuntimeSnapshot(ctx, runtimeSnapshot.Ref))
		}
		return BackupRecord{}, err
	}
	if _, ok := e.Backend.(RuntimeSnapshotStorage); ok {
		if _, err = e.collectStorageLocked(ctx, journal, map[string]bool{backup.ID: true}); err != nil {
			return BackupRecord{}, errors.Join(err, e.discardBackup(ctx, backup))
		}
	}
	if _, err = journal.RecordSnapshot(record.ID, backup.ID, e.now()); err != nil {
		return BackupRecord{}, errors.Join(err, e.discardBackup(ctx, backup))
	}
	return backup, nil
}

func (e *TransactionEngine) openJournal(ctx context.Context) (*OperationLock, *OperationJournal, error) {
	if err := ctx.Err(); err != nil {
		return nil, nil, err
	}
	lock, err := AcquireOperationLock(e.Store.Root)
	if err != nil {
		return nil, nil, err
	}
	journal, err := OpenOperationJournal(e.Store.Root, lock, OperationJournalOptions{})
	if err != nil {
		_ = lock.Close()
		return nil, nil, err
	}
	return lock, journal, nil
}

func (e *TransactionEngine) restoreRecoveryBackup(ctx context.Context, backup BackupRecord) error {
	if err := e.Backend.Restore(ctx, backup.RuntimeRef); err != nil {
		return err
	}
	if err := e.Store.RestoreBackup(ctx, backup); err != nil {
		return err
	}
	if backup.Installed == nil {
		return e.Backend.VerifyStopped(ctx)
	}
	if err := e.Backend.Restart(ctx); err != nil {
		return err
	}
	return e.Backend.Health(ctx)
}

func (e *TransactionEngine) recoverInterrupted(ctx context.Context, journal *OperationJournal) error {
	_, found, err := journal.RecoverInterrupted(ctx, func(ctx context.Context, record OperationRecord) error {
		if record.RecoveryBackupID == "" {
			return nil
		}
		backup, loadErr := e.Store.LoadBackup(ctx, record.RecoveryBackupID)
		if loadErr != nil {
			return loadErr
		}
		return e.restoreRecoveryBackup(ctx, backup)
	}, e.now())
	if !found {
		return err
	}
	return err
}

func (e *TransactionEngine) recoverFailure(ctx context.Context, journal *OperationJournal, operationID string, backup *BackupRecord, original error) error {
	if original == nil {
		return nil
	}
	record, err := journal.BeginRecovery(operationID, original, e.now())
	if err != nil {
		return errors.Join(original, err)
	}
	if backup == nil && record.RecoveryBackupID != "" {
		loaded, loadErr := e.Store.LoadBackup(ctx, record.RecoveryBackupID)
		if loadErr != nil {
			_, _ = journal.MarkRecoveryFailed(operationID, loadErr, e.now())
			return &RecoveryFailedError{Original: original.Error(), Recovery: loadErr.Error()}
		}
		backup = &loaded
	}
	if backup == nil {
		if _, err = journal.MarkRolledBack(operationID, e.now()); err != nil {
			return errors.Join(original, err)
		}
		return original
	}
	recoveryErr := e.restoreRecoveryBackup(ctx, *backup)
	if recoveryErr != nil {
		_, persistErr := journal.MarkRecoveryFailed(operationID, recoveryErr, e.now())
		if persistErr != nil {
			return errors.Join(original, recoveryErr, persistErr)
		}
		return &RecoveryFailedError{Original: original.Error(), Recovery: recoveryErr.Error()}
	}
	if _, err = journal.MarkRolledBack(operationID, e.now()); err != nil {
		return errors.Join(original, err)
	}
	return original
}

func maintenancePlan(snapshot Snapshot, candidateID string, now time.Time) (PreparedPlan, error) {
	if snapshot.Host.Revision == "" || !digestPattern.MatchString(candidateID) {
		return PreparedPlan{}, errors.New("host lifecycle maintenance plan identity is invalid")
	}
	activeID := ""
	if snapshot.Installed != nil {
		activeID = snapshot.Installed.ID
	}
	plan := PreparedPlan{
		ObservedHostRevision:  snapshot.Host.Revision,
		ActiveGenerationID:    activeID,
		CandidateGenerationID: candidateID,
		Impact:                Impact{RollbackCompatible: true},
		PreparedAt:            now.UTC().Truncate(time.Second),
	}
	id, err := preparedPlanID(plan)
	if err != nil {
		return PreparedPlan{}, err
	}
	plan.ID = id
	return plan, nil
}

func EnsureFileStore(root string) (*FileStore, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || root == string(filepath.Separator) || strings.ContainsRune(root, 0) {
		return nil, errors.New("host lifecycle state root must be a clean absolute non-root path")
	}
	if err := os.MkdirAll(root, 0700); err != nil {
		return nil, err
	}
	return OpenFileStore(root)
}

func (s *FileStore) InitializeInstall(ctx context.Context, candidate Generation, installation InstallationState, now time.Time) error {
	if s == nil || !candidate.Valid() || !installation.Valid() {
		return errors.New("host lifecycle install initialization is invalid")
	}
	installation = installation.Normalized()
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := os.Stat(s.path("host.json")); err == nil {
		snapshot, snapshotErr := s.Snapshot(ctx)
		if snapshotErr != nil {
			return snapshotErr
		}
		if snapshot.Installed == nil && snapshot.Available != nil && snapshot.Available.ID == candidate.ID &&
			snapshot.Installation != nil && snapshot.Installation.Equivalent(installation) {
			return nil
		}
		if snapshot.Installed != nil || snapshot.Available != nil || snapshot.Host.ActiveGenerationID != "" {
			return errors.New("host lifecycle state is already initialized for another installation")
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return err
	}
	revision := lifecycleRevision("", candidate.ID, installation.Scope, installation.Workspace, installation.MCPPort, now)
	host := HostState{
		ConfigSchema: candidate.Spec.ConfigSchema, PolicySchema: candidate.Spec.PolicySchema,
		ToolchainSchema: candidate.Spec.ToolchainSchema, StateSchema: candidate.Spec.StateSchema,
		Revision: revision,
	}
	if err := writePrivateJSON(s.path("installation.json"), installation); err != nil {
		return err
	}
	if err := writePrivateJSON(s.path("available.json"), candidate); err != nil {
		return err
	}
	if err := writePrivateJSON(s.path("host.json"), host); err != nil {
		return err
	}
	return ctx.Err()
}

func (s *FileStore) SaveAvailable(ctx context.Context, candidate Generation) error {
	if s == nil || !candidate.Valid() {
		return errors.New("available release generation is invalid")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if _, err := s.Snapshot(ctx); err != nil {
		return err
	}
	return writePrivateJSON(s.path("available.json"), candidate)
}

func (s *FileStore) SaveBackup(ctx context.Context, backup BackupRecord) error {
	if s == nil || !backup.Valid() {
		return errors.New("host lifecycle backup record is invalid")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	dir := s.path("backups")
	if err := os.MkdirAll(dir, 0700); err != nil {
		return err
	}
	info, err := os.Lstat(dir)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 || info.Mode().Perm()&0077 != 0 {
		return errors.New("host lifecycle backup directory must be private and real")
	}
	name := strings.TrimPrefix(backup.ID, "sha256:") + ".json"
	raw, err := json.Marshal(backup)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if len(raw) > maxLifecycleStateBytes {
		return errors.New("host lifecycle backup record exceeds size limit")
	}
	return safeio.PublishPrivate(filepath.Join(dir, name), raw, false)
}

func (s *FileStore) LoadBackup(ctx context.Context, id string) (BackupRecord, error) {
	if s == nil || !digestPattern.MatchString(id) {
		return BackupRecord{}, errors.New("host lifecycle backup id is invalid")
	}
	if err := ctx.Err(); err != nil {
		return BackupRecord{}, err
	}
	var backup BackupRecord
	path := filepath.Join(s.path("backups"), strings.TrimPrefix(id, "sha256:")+".json")
	if err := readPrivateJSON(path, &backup, true); err != nil {
		return BackupRecord{}, err
	}
	if !backup.Valid() || backup.ID != id {
		return BackupRecord{}, errors.New("host lifecycle backup record is invalid")
	}
	return backup, nil
}

func (s *FileStore) ListBackups(ctx context.Context) ([]BackupRecord, error) {
	return s.listBackups(ctx, true)
}

func (s *FileStore) ReadBackupSnapshot(ctx context.Context) ([]BackupRecord, error) {
	return s.listBackups(ctx, false)
}

func (s *FileStore) listBackups(ctx context.Context, cleanupInterrupted bool) ([]BackupRecord, error) {
	if s == nil {
		return nil, errors.New("host lifecycle store is not configured")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	dir := s.path("backups")
	entries, err := os.ReadDir(dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, err
	}
	result := make([]BackupRecord, 0, len(entries))
	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), ".loki-private-") {
			info, infoErr := entry.Info()
			if infoErr != nil || !info.Mode().IsRegular() || info.Mode().Perm()&0077 != 0 {
				return nil, errors.New("host lifecycle backup directory contains an unsafe interrupted publication")
			}
			if !cleanupInterrupted {
				return nil, errors.New("host lifecycle backup directory contains an interrupted publication")
			}
			if err = os.Remove(filepath.Join(dir, entry.Name())); err != nil {
				return nil, err
			}
			continue
		}
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			return nil, errors.New("host lifecycle backup directory contains an unexpected entry")
		}
		id := "sha256:" + strings.TrimSuffix(entry.Name(), ".json")
		backup, loadErr := s.LoadBackup(ctx, id)
		if loadErr != nil {
			return nil, loadErr
		}
		result = append(result, backup)
	}
	return result, nil
}

func (s *FileStore) CommitGeneration(ctx context.Context, candidate Generation, now time.Time) error {
	if s == nil || !candidate.Valid() {
		return errors.New("host lifecycle generation commit is invalid")
	}
	snapshot, err := s.Snapshot(ctx)
	if err != nil {
		return err
	}
	if snapshot.Available == nil || snapshot.Available.ID != candidate.ID || snapshot.Installation == nil {
		return errors.New("host lifecycle candidate changed before generation commit")
	}
	host := snapshot.Host
	host.ActiveGenerationID = candidate.ID
	host.ConfigSchema = candidate.Spec.ConfigSchema
	host.PolicySchema = candidate.Spec.PolicySchema
	host.ToolchainSchema = candidate.Spec.ToolchainSchema
	host.StateSchema = candidate.Spec.StateSchema
	host.Revision = lifecycleRevision(snapshot.Host.Revision, candidate.ID, snapshot.Installation.Workspace, "", now)
	if err = writePrivateJSON(s.path("installed.json"), candidate); err != nil {
		return err
	}
	if err = writePrivateJSON(s.path("host.json"), host); err != nil {
		return err
	}
	return removeIfPresent(s.path("prepared.json"))
}

func (s *FileStore) CommitComponents(ctx context.Context, enabled []string, now time.Time) error {
	if s == nil {
		return errors.New("host lifecycle store is not configured")
	}
	snapshot, err := s.Snapshot(ctx)
	if err != nil {
		return err
	}
	if snapshot.Installed == nil || snapshot.Installation == nil {
		return errors.New("host lifecycle component change requires an installed release")
	}
	normalized, err := normalizeOptionalComponentSelection(*snapshot.Installed, enabled)
	if err != nil {
		return err
	}
	host := snapshot.Host
	host.EnabledComponents = normalized
	host.Revision = lifecycleRevision(snapshot.Host.Revision, snapshot.Installed.ID, "components", normalized, now)
	return writePrivateJSON(s.path("host.json"), host)
}

func (s *FileStore) RestoreBackup(ctx context.Context, backup BackupRecord) error {
	if s == nil || !backup.Valid() {
		return errors.New("host lifecycle backup record is invalid")
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if backup.Installation != nil {
		if err := writePrivateJSON(s.path("installation.json"), *backup.Installation); err != nil {
			return err
		}
	} else if err := removeIfPresent(s.path("installation.json")); err != nil {
		return err
	}
	if backup.Installed != nil {
		if err := writePrivateJSON(s.path("installed.json"), *backup.Installed); err != nil {
			return err
		}
	} else if err := removeIfPresent(s.path("installed.json")); err != nil {
		return err
	}
	if err := writePrivateJSON(s.path("host.json"), backup.Host); err != nil {
		return err
	}
	return removeIfPresent(s.path("prepared.json"))
}

func (s *FileStore) CommitUninstall(ctx context.Context, now time.Time) error {
	if s == nil {
		return errors.New("host lifecycle store is not configured")
	}
	snapshot, err := s.Snapshot(ctx)
	if err != nil {
		return err
	}
	if snapshot.Installed == nil || snapshot.Installation == nil {
		return errors.New("host lifecycle uninstall requires an installed release")
	}
	host := snapshot.Host
	host.ActiveGenerationID = ""
	host.Revision = lifecycleRevision(snapshot.Host.Revision, snapshot.Installed.ID, snapshot.Installation.Workspace, "uninstall", now)
	if err = writePrivateJSON(s.path("host.json"), host); err != nil {
		return err
	}
	for _, name := range []string{"installed.json", "available.json", "prepared.json"} {
		if err = removeIfPresent(s.path(name)); err != nil {
			return err
		}
	}
	return ctx.Err()
}

func optionalComponentTarget(generation Generation, current []string, name string, enabled bool) ([]string, bool, error) {
	name = strings.TrimSpace(name)
	if !releaseNamePattern.MatchString(name) {
		return nil, false, errors.New("optional component name is invalid")
	}
	found := false
	optional := false
	for _, component := range generation.Spec.Components {
		if component.Name == name {
			found = true
			optional = component.Optional
			break
		}
	}
	if !found {
		return nil, false, errors.New("release does not define the requested component")
	}
	if !optional {
		return nil, false, errors.New("required release component cannot be changed independently")
	}
	normalized, err := normalizeOptionalComponentSelection(generation, current)
	if err != nil {
		return nil, false, err
	}
	exists := false
	for _, value := range normalized {
		if value == name {
			exists = true
			break
		}
	}
	if exists == enabled {
		return normalized, false, nil
	}
	if enabled {
		normalized = append(normalized, name)
		sort.Strings(normalized)
		return normalized, true, nil
	}
	result := make([]string, 0, len(normalized)-1)
	for _, value := range normalized {
		if value != name {
			result = append(result, value)
		}
	}
	return result, true, nil
}

func normalizeOptionalComponentSelection(generation Generation, values []string) ([]string, error) {
	if !generation.Valid() {
		return nil, errors.New("installed release generation is invalid")
	}
	optional := make(map[string]bool, len(generation.Spec.Components))
	for _, component := range generation.Spec.Components {
		if component.Optional {
			optional[component.Name] = true
		}
	}
	result := append([]string(nil), values...)
	for index := range result {
		result[index] = strings.TrimSpace(result[index])
		if !releaseNamePattern.MatchString(result[index]) || !optional[result[index]] {
			return nil, errors.New("enabled optional-component state is invalid for the installed release")
		}
	}
	sort.Strings(result)
	return compactStrings(result), nil
}

func lifecycleRevision(values ...any) string {
	raw, _ := json.Marshal(values)
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:])
}

func writePrivateJSON(path string, value any) error {
	raw, err := json.Marshal(value)
	if err != nil {
		return err
	}
	raw = append(raw, '\n')
	if len(raw) > maxLifecycleStateBytes {
		return errors.New("host lifecycle state file exceeds size limit")
	}
	return publishPrivate(path, raw)
}

func publishPrivate(path string, raw []byte) error {
	return safeio.PublishPrivate(path, raw, true)
}

func removeIfPresent(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	return err
}
