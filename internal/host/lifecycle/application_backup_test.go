package lifecycle

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loki/internal/control/appvalues"
)

func TestLogicalApplicationBackupRequiresOriginalOwnerAndImmutableProofs(t *testing.T) {
	root := privateLifecycleRoot(t)
	lock, err := AcquireOperationLock(root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	j, err := OpenOperationJournal(root, lock, OperationJournalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	now := time.Now().UTC()
	r, err := j.Begin(OperationBackup, operationPlanFixture(t, now), now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.requireMaintenance(r.ID, false); err != nil {
		t.Fatal(err)
	}
	fixture := migrationIntentFixture(t, root)
	before := fixture.Before
	before.Version, before.PolicySHA256 = 2, fixture.ExpectedPolicy
	intent := ApplicationBackupIntent{Version: 1, Operation: MigrationOperationUUID(r.ID), Identity: before, Policy: fixture.ExpectedPolicy, Resources: fixture.Plan.Resources, Recipient: fixture.Recipient, Directory: filepath.Join(root, "logical-backup")}
	if _, err := j.BeginApplicationBackup(r.ID, intent, now); err != nil {
		t.Fatal(err)
	}
	changed := intent
	changed.Directory += "-other"
	if _, err := j.BeginApplicationBackup(r.ID, changed, now); err == nil {
		t.Fatal("original backup changed destination")
	}
	proof := appvalues.ApplicationExportProof{Version: 1, Operation: intent.Operation, Resource: intent.Resources[0].ID, Policy: intent.Policy, Revision: strings.Repeat("a", 64), Recipient: intent.Recipient, CipherSHA256: strings.Repeat("b", 64), Bytes: 128}
	if _, _, err := CurrentHostOperation(context.Background()); err == nil {
		t.Fatal("public operation ID granted journal access")
	}
	ctx := hostOperationContext(context.Background(), j, r.ID)
	if !j.HoldsOperationRoot(root) || j.HoldsOperationRoot(root+"-other") {
		t.Fatal("host capability crossed its original root")
	}
	if current, owner, err := CurrentHostOperation(ctx); err != nil || owner != j || current.ID != r.ID {
		t.Fatal("lost original owner", err)
	}
	for attempt := 0; attempt < 2; attempt++ {
		result, err := j.CheckpointApplicationExport(r.ID, proof, now)
		if err != nil || !result.ApplicationBackup.Complete() {
			t.Fatal("backup proof/replay", err)
		}
	}
	proof.Bytes++
	if _, err := j.CheckpointApplicationExport(r.ID, proof, now); err == nil {
		t.Fatal("original resource ciphertext proof changed")
	}
	if err := lock.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := CurrentHostOperation(ctx); err == nil {
		t.Fatal("stale host context survived original lock release")
	}
}
