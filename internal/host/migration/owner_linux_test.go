//go:build linux

package migration

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"loki/internal/admin"
	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/host/lifecycle"
	secret "loki/modules/secrets"
)

func TestSourceOwnerBackupRecoversOriginalPublicationAndRejectsMissingAcknowledgedArchive(t *testing.T) {
	ctx := context.Background()
	root := t.TempDir()
	source := secret.Controller{StateDirectory: filepath.Join(root, "source")}
	if _, err := source.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := source.CreateProfile(ctx, "app"); err != nil {
		t.Fatal(err)
	}
	if _, err := source.SetSecret(ctx, "app", "TOKEN", "synthetic-owner-newline\nvalue", false); err != nil {
		t.Fatal(err)
	}
	resource := controlpolicy.ApplicationResource{ID: "123e4567-e89b-42d3-a456-426614174531", Alias: "app", Repository: "owner/repo", Network: "none", Environments: []string{"common"}, Keys: map[string]string{"TOKEN": "secret"}, Principals: []controlpolicy.ApplicationGrant{{Principal: "agent", Execute: true}}}
	plan, err := source.PrepareApplicationMigration(ctx, []secret.MigrationProfileMapping{{Profile: "app", Resource: resource.ID, Environment: "common", Keys: []secret.MigrationKeyMapping{{Source: "TOKEN"}}}}, []controlpolicy.ApplicationResource{resource}, nil)
	if err != nil {
		t.Fatal(err)
	}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	archiveDir := filepath.Join(root, "backup")
	if err := os.Mkdir(archiveDir, 0700); err != nil {
		t.Fatal(err)
	}
	intent := lifecycle.SecretMigrationIntent{Plan: plan, Before: appvalues.ApplicationIdentity{Version: 1, UID: 10006, GID: 10006, AgentUID: 10000, MetadataGID: 10001, DeploymentID: "deployment-" + strings.Repeat("a", 32), RunnerID: "123e4567-e89b-42d3-a456-426614174532", ActiveGeneration: strings.Repeat("a", 32)}, CandidateGeneration: strings.Repeat("b", 32), ExpectedPolicy: strings.Repeat("a", 64), Policy: strings.Repeat("a", 64), Mode: "native", SourceRoot: source.StateDirectory, IdentityFile: filepath.Join(root, "identity.json"), StateRoot: filepath.Join(root, "state"), ServiceStateRoot: "/var/lib/loki/secrets", RuntimeRoot: "/run/loki/secrets", Binary: "/opt/loki/bin/devtools", Recipient: identity.Recipient().String(), BackupPath: filepath.Join(archiveDir, "source.age")}
	journalRoot := filepath.Join(root, "journal")
	if _, err := lifecycle.EnsureFileStore(journalRoot); err != nil {
		t.Fatal(err)
	}
	lock, err := lifecycle.AcquireOperationLock(journalRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	lost := errors.New("synthetic original archive proof publication acknowledgement loss")
	snapshots := 0
	j, err := lifecycle.OpenOperationJournal(journalRoot, lock, lifecycle.OperationJournalOptions{Failpoint: func(name string) error {
		if name == "after_publish:applying:snapshot" {
			snapshots++
			if snapshots == 2 {
				return lost
			}
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	r, err := j.BeginSecretMigration(intent, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	owner := SourceOwner{Journal: j, ID: r.ID}
	if err := owner.Backup(ctx); !errors.Is(err, lost) {
		t.Fatal("archive proof loss was not exercised", err)
	}
	if _, err := os.Lstat(intent.BackupPath); !os.IsNotExist(err) {
		t.Fatal("published archive before proof acknowledgement", err)
	}
	j, err = lifecycle.OpenOperationJournal(journalRoot, lock, lifecycle.OperationJournalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	owner.Journal = j
	if err := owner.Backup(ctx); err != nil {
		t.Fatal("original backup recovery", err)
	}
	current, found, err := j.Active()
	if err != nil || !found || current.ID != r.ID || current.Phase != lifecycle.PhaseMigrate || !current.SecretMigration.Archive.Published {
		t.Fatal("original archive/phase not checkpointed", err)
	}
	if err := owner.Backup(ctx); err != nil {
		t.Fatal("published archive replay", err)
	}
	if _, err := owner.Session(admin.ApplicationClient{Socket: "/tmp/test-admin.sock", ExpectedUID: 1000}, nil); err == nil {
		t.Fatal("foreign broker UID selected")
	}
	if err := owner.Checkpoint(SourceEvent{Operation: lifecycle.MigrationOperationUUID(r.ID), Type: "write_completed"}); err == nil {
		t.Fatal("malformed helper completion accepted")
	}
	if _, err := source.SetSecret(ctx, "app", "TOKEN", "changed", true); err == nil {
		t.Fatal("source writer escaped pending migration")
	}
	if err := os.Remove(intent.BackupPath); err != nil {
		t.Fatal(err)
	}
	if err := owner.Backup(ctx); err == nil {
		t.Fatal("lost acknowledged archive silently replaced after target writes became possible")
	}
	if _, err := os.Lstat(intent.BackupPath); !os.IsNotExist(err) {
		t.Fatal("acknowledged archive regenerated", err)
	}
	if _, err := j.RequestSecretMigrationRollback(r.ID, time.Now()); err != nil {
		t.Fatal(err)
	}
	if _, err := owner.Session(admin.ApplicationClient{Socket: "/tmp/test-admin.sock", ExpectedUID: 10006}, nil); err == nil {
		t.Fatal("original source owner reopened transfer after rollback choice")
	}
	if err := owner.Backup(ctx); err == nil {
		t.Fatal("original source owner resumed backup after rollback choice")
	}
}
