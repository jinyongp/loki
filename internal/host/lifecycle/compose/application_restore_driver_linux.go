//go:build linux

package compose

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"time"

	controlpolicy "loki/internal/control/policy"
	"loki/internal/host/lifecycle"
	"loki/internal/platform/safeio"
)

type ApplicationRestoreStatus struct {
	Operation  string                   `json:"operation_id"`
	State      lifecycle.OperationState `json:"state"`
	Phase      lifecycle.OperationPhase `json:"phase"`
	Generation string                   `json:"candidate_generation"`
	Protection string                   `json:"protection,omitempty"`
	Imports    int                      `json:"completed_imports"`
	Sealed     bool                     `json:"sealed"`
	Released   bool                     `json:"released"`
	Rollback   bool                     `json:"rollback_requested"`
}

func applicationRestoreStatus(r lifecycle.OperationRecord) ApplicationRestoreStatus {
	result := ApplicationRestoreStatus{Operation: lifecycle.MigrationOperationUUID(r.ID), State: r.State, Phase: r.Phase, Generation: r.ApplicationRestore.Intent.CandidateGeneration, Sealed: r.ApplicationRestore.Sealed, Released: r.Maintenance.Released, Rollback: r.ApplicationRestore.RollbackRequested}
	for _, slot := range r.ApplicationRestore.Imports {
		if slot.Complete {
			result.Imports++
		}
	}
	return result
}

func (b *Backend) ApplicationRestoreStatus(ctx context.Context) (ApplicationRestoreStatus, error) {
	lock, err := lifecycle.AcquireOperationLock(b.root)
	if err != nil {
		return ApplicationRestoreStatus{}, err
	}
	defer lock.Close()
	j, err := lifecycle.OpenOperationJournal(b.root, lock, lifecycle.OperationJournalOptions{})
	if err != nil {
		return ApplicationRestoreStatus{}, err
	}
	items, err := j.List()
	if err != nil {
		return ApplicationRestoreStatus{}, err
	}
	for index := len(items) - 1; index >= 0; index-- {
		if items[index].ApplicationRestore != nil && items[index].ApplicationRestore.Intent.Mode == "compose" {
			return applicationRestoreStatus(items[index]), nil
		}
	}
	return ApplicationRestoreStatus{}, lifecycle.ErrApplicationBackup
}

func (b *Backend) fenceApplicationRestore(ctx context.Context, r lifecycle.OperationRecord) error {
	i := r.ApplicationRestore.Intent
	state, found, err := b.loadRuntime()
	if err != nil || !found || state.CoreImage != i.BeforeImage || state.GenerationID != i.BeforeProgram {
		return lifecycle.ErrApplicationBackup
	}
	op := lifecycle.MigrationOperationUUID(r.ID)
	live, err := b.launcherRunning(ctx, state)
	if err != nil {
		return err
	}
	if live {
		if err := b.BeginJobMaintenance(ctx, op, i.Interrupt); err != nil {
			return err
		}
		body, err := b.applicationAdministrator(ctx, state, []string{"maintenance", "begin", "--operation-id", op}, nil)
		var receipt struct {
			Maintenance struct {
				Operation string `json:"operation_id"`
				Active    bool   `json:"active"`
			} `json:"maintenance"`
		}
		if err != nil || json.Unmarshal(body, &receipt) != nil || receipt.Maintenance.Operation != op || !receipt.Maintenance.Active {
			return lifecycle.ErrApplicationBackup
		}
	} else {
		b.maintenance = &hostMaintenance{Version: 1, OperationID: op, Interrupt: i.Interrupt, Active: true}
		if _, err := os.Lstat(state.ApplicationRuntimeRoot); os.IsNotExist(err) {
			actual, err := b.migrationMetadata(ctx, state, i.BeforeResources, false)
			if err != nil || actual.Identity != i.Before || actual.Policy != i.ExpectedPolicy {
				return lifecycle.ErrApplicationBackup
			}
			b.initialApplicationMaintenance = op
			err = b.prepareApplicationValues(ctx, &state)
			b.initialApplicationMaintenance = ""
			if err != nil {
				return err
			}
			if err := b.saveRuntime(state); err != nil {
				return err
			}
		} else if err != nil {
			return err
		}
		gate, err := b.maintenanceCall(ctx, state, "verify", op, false, true)
		if err != nil || gate == nil || !gate.Active || gate.OperationID != op || gate.Interrupt != i.Interrupt {
			return lifecycle.ErrApplicationBackup
		}
	}
	return nil
}

func (b *Backend) stopApplicationRestoreCandidate(ctx context.Context, r lifecycle.OperationRecord) error {
	projection := filepath.Join("/dev/shm", "loki-tools-"+applicationOwnerID(b.root)+"-restore-"+r.ID, "secrets")
	generation, err := b.applicationGenerationSource(ctx, r.ApplicationRestore.Intent.CandidateGeneration)
	if err != nil {
		return err
	}
	return b.stopApplicationCandidate(ctx, applicationRestoreBinding(r), projection, generation)
}

func (b *Backend) applicationRestoreCatalog(r lifecycle.OperationRecord, rollback bool) error {
	resources := r.ApplicationRestore.Intent.Backup.Intent.Resources
	if rollback {
		resources = r.ApplicationRestore.Intent.BeforeResources
	}
	if resources == nil {
		resources = []controlpolicy.ApplicationResource{}
	}
	body, err := json.Marshal(struct {
		Resources []controlpolicy.ApplicationResource `json:"resources"`
	}{resources})
	if err != nil {
		return err
	}
	return safeio.PublishPrivate(filepath.Join(b.root, "application-resources.json"), body, true)
}

func (b *Backend) startApplicationRestoreServing(ctx context.Context, r lifecycle.OperationRecord, rollback bool) error {
	if err := b.stopApplicationRestoreCandidate(ctx, r); err != nil {
		return err
	}
	if err := b.applicationRestoreCatalog(r, rollback); err != nil {
		return err
	}
	i := r.ApplicationRestore.Intent
	if rollback {
		state, found, err := b.loadRuntime()
		if err != nil || !found {
			return lifecycle.ErrApplicationBackup
		}
		state.CoreImage, state.GenerationID = i.BeforeImage, i.BeforeProgram
		if err := b.saveRuntime(state); err != nil {
			return err
		}
	}
	b.maintenance = &hostMaintenance{Version: 1, OperationID: lifecycle.MigrationOperationUUID(r.ID), Interrupt: i.Interrupt, Active: true}
	b.initialApplicationMaintenance = b.maintenance.OperationID
	defer func() { b.initialApplicationMaintenance = "" }()
	if err := b.Restart(ctx); err != nil {
		return err
	}
	policy, err := b.ApplicationPolicy()
	want := i.Policy
	if rollback {
		want = i.ExpectedPolicy
	}
	if err != nil || policy != want {
		return lifecycle.ErrApplicationBackup
	}
	return b.Health(ctx)
}

func (b *Backend) ApplyApplicationRestore(ctx context.Context, intent *lifecycle.ApplicationRestoreIntent, identity []byte, protection string, input []byte) (ApplicationRestoreStatus, error) {
	var result ApplicationRestoreStatus
	if len(identity) > 65536 || protection != "" && protection != "init" && protection != "unlock" || (protection != "") != (len(input) > 0) || len(input) > 65536 {
		return result, lifecycle.ErrApplicationBackup
	}
	err := b.withApplicationRestore(ctx, intent, func(ctx context.Context, j *lifecycle.OperationJournal, first lifecycle.OperationRecord) error {
		if first.Maintenance.Released {
			result = applicationRestoreStatus(first)
			return nil
		}
		if err := b.cleanupApplicationRestoreAdministrators(ctx); err != nil {
			return err
		}
		for step := 0; step < 12; step++ {
			r, _, err := b.applicationRestoreRecord(ctx)
			if err != nil {
				return err
			}
			result = applicationRestoreStatus(r)
			i := r.ApplicationRestore.Intent
			if r.State.Terminal() {
				if err := b.stopApplicationRestoreCandidate(ctx, r); err != nil {
					return err
				}
				if err := b.Health(ctx); err != nil {
					return err
				}
				state, _, err := b.loadRuntime()
				if err != nil {
					return err
				}
				body, err := b.applicationAdministrator(ctx, state, []string{"maintenance", "end", "--operation-id", lifecycle.MigrationOperationUUID(r.ID)}, nil)
				var gate struct {
					Maintenance struct {
						Operation string `json:"operation_id"`
						Active    bool   `json:"active"`
					} `json:"maintenance"`
				}
				if err != nil || json.Unmarshal(body, &gate) != nil || gate.Maintenance.Operation != lifecycle.MigrationOperationUUID(r.ID) || gate.Maintenance.Active {
					return lifecycle.ErrApplicationBackup
				}
				if err := b.EndJobMaintenance(ctx, lifecycle.MigrationOperationUUID(r.ID)); err != nil {
					return err
				}
				if err := b.cleanupApplicationProjection(ctx, applicationRestoreBinding(r)); err != nil {
					return err
				}
				if _, err := j.AcknowledgeApplicationRestoreRelease(r.ID, time.Now()); err != nil {
					return err
				}
				result.Released = true
				return nil
			}
			if r.ApplicationRestore.RollbackRequested {
				if err := b.fenceApplicationRestore(ctx, r); err != nil {
					return err
				}
				if err := b.Stop(ctx); err != nil {
					return err
				}
				if err := b.VerifyStopped(ctx); err != nil {
					return err
				}
				state, _, err := b.loadRuntime()
				if err != nil {
					return err
				}
				before, err := b.migrationMetadata(ctx, state, i.BeforeResources, false)
				if err != nil || before.Identity != i.Before {
					return lifecycle.ErrApplicationBackup
				}
				if err := b.startApplicationRestoreServing(ctx, r, true); err != nil {
					return err
				}
				if _, err := j.CompleteApplicationRestore(r.ID, true, time.Now()); err != nil {
					return err
				}
				continue
			}
			switch r.Phase {
			case lifecycle.PhaseAdmitted:
				if err := b.fenceApplicationRestore(ctx, r); err != nil {
					return err
				}
				store, err := lifecycle.OpenFileStore(b.root)
				if err != nil {
					return err
				}
				engine := lifecycle.TransactionEngine{Store: store, Backend: b}
				safety, err := engine.CaptureApplicationRestoreSafety(ctx, j, r.ID)
				if err != nil {
					return err
				}
				if _, err := j.CheckpointApplicationRestoreSafety(r.ID, safety.ID, time.Now()); err != nil {
					return err
				}
				if _, err := j.AdvanceApplicationRestore(r.ID, lifecycle.PhaseSnapshot, time.Now()); err != nil {
					return err
				}
			case lifecycle.PhaseSnapshot:
				if err := b.fenceApplicationRestore(ctx, r); err != nil {
					return err
				}
				if err := b.Stop(ctx); err != nil {
					return err
				}
				if err := b.VerifyStopped(ctx); err != nil {
					return err
				}
				if _, err := j.AdvanceApplicationRestore(r.ID, lifecycle.PhaseMigrate, time.Now()); err != nil {
					return err
				}
			case lifecycle.PhaseMigrate:
				if err := b.fenceApplicationRestore(ctx, r); err != nil {
					return err
				}
				if err := b.VerifyStopped(ctx); err != nil {
					return err
				}
				projection, generation, err := b.prepareApplicationRestoreCandidate(ctx)
				if err != nil {
					return fmt.Errorf("prepare private restore candidate: %w", err)
				}
				if err := b.startApplicationCandidate(ctx, applicationRestoreBinding(r), projection, generation); err != nil {
					return fmt.Errorf("start private restore candidate: %w", err)
				}
				state, err := b.applicationRestoreProtection(ctx, "status", nil)
				if err != nil {
					return fmt.Errorf("inspect private restore protection: %w", err)
				}
				if protection != "" {
					state, err = b.applicationRestoreProtection(ctx, protection, input)
					if err != nil {
						return err
					}
					protection = ""
				}
				result.Protection = state
				if state != "unlocked" {
					return nil
				}
				if len(identity) == 0 && len(r.ApplicationRestore.Imports) > 0 {
					result.Protection = "archive_identity_required"
					return nil
				}
				if err := b.importApplicationRestore(ctx, identity); err != nil {
					return fmt.Errorf("import and seal private restore candidate: %w", err)
				}
				if _, err := j.AdvanceApplicationRestore(r.ID, lifecycle.PhaseSwitch, time.Now()); err != nil {
					return err
				}
			case lifecycle.PhaseSwitch:
				backup, target, err := b.applicationRestoreBackup(ctx, i)
				if err != nil {
					return err
				}
				if err := b.stopApplicationRestoreCandidate(ctx, r); err != nil {
					return err
				}
				if err := b.restoreSnapshot(ctx, i.RuntimeRef, true); err != nil {
					return err
				}
				request := &applicationCandidateLayout{Before: i.Before, CandidateGeneration: i.CandidateGeneration, Policy: i.Policy, Operation: lifecycle.MigrationOperationUUID(r.ID), Restore: true}
				if _, err := b.applicationLayout(ctx, target, i.Backup.Intent.Resources, false, request, "", true); err != nil {
					return err
				}
				if err := b.applicationRestoreCatalog(r, false); err != nil {
					return err
				}
				store, err := lifecycle.OpenFileStore(b.root)
				if err != nil {
					return err
				}
				if err := store.RestoreBackup(ctx, backup); err != nil {
					return err
				}
				if _, err := j.AdvanceApplicationRestore(r.ID, lifecycle.PhaseRestart, time.Now()); err != nil {
					return err
				}
			case lifecycle.PhaseRestart:
				if err := b.startApplicationRestoreServing(ctx, r, false); err != nil {
					return err
				}
				if _, err := j.AdvanceApplicationRestore(r.ID, lifecycle.PhaseHealth, time.Now()); err != nil {
					return err
				}
			case lifecycle.PhaseHealth:
				if err := b.Health(ctx); err != nil {
					return err
				}
				if _, err := j.CompleteApplicationRestore(r.ID, false, time.Now()); err != nil {
					return err
				}
			default:
				return lifecycle.ErrApplicationBackup
			}
		}
		return lifecycle.ErrApplicationBackup
	})
	if err != nil && result.Operation != "" {
		err = fmt.Errorf("application restore %s requires original recovery: %w", result.Phase, err)
	}
	return result, err
}

func (b *Backend) RollbackApplicationRestore(ctx context.Context) (ApplicationRestoreStatus, error) {
	err := b.withApplicationRestore(ctx, nil, func(ctx context.Context, j *lifecycle.OperationJournal, r lifecycle.OperationRecord) error {
		if r.State.Terminal() {
			return lifecycle.ErrApplicationBackup
		}
		state, _, err := b.loadRuntime()
		if err != nil {
			return err
		}
		before, err := b.migrationMetadata(ctx, state, r.ApplicationRestore.Intent.BeforeResources, false)
		if err != nil || before.Identity != r.ApplicationRestore.Intent.Before {
			return lifecycle.ErrApplicationBackup
		}
		_, err = j.RequestApplicationRestoreRollback(r.ID, time.Now())
		return err
	})
	if err != nil {
		return ApplicationRestoreStatus{}, err
	}
	return b.ApplyApplicationRestore(ctx, nil, nil, "", nil)
}
