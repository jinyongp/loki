//go:build linux

package management

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"loki/internal/host/lifecycle"
)

// RollbackSecretMigration chooses recovery of the unchanged original source.
// It never imports the encrypted archive over current platform credentials.
// Selection of a candidate makes this operation unavailable.
func (b *DockerFullBackend) RollbackSecretMigration(ctx context.Context) (SecretMigrationStatus, error) {
	err := b.Store.WithSecretMigrationJournal(ctx, lifecycle.OperationJournalOptions{}, func(j *lifecycle.OperationJournal) error {
		r, found, err := j.Active()
		if err != nil || !found || r.SecretMigration == nil {
			return fmt.Errorf("no original secret migration requires rollback")
		}
		// A selector rename may have succeeded before its phase ACK. Check
		// actual metadata before recording an irreversible recovery choice.
		identity, err := b.inspectApplicationIdentity(ctx, false)
		if err != nil || identity != r.SecretMigration.Intent.Before {
			return fmt.Errorf("original selector is no longer active; recover the selected migration")
		}
		_, err = j.RequestSecretMigrationRollback(r.ID, time.Now())
		return err
	})
	if err != nil {
		return SecretMigrationStatus{}, err
	}
	return b.ApplySecretMigration(ctx, nil, "", nil)
}

func (b *DockerFullBackend) restartOriginalSecretMigration(ctx context.Context, operation lifecycle.OperationRecord, candidate DeploymentReservation) error {
	if operation.State != lifecycle.OperationApplying || !operation.SecretMigration.RollbackRequested {
		return fmt.Errorf("original rollback choice is required")
	}
	t, err := b.Store.readTransaction()
	if err != nil || t == nil {
		return fmt.Errorf("original rollback activation is unavailable")
	}
	identity, err := b.inspectApplicationIdentity(ctx, false)
	before := operation.SecretMigration.Intent.Before
	// Ordinary preparation upgrades legacy metadata to its policy-bound form;
	// this does not change the original generation or provider files.
	upgraded := before
	upgraded.Version, upgraded.PolicySHA256 = 2, operation.SecretMigration.Intent.ExpectedPolicy
	if err != nil || identity != before && identity != upgraded {
		return fmt.Errorf("rollback cannot replace an actually selected candidate")
	}
	current, err := b.Store.Load()
	if err != nil || snapshotDigest(current) != snapshotDigest(t.Previous) {
		return fmt.Errorf("rollback requires unchanged original activation")
	}
	if err := b.Stop(ctx, candidate); err != nil {
		return err
	}
	digest := sha256.Sum256([]byte("loki-secret-migration-serving\x00" + operation.PlanID))
	servingID := fmt.Sprintf("deployment-%x", digest[:16])
	reservations, err := b.Store.Deployments()
	if err != nil {
		return err
	}
	for _, previous := range reservations {
		if previous.ID == servingID || previous.ID == candidate.ID || b.ConfirmAbsent(ctx, previous) == nil {
			continue
		}
		if err := b.fenceAndStopSecretMigrationPredecessor(ctx, previous); err != nil {
			return err
		}
	}
	topology, err := b.Store.FullTopology()
	if err != nil {
		return err
	}
	layouts, err := topology.Resources.Layouts()
	if err != nil {
		return err
	}
	if layouts.Launcher != nil {
		layouts.Launcher["InitialMaintenanceOperation"] = lifecycle.MigrationOperationUUID(operation.ID)
		layouts.Launcher["InitialMaintenanceInterrupt"] = operation.Maintenance.Interrupt
	}
	serving, err := b.Store.reserveDeploymentPlanLocked(topology.Resources.Plan, servingID)
	if err != nil {
		return err
	}
	if err := b.Prepare(ctx, serving, topology, layouts); err != nil {
		return err
	}
	if err := b.Start(ctx, serving); err != nil {
		return err
	}
	if err := b.FenceSecretMigrationApplication(ctx, serving, true); err != nil {
		return err
	}
	observation, err := b.Observe(ctx, serving)
	if err != nil || !observation.Ready {
		return fmt.Errorf("original services require health recovery before rollback release")
	}
	scope := ctx.Value(fullMigrationMaintenanceKey{}).(*fullMigrationMaintenanceScope)
	_, err = scope.journal.CompleteSecretMigration(operation.ID, true, time.Now())
	return err
}

func (b *DockerFullBackend) fenceAndStopSecretMigrationPredecessor(ctx context.Context, previous DeploymentReservation) error {
	ids, err := b.containerIDs(ctx, previous)
	if err != nil {
		return err
	}
	for _, id := range ids {
		container, err := b.inspectContainer(ctx, id)
		if err != nil {
			return err
		}
		if err := b.requireParent(previous, container); err != nil {
			return err
		}
		if container.State.Running && container.Config.Labels[fullServiceLabel] == "secrets" {
			if err := b.FenceSecretMigrationApplication(ctx, previous, true); err != nil {
				return err
			}
		}
	}
	return b.Stop(ctx, previous)
}
