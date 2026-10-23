package lifecycle

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"time"

	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/platform/privateio"
)

// ApplicationRestoreIntent binds the immutable encrypted backup, current
// deployment and explicit paired program/data target. Operators provide keys
// separately on each private invocation; no key/session is retained here.
type ApplicationRestoreIntent struct {
	Version             int                                 `json:"version"`
	Mode                string                              `json:"mode"`
	BackupID            string                              `json:"backup_id"`
	RuntimeRef          string                              `json:"runtime_ref"`
	Backup              ApplicationBackupCheckpoint         `json:"application_backup"`
	Before              appvalues.ApplicationIdentity       `json:"before"`
	BeforeResources     []controlpolicy.ApplicationResource `json:"before_resources"`
	ExpectedPolicy      string                              `json:"expected_policy"`
	Policy              string                              `json:"policy"`
	CandidateGeneration string                              `json:"candidate_generation"`
	BeforeImage         string                              `json:"before_image,omitempty"`
	TargetImage         string                              `json:"target_image,omitempty"`
	BeforeProgram       string                              `json:"before_program"`
	TargetProgram       string                              `json:"target_program"`
	Interrupt           bool                                `json:"interrupt"`
}

func pinnedRestoreImage(image string) bool {
	name, digest, ok := strings.Cut(image, "@")
	return ok && name != "" && len(name) <= 512 && !strings.ContainsAny(name, " \t\r\n\x00@") && digestPattern.MatchString(digest)
}

func (i ApplicationRestoreIntent) Valid() bool {
	if i.Version != 1 || i.Mode != "native" && i.Mode != "compose" && i.Mode != "managed-full" || !digestPattern.MatchString(i.BackupID) || !digestPattern.MatchString(i.BeforeProgram) || !digestPattern.MatchString(i.TargetProgram) || !i.Backup.Complete() || !filepath.IsAbs(i.RuntimeRef) || filepath.Clean(i.RuntimeRef) != i.RuntimeRef || len(i.RuntimeRef) > 4096 || strings.ContainsAny(i.RuntimeRef, "\x00\r\n") || i.Backup.Intent.Directory != filepath.Join(i.RuntimeRef, "applications") || !migrationHex(i.CandidateGeneration, 16) || i.CandidateGeneration == i.Before.ActiveGeneration || !migrationHex(i.ExpectedPolicy, 32) || !migrationHex(i.Policy, 32) || i.BeforeResources == nil {
		return false
	}
	if i.Mode == "native" {
		if i.BeforeImage != "" || i.TargetImage != "" {
			return false
		}
	} else if !pinnedRestoreImage(i.BeforeImage) || !pinnedRestoreImage(i.TargetImage) {
		return false
	}
	before := i.Before
	archive := i.Backup.Intent.Identity
	// Retained history restoration stays in one deployment incarnation. A
	// lost history root needs explicit isolated disaster recovery instead.
	before.ActiveGeneration, before.PolicySHA256 = archive.ActiveGeneration, archive.PolicySHA256
	if before != archive || i.Before.PolicySHA256 != i.ExpectedPolicy || i.Before.Version != 2 || !migrationHex(i.Before.ActiveGeneration, 16) {
		return false
	}
	canonical, err := controlpolicy.CanonicalApplications(i.BeforeResources, nil)
	return err == nil && reflect.DeepEqual(canonical, i.BeforeResources)
}

func (i ApplicationRestoreIntent) digest() string {
	raw, _ := json.Marshal(i)
	return fmt.Sprintf("sha256:%x", sha256.Sum256(raw))
}

type ApplicationRestoreImport struct {
	Resource     string `json:"resource_id"`
	CipherSHA256 string `json:"cipher_sha256"`
	Request      string `json:"request_id"`
	Digest       string `json:"digest,omitempty"`
	Complete     bool   `json:"complete"`
}

type ApplicationRestoreOperation struct {
	Intent            ApplicationRestoreIntent   `json:"intent"`
	Imports           []ApplicationRestoreImport `json:"imports"`
	Sealed            bool                       `json:"sealed"`
	RollbackRequested bool                       `json:"rollback_requested"`
}

func (r OperationRecord) validApplicationRestore() bool {
	p := r.ApplicationRestore
	if p == nil || !p.Intent.Valid() || r.PlanID != p.Intent.digest() || r.ObservedHostRevision != r.PlanID || r.CandidateGenerationID != r.PlanID || r.ActiveGenerationID != "" || r.Maintenance == nil || r.Maintenance.Interrupt != p.Intent.Interrupt || p.Imports == nil || len(p.Imports) != len(p.Intent.Backup.Exports) || r.OriginalError != "" || r.RecoveryError != "" {
		return false
	}
	proofs := map[string]appvalues.ApplicationExportProof{}
	for _, proof := range p.Intent.Backup.Exports {
		proofs[proof.Resource] = proof
	}
	complete := true
	for _, slot := range p.Imports {
		proof, ok := proofs[slot.Resource]
		if !ok || slot.CipherSHA256 != proof.CipherSHA256 || slot.Request != privateio.OperationID(MigrationOperationUUID(r.ID), slot.Resource, "application-restore/import") || slot.Digest != "" && !migrationHex(slot.Digest, 32) || slot.Complete && slot.Digest == "" {
			return false
		}
		delete(proofs, slot.Resource)
		complete = complete && slot.Complete
	}
	if p.Sealed && !complete || migrationPhase(r.Phase) >= migrationPhase(PhaseSwitch) && r.Phase != PhaseComplete && !p.Sealed {
		return false
	}
	if (r.State == OperationApplying && r.Phase != PhaseAdmitted || r.State == OperationSucceeded) && r.RecoveryBackupID == "" {
		return false
	}
	started, startErr := time.Parse(time.RFC3339Nano, r.StartedAt)
	updated, updateErr := time.Parse(time.RFC3339Nano, r.UpdatedAt)
	if startErr != nil || updateErr != nil || started.Location() != time.UTC || updated.Location() != time.UTC || updated.Before(started) {
		return false
	}
	switch r.State {
	case OperationApplying:
		return migrationPhase(r.Phase) >= 0 && r.Phase != PhaseComplete && r.CompletedAt == "" && !r.Maintenance.Released
	case OperationSucceeded:
		return p.Sealed && !p.RollbackRequested && r.Phase == PhaseComplete && validOperationCompletion(r.CompletedAt, updated)
	case OperationRolledBack:
		return p.RollbackRequested && r.Phase == PhaseComplete && validOperationCompletion(r.CompletedAt, updated)
	default:
		return false
	}
}

func (j *OperationJournal) BeginApplicationRestore(intent ApplicationRestoreIntent, now time.Time) (OperationRecord, error) {
	if j.ready() != nil || !intent.Valid() {
		return OperationRecord{}, ErrApplicationBackup
	}
	for _, r := range j.records {
		if (!r.State.Terminal() || r.Maintenance != nil && !r.Maintenance.Released) && (r.Kind != OperationApplicationRestore || r.PlanID != intent.digest()) {
			return OperationRecord{}, ErrApplicationBackup
		}
	}
	for _, r := range j.records {
		if r.Kind == OperationApplicationRestore && r.PlanID == intent.digest() {
			return detachedMigrationRecord(r), nil
		}
	}
	id, err := newOperationID()
	if err != nil {
		return OperationRecord{}, err
	}
	stamp := now.UTC().Format(time.RFC3339Nano)
	r := OperationRecord{ID: id, Kind: OperationApplicationRestore, PlanID: intent.digest(), ObservedHostRevision: intent.digest(), CandidateGenerationID: intent.digest(), State: OperationApplying, Phase: PhaseAdmitted, StartedAt: stamp, UpdatedAt: stamp, Maintenance: &OperationMaintenance{Interrupt: intent.Interrupt}, ApplicationRestore: &ApplicationRestoreOperation{Intent: intent, Imports: []ApplicationRestoreImport{}}}
	for _, proof := range intent.Backup.Exports {
		r.ApplicationRestore.Imports = append(r.ApplicationRestore.Imports, ApplicationRestoreImport{Resource: proof.Resource, CipherSHA256: proof.CipherSHA256, Request: privateio.OperationID(MigrationOperationUUID(id), proof.Resource, "application-restore/import")})
	}
	if err := j.persist(r, false); err != nil {
		return OperationRecord{}, err
	}
	j.records[id] = detachedMigrationRecord(r)
	return detachedMigrationRecord(r), nil
}

func (j *OperationJournal) updateApplicationRestore(id string, now time.Time, update func(*OperationRecord) error) (OperationRecord, error) {
	r, err := j.require(id)
	if err != nil {
		return r, err
	}
	if r.Kind != OperationApplicationRestore || r.ApplicationRestore == nil {
		return OperationRecord{}, ErrApplicationBackup
	}
	r = detachedMigrationRecord(r)
	if err := update(&r); err != nil {
		return OperationRecord{}, err
	}
	r.UpdatedAt, err = nextOperationTime(r, now)
	if err != nil {
		return OperationRecord{}, err
	}
	if r.State.Terminal() {
		r.CompletedAt = r.UpdatedAt
	}
	if err := j.persist(r, true); err != nil {
		return OperationRecord{}, err
	}
	j.records[id] = detachedMigrationRecord(r)
	return r, nil
}

func (j *OperationJournal) AdvanceApplicationRestore(id string, phase OperationPhase, now time.Time) (OperationRecord, error) {
	return j.updateApplicationRestore(id, now, func(r *OperationRecord) error {
		if r.State != OperationApplying || r.ApplicationRestore.RollbackRequested || phase == PhaseComplete || migrationPhase(phase) < 0 {
			return ErrApplicationBackup
		}
		if r.Phase == phase {
			return nil
		}
		if migrationPhase(phase) != migrationPhase(r.Phase)+1 {
			return ErrApplicationBackup
		}
		if r.Phase == PhaseAdmitted && r.RecoveryBackupID == "" {
			return ErrApplicationBackup
		}
		r.Phase = phase
		return nil
	})
}

func (j *OperationJournal) CheckpointApplicationRestoreSafety(id string, backupID string, now time.Time) (OperationRecord, error) {
	return j.updateApplicationRestore(id, now, func(r *OperationRecord) error {
		if r.State != OperationApplying || r.Phase != PhaseAdmitted || !digestPattern.MatchString(backupID) || r.RecoveryBackupID != "" && r.RecoveryBackupID != backupID {
			return ErrApplicationBackup
		}
		r.RecoveryBackupID = backupID
		return nil
	})
}

func (j *OperationJournal) CheckpointApplicationRestoreImport(id, resource, digest string, complete bool, now time.Time) (OperationRecord, error) {
	return j.updateApplicationRestore(id, now, func(r *OperationRecord) error {
		if r.State != OperationApplying || r.Phase != PhaseMigrate || r.ApplicationRestore.RollbackRequested || !migrationHex(digest, 32) {
			return ErrApplicationBackup
		}
		for index, slot := range r.ApplicationRestore.Imports {
			if slot.Resource != resource {
				continue
			}
			if slot.Digest != "" && slot.Digest != digest || slot.Complete && !complete || complete && slot.Digest == "" {
				return ErrApplicationBackup
			}
			slot.Digest, slot.Complete = digest, complete
			r.ApplicationRestore.Imports[index] = slot
			return nil
		}
		return ErrApplicationBackup
	})
}

func (j *OperationJournal) SealApplicationRestore(id string, now time.Time) (OperationRecord, error) {
	return j.updateApplicationRestore(id, now, func(r *OperationRecord) error {
		if r.State != OperationApplying || r.Phase != PhaseMigrate || r.ApplicationRestore.RollbackRequested {
			return ErrApplicationBackup
		}
		for _, slot := range r.ApplicationRestore.Imports {
			if !slot.Complete {
				return ErrApplicationBackup
			}
		}
		r.ApplicationRestore.Sealed = true
		return nil
	})
}

func (j *OperationJournal) RequestApplicationRestoreRollback(id string, now time.Time) (OperationRecord, error) {
	return j.updateApplicationRestore(id, now, func(r *OperationRecord) error {
		if r.State != OperationApplying || migrationPhase(r.Phase) > migrationPhase(PhaseSwitch) {
			return ErrApplicationBackup
		}
		r.ApplicationRestore.RollbackRequested = true
		return nil
	})
}

func (j *OperationJournal) CompleteApplicationRestore(id string, rollback bool, now time.Time) (OperationRecord, error) {
	return j.updateApplicationRestore(id, now, func(r *OperationRecord) error {
		if r.State != OperationApplying || rollback != r.ApplicationRestore.RollbackRequested || !rollback && r.Phase != PhaseHealth {
			return ErrApplicationBackup
		}
		r.State = OperationSucceeded
		if rollback {
			r.State = OperationRolledBack
		}
		r.Phase = PhaseComplete
		return nil
	})
}

func (j *OperationJournal) AcknowledgeApplicationRestoreRelease(id string, now time.Time) (OperationRecord, error) {
	return j.updateApplicationRestore(id, now, func(r *OperationRecord) error {
		if r.State != OperationSucceeded && r.State != OperationRolledBack {
			return ErrApplicationBackup
		}
		r.Maintenance.Released = true
		return nil
	})
}
