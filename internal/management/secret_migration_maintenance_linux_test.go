//go:build linux

package management

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loki/internal/host/lifecycle"
)

func TestFullMigrationMaintainsOriginalFenceThroughStoppedServices(t *testing.T) {
	store := lifecycleStore(t)
	intent := fullMigrationIntentFixture(t, store)
	b := &DockerFullBackend{Store: store}
	reservation := DeploymentReservation{ID: "deployment-" + strings.Repeat("c", 32), PlanDigest: strings.Repeat("d", 64), Services: []string{"launcher"}}
	var escaped context.Context
	err := store.WithSecretMigrationJournal(t.Context(), lifecycle.OperationJournalOptions{}, func(j *lifecycle.OperationJournal) error {
		r, err := j.BeginSecretMigration(intent, time.Now())
		if err != nil {
			return err
		}
		if _, err := b.migrationMaintenance(t.Context()); err == nil {
			t.Fatal("ordinary service effect bypassed canonical migration")
		}
		return b.WithSecretMigrationMaintenance(t.Context(), j, func(ctx context.Context) error {
			escaped = ctx
			delegated, err := b.migrationMaintenance(ctx)
			if err != nil || !delegated.Migration || delegated.OperationID != lifecycle.MigrationOperationUUID(r.ID) || delegated.OperationID == deploymentStopUUID(reservation) {
				t.Fatal("stop replaced original host identity", delegated, err)
			}
			record := fullDockerRecord{Schema: 1, Reservation: reservation, Worker: "/opt/loki/bin/loki", StopMaintenance: delegated, Order: []string{"launcher"}, Specs: []fullContainerSpec{{Service: "launcher", Name: b.containerName(reservation, "launcher"), Image: "loki-fixture@sha256:" + strings.Repeat("e", 64), Command: []string{"/opt/loki/bin/loki"}}}}
			dir, err := b.deploymentDirectory(reservation)
			if err != nil {
				return err
			}
			if err := atomicJSON(filepath.Join(dir, "services.json"), record); err != nil {
				return err
			}
			if err := b.endStopMaintenance(ctx, reservation); err != nil {
				t.Fatal("stopped service did not retain migration fence", err)
			}
			retained, err := b.record(reservation)
			if err != nil || retained.StopMaintenance == nil || *retained.StopMaintenance != *delegated {
				t.Fatal("stop discarded original fence", err)
			}
			if err := b.ReleaseSecretMigrationMaintenance(ctx, reservation); err == nil {
				t.Fatal("nonterminal migration released Job admission")
			}
			if err := b.endStopMaintenance(t.Context(), reservation); err == nil {
				t.Fatal("unscoped caller released migration fence")
			}
			foreign := &DockerFullBackend{Store: Store{Root: t.TempDir()}}
			if _, err := foreign.migrationMaintenance(ctx); err == nil {
				t.Fatal("another store used delegated migration scope")
			}
			if _, err := j.CompleteSecretMigration(r.ID, true, time.Now()); err != nil {
				return err
			}
			if _, err := b.migrationMaintenance(ctx); err != nil {
				t.Fatal("durable terminal result prematurely revoked release owner", err)
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.migrationMaintenance(escaped); err == nil {
		t.Fatal("escaped capability survived callback/store-lock release")
	}
}
