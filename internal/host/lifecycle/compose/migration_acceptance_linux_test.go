//go:build linux

package compose

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"filippo.io/age"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loki/internal/host/lifecycle"
)

func TestRealComposeMigrationPublicLifecycle(t *testing.T) {
	runRealComposeMigrationLifecycle(t, "migration")
}

func TestRealComposeApplicationRestore(t *testing.T) {
	runRealComposeMigrationLifecycle(t, "restore")
}

func TestRealComposeNewDeploymentRecovery(t *testing.T) {
	runRealComposeMigrationLifecycle(t, "new-deployment")
}

func runRealComposeMigrationLifecycle(t *testing.T, journey string) {
	image := os.Getenv("LOKI_TEST_NATIVE_SECRET_IMAGE")
	if image == "" {
		if os.Getenv("LOKI_REQUIRE_NATIVE_SECRET_COMPOSE") == "1" {
			t.Fatal("actual pinned image is required")
		}
		t.Skip("native package suite supplies actual Docker services")
	}
	binary := os.Getenv("LOKI_TEST_HOST_MIGRATION_CLI")
	if !filepath.IsAbs(binary) {
		t.Fatal("actual public host CLI is required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 6*time.Minute)
	defer cancel()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0755); err != nil {
		t.Fatal(err)
	}
	// Acceptance runs with umask 077. Bind the actual public workspace leaf
	// with explicit traversal for the configured unprivileged runtime UID.
	if err := os.Chmod(workspace, 0755); err != nil {
		t.Fatal(err)
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	port := listener.Addr().(*net.TCPAddr).Port
	listener.Close()
	sum := sha256.Sum256([]byte(root))
	b, err := New(Config{StateRoot: filepath.Join(root, "operator"), Project: "loki-migrate-" + hex.EncodeToString(sum[:6]), Runner: ExecRunner{}})
	if err != nil {
		t.Fatal(err)
	}
	state := runtimeState{Version: 1, GenerationID: "sha256:" + strings.Repeat("a", 64), CoreImage: image, Workspace: workspace, MCPPort: port}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 45*time.Second)
		defer stop()
		if current, exists, _ := b.loadRuntime(); exists {
			state = current
		}
		if t.Failed() {
			// This owned fixture runs no project command and contains only
			// synthetic data. Capture bounded startup diagnostics before cleanup.
			if body, err := b.compose(cleanup, state, "logs", "--no-color", "--tail", "20", "runtime", "launcher", "executor", "application-values"); err == nil {
				t.Log("finite owned service startup diagnostics:\n" + string(body))
			}
		}
		if _, err := b.compose(cleanup, state, "down", "--volumes", "--remove-orphans"); err != nil {
			t.Error("owned services cleanup", err)
		}
		ids, err := b.runner.Run(cleanup, nil, "container", "ls", "--all", "--filter", "label=io.loki.migration.owner="+applicationOwnerID(b.root), "--format", "{{.ID}}")
		if err != nil {
			t.Error(err)
		}
		for _, id := range strings.Fields(string(ids)) {
			if _, err := b.runner.Run(cleanup, nil, "container", "rm", "--force", id); err != nil {
				t.Error(err)
			}
		}
		for _, name := range []string{"application-state", "runtime-state"} {
			if found, _ := b.volumeExists(cleanup, b.volumeName(name)); found {
				if _, err := b.runner.Run(cleanup, nil, "volume", "rm", b.volumeName(name)); err != nil {
					t.Error(err)
				}
			}
		}
		if state.ApplicationRuntimeRoot != "" {
			_, err := b.runner.(PrivateRunner).RunPrivate(cleanup, nil, nil, "run", "--rm", "--network", "none", "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--cap-add", "FOWNER", "--user", "0:0", "--mount", applicationBind(state.ApplicationRuntimeRoot, "/fixture", false), "--entrypoint", "/bin/sh", image, "-ec", "find /fixture -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +")
			if err != nil {
				t.Error(err)
			}
			if err := os.Remove(state.ApplicationRuntimeRoot); err != nil {
				t.Error(err)
			}
			if err := os.Remove(filepath.Dir(state.ApplicationRuntimeRoot)); err != nil {
				t.Error(err)
			}
		}
	})
	if err := b.prepareApplicationValues(ctx, &state); err != nil {
		t.Fatal("initial normal roles", err)
	}
	if err := b.saveRuntime(state); err != nil {
		t.Fatal(err)
	}
	if _, err := b.compose(ctx, state, "up", "--detach", "--wait", "--wait-timeout", "45", "application-values", "launcher"); err != nil {
		t.Fatal("actual original services", err)
	}
	intent, backupIdentity := verifyRealComposeMigrationPreparation(t, ctx, b, state, image)
	invoke := func(action string, input []byte, extra ...string) SecretMigrationStatus {
		t.Helper()
		args := []string{"host", "secret-migration", "compose", action, "--state-root", b.root, "--project", b.project}
		args = append(args, extra...)
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Stdin = bytes.NewReader(input)
		var output, diagnostic bytes.Buffer
		cmd.Stdout, cmd.Stderr = &output, &diagnostic
		if err := cmd.Run(); err != nil {
			t.Fatal("public Compose migration", action, err, diagnostic.String())
		}
		var result SecretMigrationStatus
		if json.Unmarshal(output.Bytes(), &result) != nil || result.Operation == "" {
			t.Fatal("invalid public progress")
		}
		return result
	}
	plan := filepath.Join(b.root, "approved-plan.json")
	if err := writePrivateJSON(plan, intent); err != nil {
		t.Fatal(err)
	}
	pending := invoke("apply", nil, "--plan", plan, "--approve")
	if pending.Phase != lifecycle.PhaseMigrate || pending.Protection != "uninitialized" || pending.Released {
		t.Fatal("candidate was not pending manual initialization", pending)
	}
	rolledBack := invoke("rollback", nil, "--approve")
	if rolledBack.State != lifecycle.OperationRolledBack || !rolledBack.Released {
		t.Fatal("original rollback did not finish", rolledBack)
	}
	if replay := invoke("apply", nil, "--plan", plan, "--approve"); replay != rolledBack {
		t.Fatal("rollback replay created another operation")
	}
	intent, err = b.PrepareSecretMigration(ctx, intent.Plan.Mappings, intent.Plan.Resources, intent.Recipient, filepath.Join(b.root, "forward-source.age"), false)
	if err != nil {
		t.Fatal("prepare after original rollback", err)
	}
	if err := writePrivateJSON(plan, intent); err != nil {
		t.Fatal(err)
	}
	pending = invoke("apply", nil, "--plan", plan, "--approve")
	if pending.Protection != "uninitialized" {
		t.Fatal("fresh candidate protection was inherited")
	}
	// Simulate an operator-initialized candidate whose process is then lost.
	// The next public recovery must remain locked until explicit private unlock.
	passphrase := []byte("synthetic-compose-migration-private-passphrase")
	defer clear(passphrase)
	if err := b.withMigration(ctx, nil, func(ctx context.Context, _ *lifecycle.OperationJournal, _ lifecycle.OperationRecord) error {
		if _, err := b.migrationCandidateProtection(ctx, "init", passphrase); err != nil {
			return err
		}
		return b.stopMigrationCandidate(ctx)
	}); err != nil {
		t.Fatal("explicit candidate initialization/process loss", err)
	}
	var candidateRuntime string
	if err := b.withMigration(ctx, nil, func(_ context.Context, _ *lifecycle.OperationJournal, r lifecycle.OperationRecord) error {
		candidateRuntime = filepath.Join("/dev/shm", "loki-tools-"+applicationOwnerID(b.root)+"-migration-"+r.ID, "secrets")
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	// Simulate erased host tmpfs without calling a process restart a reboot.
	// Persistent source/target data and the original host/Job journals survive.
	for _, projection := range []string{state.ApplicationRuntimeRoot, candidateRuntime} {
		if _, err := b.runner.(PrivateRunner).RunPrivate(ctx, nil, nil, "run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--cap-add", "FOWNER", "--user", "0:0", "--log-driver", "none", "--mount", applicationBind(projection, "/owned-runtime", false), "--entrypoint", "/bin/sh", image, "-ec", "find /owned-runtime -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +"); err != nil {
			t.Fatal("erase only owned stopped tmpfs projection", err)
		}
		if err := os.Remove(projection); err != nil {
			t.Fatal(err)
		}
		if err := os.Remove(filepath.Dir(projection)); err != nil {
			t.Fatal(err)
		}
	}
	locked := invoke("recover", nil)
	if locked.Released || locked.Protection != "locked" && locked.Protection != "locked_manual" || locked.Operation != pending.Operation {
		t.Fatal("lost process retained protection session", locked)
	}
	completed := invoke("recover", passphrase, "--protection", "unlock", "--stdin")
	if completed.State != lifecycle.OperationSucceeded || !completed.Released || completed.Operation != pending.Operation {
		t.Fatal("original cutover did not finish", completed)
	}
	if replay := invoke("apply", nil, "--plan", plan, "--approve"); replay != completed {
		t.Fatal("completed plan did not retain original result")
	}
	if status := invoke("status", nil); status != completed {
		t.Fatal("terminal status lost original result")
	}
	current, found, err := b.loadRuntime()
	if err != nil || !found || filepath.Base(current.ApplicationStateSource) != intent.CandidateGeneration || current.ApplicationPolicySHA256 != intent.Policy {
		t.Fatal("normal serving runtime did not select candidate", err)
	}
	if _, err := os.Lstat(intent.BackupPath); err != nil {
		t.Fatal("original encrypted backup missing", err)
	}
	ids, err := b.runner.Run(ctx, nil, "container", "ls", "--all", "--filter", "label=io.loki.migration.owner="+applicationOwnerID(b.root), "--format", "{{.ID}}")
	if err != nil || strings.TrimSpace(string(ids)) != "" {
		t.Fatal("completed migration left private helpers", err)
	}
	// Exercise the actual installed host engine after cutover. Its warm logical
	// export must precede stopping services and remain bound to the same kernel
	// lock/journal that owns the ordinary offline volume snapshot and release.
	layout := filepath.Join(filepath.Dir(filepath.Dir(current.ApplicationStateSource)), "secrets-admin.json")
	unlockArgs := []string{"run", "--rm", "--interactive", "--network", "none", "--read-only", "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--user", "0:0", "--log-driver", "none", "--entrypoint", "/opt/loki/bin/loki", "--mount", applicationBind(filepath.Join(current.ApplicationRuntimeRoot, "admin"), "/run/loki/secrets/admin", true), "--mount", applicationBind(layout, "/etc/loki/secret-admin.json", true), image, "secret", "protection", "unlock", "--layout", "/etc/loki/secret-admin.json", "--stdin"}
	if _, err := b.runner.(PrivateRunner).RunPrivate(ctx, nil, bytes.NewReader(passphrase), unlockArgs...); err != nil {
		t.Fatal("manual warm-vault backup prerequisite", err)
	}
	store, err := lifecycle.EnsureFileStore(b.root)
	if err != nil {
		t.Fatal(err)
	}
	spec := composeGeneration(t, "0.49.0", "a").Spec
	_, spec.CoreImageDigest, _ = strings.Cut(image, "@")
	generation, err := lifecycle.NewGeneration(spec)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.InitializeInstall(ctx, generation, lifecycle.InstallationState{Scope: "system", Workspace: workspace, MCPPort: port}, time.Now()); err != nil {
		t.Fatal(err)
	}
	if err := store.CommitGeneration(ctx, generation, time.Now()); err != nil {
		t.Fatal(err)
	}
	current.GenerationID = generation.ID
	if err := b.saveRuntime(current); err != nil {
		t.Fatal(err)
	}
	engine := lifecycle.TransactionEngine{Store: store, Backend: b}
	backup, err := engine.Backup(ctx)
	if err != nil || !backup.Valid() || backup.ApplicationBackup == nil || !backup.ApplicationBackup.Complete() || len(backup.ApplicationBackup.Exports) != 1 {
		t.Fatal("actual original-owner logical+offline host backup", err)
	}
	proof := backup.ApplicationBackup.Exports[0]
	artifact := filepath.Join(backup.ApplicationBackup.Intent.Directory, strings.ReplaceAll(proof.Resource, "-", "")+".age")
	if err := verifyApplicationCipher(artifact, proof); err != nil {
		t.Fatal("published logical archive integrity", err)
	}
	ciphertext, err := os.Open(artifact)
	if err != nil {
		t.Fatal(err)
	}
	defer ciphertext.Close()
	plaintext, err := age.Decrypt(ciphertext, backupIdentity)
	if err != nil {
		t.Fatal("operator identity could not authenticate host archive", err)
	}
	if _, err := io.Copy(io.Discard, plaintext); err != nil {
		t.Fatal("host archive authentication failed", err)
	}
	if gate, err := b.maintenanceCall(ctx, current, "status", "", false, false); err != nil || gate == nil || gate.Active {
		t.Fatal("successful backup retained Job gate", err)
	}
	if journey == "restore" {
		verifyRealComposeApplicationRestore(t, ctx, b, binary, backup, backupIdentity, passphrase, unlockArgs)
	}
	if journey == "new-deployment" {
		verifyRealComposeNewDeployment(t, ctx, b, binary, backup, backupIdentity)
	}
}

func verifyRealComposeApplicationRestore(t *testing.T, ctx context.Context, b *Backend, binary string, backup lifecycle.BackupRecord, identity *age.X25519Identity, passphrase []byte, unlockArgs []string) {
	t.Helper()
	unlock := func() {
		t.Helper()
		if _, err := b.runner.(PrivateRunner).RunPrivate(ctx, nil, bytes.NewReader(passphrase), unlockArgs...); err != nil {
			t.Fatal("explicit warm restore safety-backup prerequisite", err)
		}
	}
	invoke := func(action string, input []byte, extra ...string) ApplicationRestoreStatus {
		t.Helper()
		args := append([]string{"host", "application-restore", "compose", action, "--state-root", b.root, "--project", b.project}, extra...)
		cmd := exec.CommandContext(ctx, binary, args...)
		cmd.Stdin = bytes.NewReader(input)
		var output, diagnostic bytes.Buffer
		cmd.Stdout, cmd.Stderr = &output, &diagnostic
		if err := cmd.Run(); err != nil {
			t.Fatal("public paired application restore", action, err, diagnostic.String())
		}
		var result ApplicationRestoreStatus
		if json.Unmarshal(output.Bytes(), &result) != nil || result.Operation == "" {
			t.Fatal("invalid public restore progress")
		}
		return result
	}
	plan := filepath.Join(b.root, "restore-plan.json")
	prepare := func() lifecycle.ApplicationRestoreIntent {
		t.Helper()
		intent, err := b.PrepareApplicationRestore(ctx, backup.ID, false)
		if err != nil || !intent.Valid() {
			t.Fatal("read-only paired restore preparation", err)
		}
		if err := writePrivateJSON(plan, intent); err != nil {
			t.Fatal(err)
		}
		return intent
	}
	unlock()
	prepare()
	pending := invoke("apply", nil, "--plan", plan, "--approve")
	if pending.Phase != lifecycle.PhaseMigrate || pending.Protection != "uninitialized" || pending.Released {
		t.Fatal("restore implicitly initialized candidate", pending)
	}
	rolledBack := invoke("rollback", nil, "--approve")
	if rolledBack.State != lifecycle.OperationRolledBack || !rolledBack.Released {
		t.Fatal("restore pre-cutover rollback", rolledBack)
	}
	if replay := invoke("apply", nil, "--plan", plan, "--approve"); replay != rolledBack {
		t.Fatal("restore rollback replay created another operation")
	}
	unlock()
	intent := prepare()
	pending = invoke("apply", nil, "--plan", plan, "--approve")
	initialized := invoke("recover", passphrase, "--protection", "init", "--stdin")
	if initialized.Operation != pending.Operation || initialized.Protection != "archive_identity_required" || initialized.Released {
		t.Fatal("candidate import did not require the explicit operator archive identity", initialized)
	}
	var projection string
	if err := b.withApplicationRestore(ctx, nil, func(ctx context.Context, _ *lifecycle.OperationJournal, r lifecycle.OperationRecord) error {
		projection = filepath.Join("/dev/shm", "loki-tools-"+applicationOwnerID(b.root)+"-restore-"+r.ID, "secrets")
		return b.stopApplicationRestoreCandidate(ctx, r)
	}); err != nil {
		t.Fatal("owned restore candidate process loss", err)
	}
	if _, err := b.runner.(PrivateRunner).RunPrivate(ctx, nil, nil, "run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--cap-add", "FOWNER", "--user", "0:0", "--log-driver", "none", "--mount", applicationBind(projection, "/owned-runtime", false), "--entrypoint", "/bin/sh", intent.TargetImage, "-ec", "find /owned-runtime -mindepth 1 -maxdepth 1 -exec rm -rf -- {} +"); err != nil {
		t.Fatal("erase owned stopped restore tmpfs", err)
	}
	if err := os.Remove(projection); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(filepath.Dir(projection)); err != nil {
		t.Fatal(err)
	}
	locked := invoke("recover", nil)
	if locked.Operation != pending.Operation || locked.Released || locked.Protection != "locked" && locked.Protection != "locked_manual" {
		t.Fatal("restore process/tmpfs loss inherited the key session", locked)
	}
	key := filepath.Join(b.root, "operator-restore-identity")
	if err := os.WriteFile(key, []byte(identity.String()), 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(key)
	completed := invoke("recover", passphrase, "--identity-file", key, "--protection", "unlock", "--stdin")
	if completed.State != lifecycle.OperationSucceeded || !completed.Released || !completed.Sealed || completed.Imports != 1 || completed.Operation != pending.Operation {
		t.Fatal("original paired logical restore did not complete", completed)
	}
	if replay := invoke("apply", nil, "--plan", plan, "--approve"); replay != completed {
		t.Fatal("restore completion replay changed original result")
	}
	current, found, err := b.loadRuntime()
	if err != nil || !found || filepath.Base(current.ApplicationStateSource) != intent.CandidateGeneration {
		t.Fatal("restore did not select fresh generation", err)
	}
	statusArgs := append([]string{}, unlockArgs[:len(unlockArgs)-1]...)
	statusArgs[len(unlockArgs)-4] = "status"
	status, err := b.runner.(PrivateRunner).RunPrivate(ctx, nil, nil, statusArgs...)
	var protection struct {
		State string `json:"state"`
	}
	if err != nil || json.Unmarshal(status, &protection) != nil || protection.State != "locked" && protection.State != "locked_manual" {
		t.Fatal("restore reused candidate key session", err)
	}
	ids, err := b.runner.Run(ctx, nil, "container", "ls", "--all", "--filter", "label=io.loki.restore.owner="+applicationOwnerID(b.root), "--format", "{{.ID}}")
	if err != nil || strings.TrimSpace(string(ids)) != "" {
		t.Fatal("restore left private helpers", err)
	}
}
