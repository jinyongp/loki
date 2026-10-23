package lifecycle

import (
	"context"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loki/internal/control/appvalues"
)

func applicationRestoreFixture(t *testing.T, root string) ApplicationRestoreIntent {
	t.Helper()
	m := migrationIntentFixture(t, root)
	identity := m.Before
	identity.Version, identity.PolicySHA256 = 2, m.ExpectedPolicy
	operation := "11111111-1111-4111-8111-111111111111"
	ref := filepath.Join(root, "snapshot")
	b := ApplicationBackupCheckpoint{Intent: ApplicationBackupIntent{Version: 1, Operation: operation, Identity: identity, Policy: m.ExpectedPolicy, Resources: m.Plan.Resources, Recipient: m.Recipient, Directory: filepath.Join(ref, "applications")}, Exports: []appvalues.ApplicationExportProof{{Version: 1, Operation: operation, Resource: m.Plan.Resources[0].ID, Policy: m.ExpectedPolicy, Revision: strings.Repeat("a", 64), Recipient: m.Recipient, CipherSHA256: strings.Repeat("b", 64), Bytes: 128}}}
	i := ApplicationRestoreIntent{Version: 1, Mode: "compose", BackupID: "sha256:" + strings.Repeat("c", 64), RuntimeRef: ref, Backup: b, Before: identity, BeforeResources: m.Plan.Resources, ExpectedPolicy: m.ExpectedPolicy, Policy: m.Policy, CandidateGeneration: m.CandidateGeneration, BeforeImage: "fixture/root@sha256:" + strings.Repeat("d", 64), TargetImage: "fixture/root@sha256:" + strings.Repeat("e", 64), BeforeProgram: "sha256:" + strings.Repeat("f", 64), TargetProgram: "sha256:" + strings.Repeat("1", 64)}
	if !i.Valid() {
		t.Fatal("invalid application restore fixture")
	}
	return i
}

func TestApplicationRestorePreservesOriginalSafetyImportAndReleaseAuthority(t *testing.T) {
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
	i := applicationRestoreFixture(t, root)
	now := time.Now().UTC()
	r, err := j.BeginApplicationRestore(i, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.AdvanceApplicationRestore(r.ID, PhaseSnapshot, now); err == nil {
		t.Fatal("restore imported without a retained safety backup")
	}
	changed := i
	changed.Interrupt = true
	if _, err := j.BeginApplicationRestore(changed, now); err == nil {
		t.Fatal("pending restore changed its original intent")
	}
	if _, err := j.CheckpointApplicationRestoreSafety(r.ID, operationBackupID(), now); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []OperationPhase{PhaseSnapshot, PhaseMigrate} {
		if _, err := j.AdvanceApplicationRestore(r.ID, phase, now); err != nil {
			t.Fatal(err)
		}
	}
	resource := i.Backup.Exports[0].Resource
	digest := strings.Repeat("2", 64)
	if _, err := j.CheckpointApplicationRestoreImport(r.ID, resource, digest, true, now); err == nil {
		t.Fatal("restore completion preceded original import intent")
	}
	if _, err := j.SealApplicationRestore(r.ID, now); err == nil {
		t.Fatal("incomplete restore became selectable")
	}
	if _, err := j.CheckpointApplicationRestoreImport(r.ID, resource, digest, false, now); err != nil {
		t.Fatal(err)
	}
	if _, err := j.CheckpointApplicationRestoreImport(r.ID, resource, strings.Repeat("3", 64), false, now); err == nil {
		t.Fatal("same import slot changed its exact preview digest")
	}
	for attempt := 0; attempt < 2; attempt++ {
		if _, err := j.CheckpointApplicationRestoreImport(r.ID, resource, digest, true, now); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := j.SealApplicationRestore(r.ID, now); err != nil {
		t.Fatal(err)
	}
	for _, phase := range []OperationPhase{PhaseSwitch, PhaseRestart, PhaseHealth} {
		if _, err := j.AdvanceApplicationRestore(r.ID, phase, now); err != nil {
			t.Fatal(err)
		}
	}
	r, err = j.CompleteApplicationRestore(r.ID, false, now)
	if err != nil || r.Maintenance.Released {
		t.Fatal("restore result prematurely released maintenance", err)
	}
	if _, found, err := j.Active(); err != nil || !found {
		t.Fatal("lost terminal original owner before release ACK", err)
	}
	invoked := false
	if _, _, err := j.RecoverInterrupted(context.Background(), func(context.Context, OperationRecord) error { invoked = true; return nil }, now); err == nil || invoked {
		t.Fatal("generic recovery bypassed logical restore owner")
	}
	var stale context.Context
	if err := WithHostOperation(context.Background(), j, r.ID, func(ctx context.Context) error { stale = ctx; _, _, err := CurrentHostOperation(ctx); return err }); err != nil {
		t.Fatal(err)
	}
	if _, _, err := CurrentHostOperation(stale); err == nil {
		t.Fatal("adapter capability survived its callback")
	}
	if _, err := j.AcknowledgeApplicationRestoreRelease(r.ID, now); err != nil {
		t.Fatal(err)
	}
	if replay, err := j.BeginApplicationRestore(i, now); err != nil || replay.ID != r.ID || !replay.Maintenance.Released {
		t.Fatal("completed plan did not replay original result", err)
	}
}

func TestApplicationRestoreRollbackDirectionCannotResumeImports(t *testing.T) {
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
	i := applicationRestoreFixture(t, root)
	now := time.Now().UTC()
	r, err := j.BeginApplicationRestore(i, now)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := j.RequestApplicationRestoreRollback(r.ID, now); err != nil {
		t.Fatal(err)
	}
	if _, err := j.AdvanceApplicationRestore(r.ID, PhaseSnapshot, now); err == nil {
		t.Fatal("forward restore resumed after persisted rollback")
	}
	if _, err := j.CompleteApplicationRestore(r.ID, true, now); err != nil {
		t.Fatal(err)
	}
}
