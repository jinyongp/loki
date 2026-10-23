//go:build linux

package compose

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/host/lifecycle"
)

func TestRawApplicationRestoreRefusesBeforeStoppingCurrentServices(t *testing.T) {
	b, runner, workspace := composeBackendFixture(t)
	generation := composeGeneration(t, "0.49.0", "a")
	state, err := b.stateFor(generation, lifecycle.InstallationState{Scope: "system", Workspace: workspace}, nil)
	if err != nil {
		t.Fatal(err)
	}
	state.ApplicationStateSource = "/var/lib/docker/volumes/fixture/_data/generations/" + strings.Repeat("a", 32)
	state.ApplicationRuntimeRoot = "/dev/shm/loki-source-owned/secrets"
	state.ApplicationPolicySHA256 = strings.Repeat("b", 64)
	if err := b.saveRuntime(state); err != nil {
		t.Fatal(err)
	}
	ref := filepath.Join(b.snapshotRoot, "raw-before-cutover")
	if err := os.Mkdir(ref, 0700); err != nil {
		t.Fatal(err)
	}
	if err := writePrivateJSON(filepath.Join(ref, "manifest.json"), snapshotManifest{Version: 1, Runtime: &state, Volumes: map[string]snapshotVolume{}}); err != nil {
		t.Fatal(err)
	}
	lock, err := lifecycle.AcquireOperationLock(b.root)
	if err != nil {
		t.Fatal(err)
	}
	defer lock.Close()
	j, err := lifecycle.OpenOperationJournal(b.root, lock, lifecycle.OperationJournalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	key, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	identity := appvalues.ApplicationIdentity{Version: 2, UID: 10006, GID: 10006, AgentUID: 10000, MetadataGID: 10001, DeploymentID: "deployment-" + strings.Repeat("a", 32), RunnerID: "123e4567-e89b-42d3-a456-426614174441", ActiveGeneration: strings.Repeat("a", 32), PolicySHA256: state.ApplicationPolicySHA256}
	backupIntent := lifecycle.ApplicationBackupIntent{Version: 1, Operation: "123e4567-e89b-42d3-a456-426614174442", Identity: identity, Policy: state.ApplicationPolicySHA256, Resources: []controlpolicy.ApplicationResource{}, Recipient: key.Recipient().String(), Directory: filepath.Join(ref, "applications")}
	intent := lifecycle.ApplicationRestoreIntent{Version: 1, Mode: "compose", BackupID: "sha256:" + strings.Repeat("c", 64), RuntimeRef: ref, Backup: lifecycle.ApplicationBackupCheckpoint{Intent: backupIntent, Exports: []appvalues.ApplicationExportProof{}}, Before: identity, BeforeResources: []controlpolicy.ApplicationResource{}, ExpectedPolicy: identity.PolicySHA256, Policy: identity.PolicySHA256, CandidateGeneration: strings.Repeat("d", 32), BeforeImage: state.CoreImage, TargetImage: state.CoreImage, BeforeProgram: generation.ID, TargetProgram: generation.ID}
	r, err := j.BeginApplicationRestore(intent, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	backupIntent.Operation = lifecycle.MigrationOperationUUID(r.ID)
	if _, err := j.BeginApplicationBackup(r.ID, backupIntent, time.Now()); err != nil {
		t.Fatal(err)
	}
	err = lifecycle.WithHostOperation(context.Background(), j, r.ID, func(ctx context.Context) error { return b.Restore(ctx, ref) })
	if err == nil || len(runner.snapshot()) != 0 {
		t.Fatal("raw historical application roots reached service/volume effects", err)
	}
}
