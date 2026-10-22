package lifecycle

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
)

func migrationIntentFixture(t *testing.T, root string) SecretMigrationIntent {
	t.Helper()
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	resources, err := controlpolicy.CanonicalApplications([]controlpolicy.ApplicationResource{{
		ID: "123e4567-e89b-42d3-a456-426614174471", Alias: "web", Repository: "owner/repo", Network: "none",
		Principals: []controlpolicy.ApplicationGrant{{Principal: "agent", Execute: true}}, Environments: []string{"common"}, Keys: map[string]string{"TOKEN": "secret"},
	}}, nil)
	if err != nil {
		t.Fatal(err)
	}
	i := SecretMigrationIntent{
		Plan:                appvalues.MigrationPlan{Version: 1, Revision: 4, Resources: resources, Values: 1, Mappings: []appvalues.MigrationProfileMapping{{Profile: "web", Resource: resources[0].ID, Environment: "common", Keys: []appvalues.MigrationKeyMapping{{Source: "TOKEN", Name: "TOKEN", Kind: "secret"}}}}},
		Before:              appvalues.ApplicationIdentity{Version: 1, UID: 10006, GID: 10006, AgentUID: 10000, MetadataGID: 10001, DeploymentID: "deployment-" + strings.Repeat("a", 32), RunnerID: "123e4567-e89b-42d3-a456-426614174472", ActiveGeneration: strings.Repeat("a", 32)},
		CandidateGeneration: strings.Repeat("b", 32), ExpectedPolicy: strings.Repeat("c", 64), Policy: strings.Repeat("d", 64), Mode: "native",
		SourceRoot: filepath.Join(root, "source"), IdentityFile: filepath.Join(root, "identity.json"), StateRoot: filepath.Join(root, "state"), ServiceStateRoot: "/var/lib/loki-go/secrets", RuntimeRoot: "/run/loki-go/secret-migration", Binary: "/opt/loki/bin/devtools", Recipient: id.Recipient().String(), BackupPath: filepath.Join(root, "source.age"),
	}
	if !i.Valid() {
		t.Fatal("invalid migration intent fixture")
	}
	return i
}

func TestSecretMigrationOriginalCheckpointsAndTerminalFence(t *testing.T) {
	root := privateLifecycleRoot(t)
	lock, err := AcquireOperationLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	open := func() *OperationJournal {
		t.Helper()
		j, err := OpenOperationJournal(root, lock, OperationJournalOptions{})
		if err != nil {
			t.Fatal(err)
		}
		return j
	}
	j := open()
	now := time.Now()
	intent := migrationIntentFixture(t, root)
	first, err := j.BeginSecretMigration(intent, now)
	if err != nil || !first.Valid() {
		t.Fatal("admit", err)
	}
	first.SecretMigration.Intent.Plan.Resources[0].Keys["TOKEN"] = "variable"
	replay, err := j.BeginSecretMigration(intent, now)
	if err != nil || replay.ID != first.ID || replay.SecretMigration.Intent.Plan.Resources[0].Keys["TOKEN"] != "secret" {
		t.Fatal("caller mutated admitted metadata", err)
	}
	changed := intent
	changed.Interrupt = true
	if _, err := j.BeginSecretMigration(changed, now); err == nil {
		t.Fatal("changed original interruption choice")
	}
	if _, err := j.AdvanceSecretMigration(first.ID, PhaseSwitch, now); err == nil {
		t.Fatal("skipped source freeze and recoverable backup")
	}
	if _, err := j.AdvanceSecretMigration(first.ID, PhaseSnapshot, now); err != nil {
		t.Fatal(err)
	}
	if _, err := j.CheckpointMigrationArchive(first.ID, strings.Repeat("e", 64), 4096, true, now); err == nil {
		t.Fatal("acknowledged an archive without its prior ciphertext checkpoint")
	}
	if _, err := j.CheckpointMigrationArchive(first.ID, strings.Repeat("e", 64), 4096, false, now); err != nil {
		t.Fatal(err)
	}
	if _, err := j.AdvanceSecretMigration(first.ID, PhaseMigrate, now); err == nil {
		t.Fatal("admitted target writes before archive publication")
	}
	if _, err := j.CheckpointMigrationArchive(first.ID, strings.Repeat("e", 64), 4096, true, now); err != nil {
		t.Fatal(err)
	}
	if _, err := j.CheckpointMigrationArchive(first.ID, strings.Repeat("f", 64), 4096, false, now); err == nil {
		t.Fatal("substituted a published original source backup")
	}
	if _, err := j.AdvanceSecretMigration(first.ID, PhaseMigrate, now); err != nil {
		t.Fatal(err)
	}
	j = open()
	if _, err := j.AdvanceSecretMigration(first.ID, PhaseMigrate, now); err != nil {
		t.Fatal("lost phase checkpoint ACK", err)
	}
	w := MigrationWriteCheckpoint{Resource: intent.Plan.Resources[0].ID, Environment: "common", Action: "set_secret", Name: "TOKEN", Revision: strings.Repeat("a", 64)}
	w.Request = MigrationWriteRequest(first.ID, w)
	if _, err := j.CheckpointMigrationWrite(first.ID, w, now); err != nil {
		t.Fatal(err)
	}
	wrong := w
	wrong.Revision = strings.Repeat("b", 64)
	if _, err := j.CheckpointMigrationWrite(first.ID, wrong, now); err == nil {
		t.Fatal("rebased original slot request to a later revision")
	}
	if _, err := j.AdvanceSecretMigration(first.ID, PhaseSwitch, now); err == nil {
		t.Fatal("selected generation before writes/comparison completed")
	}
	if _, err := j.CompleteMigrationWrite(first.ID, w.Request, now); err != nil {
		t.Fatal(err)
	}
	if _, err := open().CheckpointMigrationWrite(first.ID, w, now); err != nil {
		t.Fatal("lost write completion ACK", err)
	}
	j = open()
	binding := appvalues.MigrationComparisonBinding{Operation: MigrationOperationUUID(first.ID), Attempt: "123e4567-e89b-42d3-a456-426614174473", Policy: intent.Policy, Resource: w.Resource, Environment: "common", Names: []string{"TOKEN"}, Runner: "123e4567-e89b-42d3-a456-426614174474", Instance: "123e4567-e89b-42d3-a456-426614174475", RootID: strings.Repeat("a", 64), VaultID: strings.Repeat("b", 32), Execution: strings.Repeat("c", 32), Input: strings.Repeat("d", 32), CorrelationID: strings.Repeat("e", 32), Revision: 1}
	if _, err := j.CompleteMigrationComparison(first.ID, binding.Attempt, 1, now); err == nil {
		t.Fatal("accepted comparison without original private binding")
	}
	if _, err := j.CheckpointMigrationComparison(first.ID, binding, now); err != nil {
		t.Fatal(err)
	}
	if _, err := j.AdvanceSecretMigration(first.ID, PhaseSwitch, now); err == nil {
		t.Fatal("selected before private match acknowledgement")
	}
	if _, err := j.CompleteMigrationComparison(first.ID, binding.Attempt, 2, now); err == nil {
		t.Fatal("accepted wrong private comparison count")
	}
	if _, err := j.CompleteMigrationComparison(first.ID, binding.Attempt, 1, now); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []OperationPhase{PhaseSwitch, PhaseRestart, PhaseHealth} {
		if _, err := j.AdvanceSecretMigration(first.ID, phase, now); err != nil {
			t.Fatal(phase, err)
		}
	}
	if _, err := j.CompleteSecretMigration(first.ID, true, now); err == nil {
		t.Fatal("silently rolled a selected generation back to its old authority")
	}
	if _, err := j.CompleteSecretMigration(first.ID, false, now); err != nil {
		t.Fatal(err)
	}
	j = open()
	if _, found, err := j.Active(); err != nil || !found {
		t.Fatal("terminal host checkpoint lost its pending source/Job fence", err)
	}
	if _, err := j.Begin(OperationApply, operationPlanFixture(t, now), now); err == nil {
		t.Fatal("admitted unrelated apply before original release acknowledgement")
	}
	if err := (&TransactionEngine{}).recoverInterrupted(context.Background(), j); !errors.Is(err, errSecretMigration) {
		t.Fatal("generic recovery touched migration fence", err)
	}
	if _, err := j.AcknowledgeMigrationRelease(first.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, found, err := open().Active(); err != nil || found {
		t.Fatal("released completed operation still active", err)
	}
	if replay, err := open().BeginSecretMigration(intent, now); err != nil || replay.ID != first.ID || replay.State != OperationSucceeded {
		t.Fatal("lost final response created a new migration", err)
	}
}

func TestSecretMigrationAdmissionPublicationLossAndRollback(t *testing.T) {
	root := privateLifecycleRoot(t)
	lock, err := AcquireOperationLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	now := time.Now()
	intent := migrationIntentFixture(t, root)
	lost := errors.New("synthetic lost admission acknowledgement")
	j, err := OpenOperationJournal(root, lock, OperationJournalOptions{Failpoint: func(name string) error {
		if name == "after_publish:applying:admitted" {
			return lost
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.BeginSecretMigration(intent, now); !errors.Is(err, lost) {
		t.Fatal(err)
	}
	j, err = OpenOperationJournal(root, lock, OperationJournalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	r, err := j.BeginSecretMigration(intent, now)
	if err != nil || !r.Valid() {
		t.Fatal("recover published original admission", err)
	}
	if _, found, err := j.RecoverInterrupted(context.Background(), func(context.Context, OperationRecord) error {
		t.Fatal("generic volume restore received encrypted source backup")
		return nil
	}, now); !found || !errors.Is(err, errSecretMigration) {
		t.Fatal("generic recovery did not fail closed", err)
	}
	if _, err := j.CompleteSecretMigration(r.ID, false, now); err == nil {
		t.Fatal("completed without target selection/health")
	}
	if _, err := j.CompleteSecretMigration(r.ID, true, now); err != nil {
		t.Fatal(err)
	}
	if _, err := j.AcknowledgeMigrationRelease(r.ID, now); err != nil {
		t.Fatal(err)
	}
	intent.CandidateGeneration = strings.Repeat("f", 32)
	if next, err := j.BeginSecretMigration(intent, now); err != nil || next.ID == r.ID {
		t.Fatal("explicit later plan failed after verified rollback/release", err)
	}
}
