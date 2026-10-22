//go:build linux

package management

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/host/lifecycle"
	hostpolicy "loki/internal/host/policy"
	"loki/internal/tools"
)

func TestFullMigrationRejectsUnservableMetadataBeforeAdmission(t *testing.T) {
	store := migrationCandidateStore(t)
	intent := fullMigrationIntentFixture(t, store)
	resource := intent.Plan.Resources[0]
	for index := 0; index < 17000; index++ {
		resource.Environments = append(resource.Environments, fmt.Sprintf("env-%05d-%s", index, strings.Repeat("a", 50)))
	}
	c, contract, err := store.fullApplicationInputs()
	if err != nil {
		t.Fatal(err)
	}
	intent.Plan.Resources, err = controlpolicy.CanonicalApplications([]controlpolicy.ApplicationResource{resource}, contract.Environment)
	if err != nil {
		t.Fatal(err)
	}
	policy, err := hostpolicy.CompileApplications(c, contract, intent.Plan.Resources)
	if err != nil {
		t.Fatal(err)
	}
	intent.Policy = policy.Digest()
	err = store.WithSecretMigrationJournal(t.Context(), lifecycle.OperationJournalOptions{}, func(j *lifecycle.OperationJournal) error {
		if _, err := j.BeginSecretMigration(intent, time.Now()); err == nil {
			t.Fatal("oversized candidate metadata admitted before the service could read it")
		}
		if _, found, err := j.Active(); err != nil || found {
			t.Fatal("failed preflight left an admitted host operation", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(store.Root, "transaction.json")); !os.IsNotExist(err) {
		t.Fatal("failed metadata preflight published a transaction", err)
	}
}

func fullMigrationIntentFixture(t *testing.T, store Store) lifecycle.SecretMigrationIntent {
	t.Helper()
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	core, err := store.Generation(state.Installed["runtime-core"].Artifact)
	if err != nil {
		t.Fatal(err)
	}
	contract, err := os.ReadFile("../../packaging/images/config/execution-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(core, "contract.json"), contract, 0600); err != nil {
		t.Fatal(err)
	}
	c, execution, err := store.fullApplicationInputs()
	if err != nil {
		t.Fatal(err)
	}
	resource := controlpolicy.ApplicationResource{ID: "123e4567-e89b-42d3-a456-426614174501", Alias: "web", Repository: "owner/repo", Network: "none", Principals: []controlpolicy.ApplicationGrant{{Principal: "agent", Execute: true}}, Environments: []string{"common"}, Keys: map[string]string{"TOKEN": "secret"}}
	resources, err := controlpolicy.CanonicalApplications([]controlpolicy.ApplicationResource{resource}, execution.Environment)
	if err != nil {
		t.Fatal(err)
	}
	before, err := hostpolicy.CompileApplications(c, execution, state.ApplicationResources)
	if err != nil {
		t.Fatal(err)
	}
	after, err := hostpolicy.CompileApplications(c, execution, resources)
	if err != nil {
		t.Fatal(err)
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	return lifecycle.SecretMigrationIntent{
		Plan:                appvalues.MigrationPlan{Version: 1, Revision: 1, Values: 1, Resources: resources, Mappings: []appvalues.MigrationProfileMapping{{Profile: "web", Resource: resource.ID, Environment: "common", Keys: []appvalues.MigrationKeyMapping{{Source: "TOKEN", Name: "TOKEN", Kind: "secret"}}}}},
		Before:              appvalues.ApplicationIdentity{Version: 1, UID: 10006, GID: 10006, AgentUID: 10000, MetadataGID: 10001, DeploymentID: "deployment-" + strings.Repeat("a", 32), RunnerID: "123e4567-e89b-42d3-a456-426614174502", ActiveGeneration: strings.Repeat("a", 32)},
		CandidateGeneration: strings.Repeat("b", 32), ExpectedPolicy: before.Digest(), Policy: after.Digest(), Mode: "managed-full",
		SourceRoot: "/source-vault", IdentityFile: "/app-store/deployment.json", StateRoot: "/app-store", ServiceStateRoot: "/var/lib/loki/secrets", RuntimeRoot: "/run/loki/secrets", Binary: "/opt/loki/bin/devtools", Recipient: id.Recipient().String(), BackupPath: "/operator/source.age",
	}
}

func TestFullMigrationSharesExistingTransactionAndSelectionLock(t *testing.T) {
	store := lifecycleStore(t)
	intent := fullMigrationIntentFixture(t, store)
	now := time.Now()
	var original lifecycle.OperationRecord
	var escaped *lifecycle.OperationJournal
	err := store.WithSecretMigrationJournal(t.Context(), lifecycle.OperationJournalOptions{}, func(j *lifecycle.OperationJournal) error {
		escaped = j
		var err error
		original, err = j.BeginSecretMigration(intent, now)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := escaped.List(); err == nil {
		t.Fatal("migration journal remained writable after owner lock release")
	}
	transaction, err := store.readTransaction()
	if err != nil || transaction.Action != "secret_migration" || transaction.SecretMigration.ID != original.ID || transaction.Phase != tools.Staged {
		t.Fatal("did not retain canonical operation inside existing transaction", err)
	}
	if _, err := os.Lstat(filepath.Join(store.Root, "operations")); !errors.Is(err, os.ErrNotExist) {
		t.Fatal("created competing full lifecycle journal", err)
	}
	state, err := store.Load()
	if err != nil || len(state.ApplicationResources) != 0 {
		t.Fatal("admission changed active grants before verified selection", err)
	}
	if err := store.RequireMutable(); err == nil {
		t.Fatal("ordinary full mutation ignored source migration")
	}
	if err := store.Recover(); err == nil {
		t.Fatal("generic tools recovery committed unfinished source migration")
	}
	backend := &lifecycleBackend{running: map[string]bool{}}
	if _, err := store.ReconcileFull(t.Context(), backend); err == nil || len(backend.prepared) != 0 {
		t.Fatal("ordinary start bypassed the original migration", err)
	}
	if err := store.StopFull(t.Context(), backend); err == nil {
		t.Fatal("ordinary stop bypassed the original migration")
	}
	err = store.WithSecretMigrationJournal(t.Context(), lifecycle.OperationJournalOptions{}, func(j *lifecycle.OperationJournal) error {
		r, err := j.BeginSecretMigration(intent, now)
		if err != nil || r.ID != original.ID {
			t.Fatal("original full operation not replayable", err)
		}
		if _, err := j.CompleteSecretMigration(r.ID, true, now); err != nil {
			return err
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	transaction, err = store.readTransaction()
	if err != nil || transaction.Phase != tools.Staged {
		t.Fatal("released owner admission before source/Job fence ACK", err)
	}
	err = store.WithSecretMigrationJournal(t.Context(), lifecycle.OperationJournalOptions{}, func(j *lifecycle.OperationJournal) error {
		_, err := j.AcknowledgeMigrationRelease(original.ID, now)
		return err
	})
	if err != nil {
		t.Fatal(err)
	}
	transaction, err = store.readTransaction()
	if err != nil || transaction.Phase != tools.Aborted {
		t.Fatal("verified rollback/release did not close existing owner transaction", err)
	}
	if err := store.RequireMutable(); err != nil {
		t.Fatal("closed migration left ordinary owner blocked", err)
	}
}

func TestFullMigrationRecoversPublishedRecordWithoutSecondAuthority(t *testing.T) {
	store := lifecycleStore(t)
	intent := fullMigrationIntentFixture(t, store)
	lost := errors.New("synthetic lost full admission acknowledgement")
	opts := lifecycle.OperationJournalOptions{Failpoint: func(name string) error {
		if name == "after_publish:applying:admitted" {
			return lost
		}
		return nil
	}}
	err := store.WithSecretMigrationJournal(t.Context(), opts, func(j *lifecycle.OperationJournal) error {
		_, err := j.BeginSecretMigration(intent, time.Now())
		return err
	})
	if !errors.Is(err, lost) {
		t.Fatal(err)
	}
	saved, err := store.readTransaction()
	if err != nil || saved.SecretMigration == nil {
		t.Fatal("lost already-published original intent", err)
	}
	err = store.WithSecretMigrationJournal(t.Context(), lifecycle.OperationJournalOptions{}, func(j *lifecycle.OperationJournal) error {
		r, err := j.BeginSecretMigration(intent, time.Now())
		if err != nil || r.ID != saved.SecretMigration.ID {
			t.Fatal("full replay manufactured another operation", err)
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}
