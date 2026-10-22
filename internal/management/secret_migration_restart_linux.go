//go:build linux

package management

import (
	"context"
	"crypto/sha256"
	"fmt"
	"time"

	"loki/internal/host/lifecycle"
)

// RestartSecretMigration resumes the original selected generation using normal
// full service preparation/readiness. The original Job and app gates remain
// closed; no private candidate capability is installed in serving roles.
func (b *DockerFullBackend) RestartSecretMigration(ctx context.Context) (FullLifecycleReport, error) {
	var report FullLifecycleReport
	operation, err := b.migrationRecord(ctx)
	if err != nil {
		return report, err
	}
	if operation.State != lifecycle.OperationApplying || operation.Phase != lifecycle.PhaseRestart && operation.Phase != lifecycle.PhaseHealth {
		return report, fmt.Errorf("restart requires the original selected migration checkpoint")
	}
	transaction, err := b.Store.readTransaction()
	if err != nil || transaction == nil || transaction.SecretMigration == nil || transaction.SecretMigration.ID != operation.ID {
		return report, fmt.Errorf("original serving intent is unavailable")
	}
	current, err := b.Store.Load()
	if err != nil || snapshotDigest(current) != snapshotDigest(transaction.Candidate) {
		return report, fmt.Errorf("selected activation differs from the original migration")
	}
	identity, err := b.inspectApplicationIdentity(ctx, false)
	if err != nil {
		return report, err
	}
	want := operation.SecretMigration.Intent.Before
	want.Version, want.ActiveGeneration, want.PolicySHA256 = 2, operation.SecretMigration.Intent.CandidateGeneration, operation.SecretMigration.Intent.Policy
	if identity != want {
		return report, fmt.Errorf("actual selector differs from the original serving generation/policy")
	}
	topology, err := b.Store.FullTopology()
	if err != nil {
		return report, err
	}
	layouts, err := topology.Resources.Layouts()
	if err != nil {
		return report, err
	}
	if layouts.Launcher != nil {
		layouts.Launcher["InitialMaintenanceOperation"] = lifecycle.MigrationOperationUUID(operation.ID)
		layouts.Launcher["InitialMaintenanceInterrupt"] = operation.Maintenance.Interrupt
	}
	identityDigest := sha256.Sum256([]byte("loki-secret-migration-serving\x00" + operation.PlanID))
	serving, err := b.Store.reserveDeploymentPlanLocked(topology.Resources.Plan, fmt.Sprintf("deployment-%x", identityDigest[:16]))
	if err != nil {
		return report, err
	}
	report.Deployment = &serving
	if err := b.Prepare(ctx, serving, topology, layouts); err != nil {
		return report, err
	}
	if err := b.Start(ctx, serving); err != nil {
		return report, err
	}
	scope := ctx.Value(fullMigrationMaintenanceKey{}).(*fullMigrationMaintenanceScope)
	if operation.Phase == lifecycle.PhaseRestart {
		if _, err := scope.journal.AdvanceSecretMigration(operation.ID, lifecycle.PhaseHealth, time.Now()); err != nil {
			return report, err
		}
	}
	report.Observation, err = b.Observe(ctx, serving)
	if err != nil || !report.Observation.Ready {
		return report, fmt.Errorf("original selected services require health recovery")
	}
	if _, err := scope.journal.CompleteSecretMigration(operation.ID, false, time.Now()); err != nil {
		return report, err
	}
	return report, nil
}
