//go:build linux

package management

import (
	"context"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"loki/internal/host/lifecycle"
	"loki/internal/tools"
)

func migrationCandidateStore(t *testing.T) Store {
	t.Helper()
	store := lifecycleStore(t)
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	installed := ownedFixtureTarget(t, store, LocalTarget(tools.Full), "secrets", "runtime-core")
	state.Installed["secrets"] = installed
	generation, err := store.Generation(installed.Artifact)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(generation, "program"), []byte("fixture"), 0755); err != nil {
		t.Fatal(err)
	}
	payload := FullPayload{Schema: 1, Module: "secrets", Release: Release, Target: installed.Artifact.Target, Images: map[string]string{}, Programs: map[string]string{"secret-broker": "program"}, Assets: map[string]string{}}
	if err := atomicJSON(filepath.Join(generation, "full-runtime.json"), payload); err != nil {
		t.Fatal(err)
	}
	state.Config.Tools = append(state.Config.Tools, tools.Selection{ID: "secrets", Enabled: true})
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestFullMigrationCandidateUsesOriginalRegistryAndRejectsTerminalRestart(t *testing.T) {
	store := migrationCandidateStore(t)
	intent := fullMigrationIntentFixture(t, store)
	b := &DockerFullBackend{Store: store}
	var escaped context.Context
	err := store.WithSecretMigrationJournal(t.Context(), lifecycle.OperationJournalOptions{}, func(j *lifecycle.OperationJournal) error {
		record, err := j.BeginSecretMigration(intent, time.Now())
		if err != nil {
			return err
		}
		return b.WithSecretMigrationMaintenance(t.Context(), j, func(ctx context.Context) error {
			escaped = ctx
			reserved, err := b.ReserveSecretMigrationCandidate(ctx)
			if err != nil {
				return err
			}
			if reserved.ID != "deployment-"+record.ID || !reflect.DeepEqual(reserved.Services, []string{"secrets"}) || len(reserved.Artifacts) != 2 {
				t.Fatal("candidate changed original operation or acquired unrelated roles", reserved)
			}
			for _, artifact := range reserved.Artifacts {
				if artifact.Module != "runtime-core" && artifact.Module != "secrets" {
					t.Fatal("candidate pinned an unrelated tool")
				}
			}
			replayed, err := b.ReserveSecretMigrationCandidate(ctx)
			if err != nil || !reflect.DeepEqual(replayed, reserved) {
				t.Fatal("candidate reservation ACK loss allocated another deployment", err)
			}
			registry, err := store.deploymentRegistry()
			if err != nil || len(registry.Deployments) != 1 {
				t.Fatal("candidate created another registry or duplicate reservation", err)
			}
			if _, err := j.CompleteSecretMigration(record.ID, true, time.Now()); err != nil {
				return err
			}
			if _, err := b.ReserveSecretMigrationCandidate(ctx); err == nil {
				t.Fatal("terminal migration created a candidate")
			}
			if err := b.StartSecretMigrationCandidate(ctx, reserved); err == nil {
				t.Fatal("terminal migration restarted a private broker")
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := b.ReserveSecretMigrationCandidate(escaped); err == nil {
		t.Fatal("expired owner scope changed the deployment registry")
	}
}
