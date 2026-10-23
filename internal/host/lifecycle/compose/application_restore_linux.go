//go:build linux

package compose

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	controlpolicy "loki/internal/control/policy"
	"loki/internal/host/lifecycle"
)

func (b *Backend) applicationRestoreBackup(ctx context.Context, intent lifecycle.ApplicationRestoreIntent) (lifecycle.BackupRecord, runtimeState, error) {
	if intent.NewDeployment {
		return b.newDeploymentRestoreArchive(ctx, intent)
	}
	var target runtimeState
	store, err := lifecycle.OpenFileStore(b.root)
	if err != nil {
		return lifecycle.BackupRecord{}, target, err
	}
	backup, err := store.LoadBackup(ctx, intent.BackupID)
	if err != nil || backup.ApplicationBackup == nil || backup.RuntimeRef != intent.RuntimeRef || !reflect.DeepEqual(*backup.ApplicationBackup, intent.Backup) {
		return backup, target, lifecycle.ErrApplicationBackup
	}
	directory, err := b.snapshotDirectory(backup.RuntimeRef)
	if err != nil {
		return backup, target, err
	}
	var manifest snapshotManifest
	if readPrivateJSON(filepath.Join(directory, "manifest.json"), &manifest) != nil || manifest.Version != snapshotManifestVersion || manifest.Runtime == nil || manifest.ApplicationBackup == nil || !reflect.DeepEqual(*manifest.ApplicationBackup, intent.Backup) {
		return backup, target, lifecycle.ErrApplicationBackup
	}
	target = *manifest.Runtime
	if target.validate() != nil || target.CoreImage != intent.TargetImage || target.GenerationID != intent.TargetProgram || backup.Installed == nil || backup.Installed.ID != intent.TargetProgram {
		return backup, target, lifecycle.ErrApplicationBackup
	}
	for _, proof := range intent.Backup.Exports {
		if err := verifyApplicationCipher(filepath.Join(intent.Backup.Intent.Directory, restoreCipherName(proof.Resource)), proof); err != nil {
			return backup, target, err
		}
	}
	// Validate every archive that will be restored before changing any volume.
	// Retained value/session/platform roots are deliberately not restored.
	for _, name := range persistentVolumes {
		if name == "application-state" || name == "runner-state" || name == "runtime-state" || name == "signing-state" {
			continue
		}
		entry := manifest.Volumes[name]
		if !entry.Present {
			continue
		}
		sum, err := fileSHA256(filepath.Join(directory, name+".tar"))
		if err != nil || !validSHA256(entry.SHA256) || sum != entry.SHA256 {
			return backup, target, lifecycle.ErrApplicationBackup
		}
	}
	return backup, target, nil
}

func restoreCipherName(resource string) string {
	return strings.ReplaceAll(resource, "-", "") + ".age"
}

func (b *Backend) currentApplicationCatalog() ([]controlpolicy.ApplicationResource, error) {
	var catalog struct {
		Resources []controlpolicy.ApplicationResource `json:"resources"`
	}
	path := filepath.Join(b.root, "application-resources.json")
	if _, err := os.Lstat(path); os.IsNotExist(err) {
		return []controlpolicy.ApplicationResource{}, nil
	} else if err != nil {
		return nil, err
	}
	if err := readPrivateJSON(path, &catalog); err != nil {
		return nil, err
	}
	return catalog.Resources, nil
}

// PrepareApplicationRestore is read-only with respect to generations, keys and
// admission. It checks the actual archived/current program metadata and all
// encrypted artifacts before an operator approves the paired restore intent.
func (b *Backend) PrepareApplicationRestore(ctx context.Context, backupID string, interrupt bool) (lifecycle.ApplicationRestoreIntent, error) {
	return b.prepareApplicationRestore(ctx, backupID, interrupt, nil)
}

func (b *Backend) prepareApplicationRestore(ctx context.Context, backupID string, interrupt bool, recovery *newDeploymentApplicationArchive) (lifecycle.ApplicationRestoreIntent, error) {
	var intent lifecycle.ApplicationRestoreIntent
	lock, err := lifecycle.AcquireOperationLock(b.root)
	if err != nil {
		return intent, err
	}
	defer lock.Close()
	j, err := lifecycle.OpenOperationJournal(b.root, lock, lifecycle.OperationJournalOptions{})
	if err != nil {
		return intent, err
	}
	if _, found, err := j.Active(); err != nil || found {
		return intent, lifecycle.ErrApplicationBackup
	}
	state, found, err := b.loadRuntime()
	if err != nil || !found {
		return intent, lifecycle.ErrApplicationBackup
	}
	store, err := lifecycle.OpenFileStore(b.root)
	if err != nil {
		return intent, err
	}
	var backup lifecycle.BackupRecord
	if recovery == nil {
		backup, err = store.LoadBackup(ctx, backupID)
	} else {
		backup = recovery.Source
	}
	if err != nil || backup.ApplicationBackup == nil {
		return intent, lifecycle.ErrApplicationBackup
	}
	var manifest snapshotManifest
	if recovery == nil {
		directory, err := b.snapshotDirectory(backup.RuntimeRef)
		if err != nil {
			return intent, err
		}
		if readPrivateJSON(filepath.Join(directory, "manifest.json"), &manifest) != nil || manifest.Runtime == nil {
			return intent, lifecycle.ErrApplicationBackup
		}
	} else {
		manifest.Runtime = &recovery.Runtime
	}
	resources, err := b.currentApplicationCatalog()
	if err != nil {
		return intent, err
	}
	before, err := b.migrationMetadata(ctx, state, resources, recovery != nil)
	if err != nil || recovery == nil && before.Identity.Version != 2 || recovery != nil && before.Identity.Version != 1 || before.Policy != state.ApplicationPolicySHA256 {
		return intent, lifecycle.ErrApplicationBackup
	}
	resources, err = controlpolicy.CanonicalApplications(resources, before.BaseEnvironment)
	if err != nil {
		return intent, err
	}
	target, err := b.migrationMetadata(ctx, *manifest.Runtime, backup.ApplicationBackup.Intent.Resources, false)
	if err != nil || target.Identity != before.Identity {
		return intent, lifecycle.ErrApplicationBackup
	}
	var nonce [16]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return intent, err
	}
	intent = lifecycle.ApplicationRestoreIntent{Version: 1, Mode: "compose", BackupID: backup.ID, RuntimeRef: backup.RuntimeRef, Backup: *backup.ApplicationBackup, Before: before.Identity, BeforeResources: resources, ExpectedPolicy: before.Policy, Policy: target.Policy, CandidateGeneration: hex.EncodeToString(nonce[:]), BeforeImage: state.CoreImage, TargetImage: manifest.Runtime.CoreImage, BeforeProgram: state.GenerationID, TargetProgram: manifest.Runtime.GenerationID, Interrupt: interrupt}
	if recovery != nil {
		intent.NewDeployment = true
		intent.BackupID, intent.RuntimeRef = recovery.ID(), recovery.directory
		intent.Backup.Intent.Directory = filepath.Join(recovery.directory, "applications")
	}
	if !intent.Valid() {
		return lifecycle.ApplicationRestoreIntent{}, lifecycle.ErrApplicationBackup
	}
	if _, _, err := b.applicationRestoreBackup(ctx, intent); err != nil {
		return lifecycle.ApplicationRestoreIntent{}, err
	}
	return intent, nil
}

func (b *Backend) withApplicationRestore(ctx context.Context, intent *lifecycle.ApplicationRestoreIntent, use func(context.Context, *lifecycle.OperationJournal, lifecycle.OperationRecord) error) error {
	lock, err := lifecycle.AcquireOperationLock(b.root)
	if err != nil {
		return err
	}
	defer lock.Close()
	j, err := lifecycle.OpenOperationJournal(b.root, lock, lifecycle.OperationJournalOptions{})
	if err != nil {
		return err
	}
	var record lifecycle.OperationRecord
	if intent != nil {
		if !intent.Valid() || intent.Mode != "compose" {
			return lifecycle.ErrApplicationBackup
		}
		items, err := j.List()
		if err != nil {
			return err
		}
		encoded, _ := json.Marshal(intent)
		digest := sha256.Sum256(encoded)
		plan := "sha256:" + hex.EncodeToString(digest[:])
		for _, item := range items {
			if item.Kind == lifecycle.OperationApplicationRestore && item.PlanID == plan {
				record = item
				break
			}
		}
		if record.ID == "" {
			if _, _, err := b.applicationRestoreBackup(ctx, *intent); err != nil {
				return err
			}
			state, found, err := b.loadRuntime()
			if err != nil || !found || state.CoreImage != intent.BeforeImage || state.GenerationID != intent.BeforeProgram {
				return lifecycle.ErrApplicationBackup
			}
			actual, err := b.migrationMetadata(ctx, state, intent.BeforeResources, intent.NewDeployment)
			if err != nil || actual.Identity != intent.Before || actual.Policy != intent.ExpectedPolicy {
				return lifecycle.ErrApplicationBackup
			}
			record, err = j.BeginApplicationRestore(*intent, time.Now())
			if err != nil {
				return err
			}
		}
	} else {
		var found bool
		record, found, err = j.Active()
		if err != nil || !found || record.Kind != lifecycle.OperationApplicationRestore || record.ApplicationRestore == nil || record.ApplicationRestore.Intent.Mode != "compose" {
			return lifecycle.ErrApplicationBackup
		}
	}
	return lifecycle.WithHostOperation(ctx, j, record.ID, func(ctx context.Context) error { return use(ctx, j, record) })
}

func (b *Backend) applicationRestoreRecord(ctx context.Context) (lifecycle.OperationRecord, *lifecycle.OperationJournal, error) {
	r, j, err := lifecycle.CurrentHostOperation(ctx)
	if err != nil || !j.HoldsOperationRoot(b.root) || r.Kind != lifecycle.OperationApplicationRestore || r.ApplicationRestore == nil || r.ApplicationRestore.Intent.Mode != "compose" {
		return lifecycle.OperationRecord{}, nil, lifecycle.ErrApplicationBackup
	}
	active, found, err := j.Active()
	if err != nil || !found || active.ID != r.ID {
		return lifecycle.OperationRecord{}, nil, lifecycle.ErrApplicationBackup
	}
	state, found, err := b.loadRuntime()
	i := r.ApplicationRestore.Intent
	if err != nil || !found || (state.CoreImage != i.BeforeImage || state.GenerationID != i.BeforeProgram) && (state.CoreImage != i.TargetImage || state.GenerationID != i.TargetProgram) {
		return lifecycle.OperationRecord{}, nil, lifecycle.ErrApplicationBackup
	}
	return r, j, nil
}

func applicationRestoreBinding(r lifecycle.OperationRecord) applicationCandidateBinding {
	i := r.ApplicationRestore.Intent
	return applicationCandidateBinding{kind: "restore", operation: r.ID, generation: i.CandidateGeneration, image: i.TargetImage}
}

func (b *Backend) prepareApplicationRestoreCandidate(ctx context.Context) (string, string, error) {
	r, _, err := b.applicationRestoreRecord(ctx)
	if err != nil || r.State != lifecycle.OperationApplying || r.Phase != lifecycle.PhaseMigrate || r.ApplicationRestore.RollbackRequested {
		return "", "", lifecycle.ErrApplicationBackup
	}
	i := r.ApplicationRestore.Intent
	_, target, err := b.applicationRestoreBackup(ctx, i)
	if err != nil {
		return "", "", err
	}
	current, _, err := b.loadRuntime()
	if err != nil {
		return "", "", err
	}
	before, err := b.migrationMetadata(ctx, current, i.BeforeResources, i.NewDeployment)
	if err != nil || before.Identity != i.Before || before.Policy != i.ExpectedPolicy {
		return "", "", lifecycle.ErrApplicationBackup
	}
	projection, err := b.applicationCandidateProjection("restore", r.ID)
	if err != nil {
		return "", "", err
	}
	request := &applicationCandidateLayout{Before: i.Before, CandidateGeneration: i.CandidateGeneration, Policy: i.Policy, Operation: lifecycle.MigrationOperationUUID(r.ID), Restore: true}
	if _, err := b.applicationLayout(ctx, target, i.Backup.Intent.Resources, false, request, projection, false); err != nil {
		return "", "", err
	}
	generation, err := b.applicationGenerationSource(ctx, i.CandidateGeneration)
	return projection, generation, err
}
