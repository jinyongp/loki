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

type SecretMigrationStatus struct {
	Operation  string                   `json:"operation_id"`
	State      lifecycle.OperationState `json:"state"`
	Phase      lifecycle.OperationPhase `json:"phase"`
	Generation string                   `json:"candidate_generation"`
	Protection string                   `json:"protection,omitempty"`
	Released   bool                     `json:"released"`
	Rollback   bool                     `json:"rollback_requested"`
}

func composeMigrationStatus(r lifecycle.OperationRecord) SecretMigrationStatus {
	return SecretMigrationStatus{Operation: lifecycle.MigrationOperationUUID(r.ID), State: r.State, Phase: r.Phase, Generation: r.SecretMigration.Intent.CandidateGeneration, Released: r.Maintenance.Released, Rollback: r.SecretMigration.RollbackRequested}
}

func (b *Backend) SecretMigrationStatus(ctx context.Context) (SecretMigrationStatus, error) {
	lock, err := lifecycle.AcquireOperationLock(b.root)
	if err != nil {
		return SecretMigrationStatus{}, err
	}
	defer lock.Close()
	j, err := lifecycle.OpenOperationJournal(b.root, lock, lifecycle.OperationJournalOptions{})
	if err != nil {
		return SecretMigrationStatus{}, err
	}
	items, err := j.List()
	if err != nil {
		return SecretMigrationStatus{}, err
	}
	for n := len(items) - 1; n >= 0; n-- {
		if items[n].SecretMigration != nil && items[n].SecretMigration.Intent.Mode == "compose" {
			return composeMigrationStatus(items[n]), nil
		}
	}
	return SecretMigrationStatus{}, errComposeMigration
}

func (b *Backend) fenceMigrationApplication(ctx context.Context, r lifecycle.OperationRecord, begin bool) error {
	if begin && r.State != lifecycle.OperationApplying || !begin && !r.State.Terminal() {
		return errComposeMigration
	}
	state, found, err := b.loadRuntime()
	private, ok := b.runner.(PrivateRunner)
	if err != nil || !found || !ok || state.ApplicationRuntimeRoot == "" {
		return errComposeMigration
	}
	action := "end"
	if begin {
		action = "begin"
	}
	layout := filepath.Join(filepath.Dir(filepath.Dir(state.ApplicationStateSource)), "secrets-admin.json")
	args := []string{"run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--security-opt", "no-new-privileges:true", "--user", "0:0", "--memory", "134217728", "--pids-limit", "32", "--log-driver", "none", "--entrypoint", "/opt/loki/bin/loki", "--mount", applicationBind(filepath.Join(state.ApplicationRuntimeRoot, "admin"), "/run/loki/secrets/admin", true), "--mount", applicationBind(layout, "/etc/loki/secret-admin.json", true), state.CoreImage, "secret", "app", "maintenance", action, "--operation-id", lifecycle.MigrationOperationUUID(r.ID), "--layout", "/etc/loki/secret-admin.json"}
	body, err := private.RunPrivate(ctx, nil, nil, args...)
	defer clear(body)
	var receipt struct {
		Maintenance struct {
			Version   int    `json:"version"`
			Operation string `json:"operation_id"`
			Active    bool   `json:"active"`
		} `json:"maintenance"`
	}
	if err != nil || json.Unmarshal(body, &receipt) != nil || receipt.Maintenance.Version != 1 || receipt.Maintenance.Operation != lifecycle.MigrationOperationUUID(r.ID) || receipt.Maintenance.Active != begin {
		return errComposeMigration
	}
	return nil
}

func (b *Backend) freezeMigration(ctx context.Context, r lifecycle.OperationRecord) error {
	state, found, err := b.loadRuntime()
	if err != nil || !found {
		return errComposeMigration
	}
	op := lifecycle.MigrationOperationUUID(r.ID)
	live, err := b.launcherRunning(ctx, state)
	if err != nil {
		return err
	}
	if live {
		if err := b.BeginJobMaintenance(ctx, op, r.SecretMigration.Intent.Interrupt); err != nil {
			return err
		}
		if err := b.fenceMigrationApplication(ctx, r, true); err != nil {
			return err
		}
	} else {
		if _, err := os.Lstat(state.ApplicationRuntimeRoot); os.IsNotExist(err) {
			// A host reboot erases tmpfs roles but preserves the original Job
			// journal. Only this locked operation may rebuild its pre-cutover
			// projection; this never starts services or initializes protection.
			before, err := b.migrationMetadata(ctx, state, r.SecretMigration.Intent.BeforeResources, true)
			if err != nil || before.Identity != r.SecretMigration.Intent.Before || before.Policy != r.SecretMigration.Intent.ExpectedPolicy {
				return errComposeMigration
			}
			b.maintenance = &hostMaintenance{Version: 1, OperationID: op, Interrupt: r.SecretMigration.Intent.Interrupt, Active: true}
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
		if err != nil || gate == nil || !gate.Active || gate.OperationID != op || gate.Interrupt != r.SecretMigration.Intent.Interrupt {
			return errComposeMigration
		}
		b.maintenance = &hostMaintenance{Version: 1, OperationID: op, Interrupt: gate.Interrupt, Active: true}
	}
	if err := b.Stop(ctx); err != nil {
		return err
	}
	return b.VerifyStopped(ctx)
}

func (b *Backend) migrationCatalog(r lifecycle.OperationRecord, rollback bool) error {
	resources := r.SecretMigration.Intent.Plan.Resources
	if rollback {
		resources = r.SecretMigration.Intent.BeforeResources
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

func (b *Backend) startMigrationServing(ctx context.Context, r lifecycle.OperationRecord) error {
	if err := b.stopMigrationCandidate(ctx); err != nil {
		return err
	}
	rollback := r.SecretMigration.RollbackRequested
	if err := b.migrationCatalog(r, rollback); err != nil {
		return err
	}
	b.maintenance = &hostMaintenance{Version: 1, OperationID: lifecycle.MigrationOperationUUID(r.ID), Interrupt: r.SecretMigration.Intent.Interrupt, Active: true}
	b.initialApplicationMaintenance = lifecycle.MigrationOperationUUID(r.ID)
	defer func() { b.initialApplicationMaintenance = "" }()
	if err := b.Restart(ctx); err != nil {
		return err
	}
	want := r.SecretMigration.Intent.Policy
	if rollback {
		want = r.SecretMigration.Intent.ExpectedPolicy
	}
	actual, err := b.ApplicationPolicy()
	if err != nil || actual != want {
		return errComposeMigration
	}
	return b.Health(ctx)
}

// ApplySecretMigration retains every effect under the installed Compose owner
// lock/journal. It returns pending for manual key protection; no automatic
// initialization, unlock, alternate journal, or archive overlay is available.
func (b *Backend) ApplySecretMigration(ctx context.Context, intent *lifecycle.SecretMigrationIntent, protection string, input []byte) (SecretMigrationStatus, error) {
	var result SecretMigrationStatus
	if protection != "" && protection != "init" && protection != "unlock" || (protection != "") != (len(input) > 0) || len(input) > 65536 {
		return result, errComposeMigration
	}
	err := b.withMigration(ctx, intent, func(ctx context.Context, j *lifecycle.OperationJournal, first lifecycle.OperationRecord) error {
		if first.Maintenance.Released {
			result = composeMigrationStatus(first)
			return nil
		}
		if err := b.cleanupMigrationAdministrators(ctx); err != nil {
			return err
		}
		for step := 0; step < 12; step++ {
			r, _, err := b.migrationRecord(ctx)
			if err != nil {
				return err
			}
			result = composeMigrationStatus(r)
			i := r.SecretMigration.Intent
			if r.State.Terminal() {
				// The normal serving generation already passed its cold restart
				// before this terminal record. Avoid recreating startup gates or
				// disturbing Jobs when only a release ACK was lost.
				if err := b.Health(ctx); err != nil {
					return err
				}
				action := "commit_source"
				if r.State == lifecycle.OperationRolledBack {
					action = "rollback_source"
				}
				if err := b.runMigrationSource(ctx, action); err != nil {
					return err
				}
				if r.State == lifecycle.OperationSucceeded {
					if err := b.publishApplicationBackupRecipient(i.Recipient, false); err != nil {
						return err
					}
				}
				if err := b.fenceMigrationApplication(ctx, r, false); err != nil {
					return err
				}
				if err := b.EndJobMaintenance(ctx, lifecycle.MigrationOperationUUID(r.ID)); err != nil {
					return err
				}
				if err := b.cleanupMigrationCipher(ctx, r); err != nil {
					return err
				}
				if err := b.cleanupMigrationProjection(ctx, r); err != nil {
					return err
				}
				if _, err := j.AcknowledgeMigrationRelease(r.ID, time.Now()); err != nil {
					return err
				}
				result.Released = true
				return nil
			}
			if r.SecretMigration.RollbackRequested {
				if err := b.freezeMigration(ctx, r); err != nil {
					return err
				}
				state, _, err := b.loadRuntime()
				if err != nil {
					return err
				}
				actual, err := b.migrationMetadata(ctx, state, i.BeforeResources, true)
				if err != nil || actual.Identity != i.Before {
					return errComposeMigration
				}
				if err := b.startMigrationServing(ctx, r); err != nil {
					return err
				}
				if _, err := j.CompleteSecretMigration(r.ID, true, time.Now()); err != nil {
					return err
				}
				continue
			}
			switch r.Phase {
			case lifecycle.PhaseAdmitted, lifecycle.PhaseSnapshot:
				if err := b.freezeMigration(ctx, r); err != nil {
					return err
				}
				if err := b.runMigrationSource(ctx, "backup"); err != nil {
					return err
				}
			case lifecycle.PhaseMigrate:
				if err := b.freezeMigration(ctx, r); err != nil {
					return err
				}
				if err := b.startMigrationCandidate(ctx); err != nil {
					return err
				}
				state, err := b.migrationCandidateProtection(ctx, "status", nil)
				if err != nil {
					return err
				}
				if protection != "" {
					state, err = b.migrationCandidateProtection(ctx, protection, input)
					if err != nil {
						return err
					}
					protection = ""
				}
				result.Protection = state
				if state != "unlocked" {
					return nil
				}
				if err := b.runMigrationSource(ctx, "transfer"); err != nil {
					return err
				}
			case lifecycle.PhaseSwitch:
				state, _, err := b.loadRuntime()
				if err != nil {
					return err
				}
				if _, err := b.migrationLayout(ctx, state, i.Plan.Resources, false, &r, "", true); err != nil {
					return err
				}
				if err := b.migrationCatalog(r, false); err != nil {
					return err
				}
				if _, err := j.AdvanceSecretMigration(r.ID, lifecycle.PhaseRestart, time.Now()); err != nil {
					return err
				}
			case lifecycle.PhaseRestart:
				if err := b.startMigrationServing(ctx, r); err != nil {
					return err
				}
				if _, err := j.AdvanceSecretMigration(r.ID, lifecycle.PhaseHealth, time.Now()); err != nil {
					return err
				}
			case lifecycle.PhaseHealth:
				if err := b.Health(ctx); err != nil {
					return err
				}
				if _, err := j.CompleteSecretMigration(r.ID, false, time.Now()); err != nil {
					return err
				}
			default:
				return errComposeMigration
			}
		}
		return errComposeMigration
	})
	if err != nil && result.Operation != "" {
		err = fmt.Errorf("Compose secret migration %s requires original recovery: %w", result.Phase, err)
	}
	return result, err
}

func (b *Backend) RollbackSecretMigration(ctx context.Context) (SecretMigrationStatus, error) {
	err := b.withMigration(ctx, nil, func(ctx context.Context, j *lifecycle.OperationJournal, r lifecycle.OperationRecord) error {
		if r.State.Terminal() {
			return errComposeMigration
		}
		state, _, err := b.loadRuntime()
		if err != nil {
			return err
		}
		actual, err := b.migrationMetadata(ctx, state, r.SecretMigration.Intent.BeforeResources, true)
		if err != nil || actual.Identity != r.SecretMigration.Intent.Before {
			return errComposeMigration
		}
		_, err = j.RequestSecretMigrationRollback(r.ID, time.Now())
		return err
	})
	if err != nil {
		return SecretMigrationStatus{}, err
	}
	return b.ApplySecretMigration(ctx, nil, "", nil)
}
