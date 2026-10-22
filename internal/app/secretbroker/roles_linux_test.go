//go:build linux

package secretbroker_test

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"syscall"
	"testing"
	"time"

	"filippo.io/age"
	"loki/internal/admin"
	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/fault"
	"loki/internal/host/lifecycle"
	hostmigration "loki/internal/host/migration"
	"loki/internal/platform/privateio"
	"loki/internal/rpc"
	secret "loki/modules/secrets"
)

type sourceMetadataCapture struct {
	input    io.ReadCloser
	output   io.WriteCloser
	metadata bytes.Buffer
}

func (c *sourceMetadataCapture) Read(body []byte) (int, error) {
	n, err := c.input.Read(body)
	_, _ = c.metadata.Write(body[:n])
	return n, err
}
func (c *sourceMetadataCapture) Write(body []byte) (int, error) { return c.output.Write(body) }
func (c *sourceMetadataCapture) Close() error {
	return errors.Join(c.input.Close(), c.output.Close())
}

func TestRealApplicationRoleSockets(t *testing.T) {
	broker, provider, worker := os.Getenv("LOKI_SECRET_TEST_BROKER"), os.Getenv("LOKI_SECRET_TEST_DEVTOOLS"), os.Getenv("LOKI_SECRET_TEST_WORKER")
	if broker == "" || provider == "" || worker == "" || os.Geteuid() != 0 {
		if os.Getenv("LOKI_REQUIRE_SECRET_ROLES") == "1" {
			t.Fatal("root and exact native worker/broker/provider binaries are required")
		}
		t.Skip("native values Actions suite supplies root and exact binaries")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	t.Cleanup(cancel)
	root, err := os.MkdirTemp("/tmp", "loki-value-roles-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	if os.Chmod(root, 0755) != nil {
		t.Fatal("fixture root permissions")
	}
	copyExecutable := func(source, name string) string {
		t.Helper()
		data, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, name)
		if os.WriteFile(path, data, 0555) != nil || os.Chmod(path, 0555) != nil {
			t.Fatal("fixture executable")
		}
		return path
	}
	broker = copyExecutable(broker, "broker-bin")
	provider = copyExecutable(provider, "provider-bin")
	makeDir := func(name string, mode os.FileMode, gid int) string {
		t.Helper()
		path := filepath.Join(root, name)
		if os.Mkdir(path, mode) != nil || os.Chown(path, 1000, gid) != nil || os.Chmod(path, mode) != nil {
			t.Fatal("fixture role directory")
		}
		return path
	}
	data := makeDir("provider", 0700, 1000)
	metadata := makeDir("metadata", 0700, 1000)
	jobs := filepath.Join(makeDir("jobs", 0700, 1000), "jobs.sock")
	adminSocket := filepath.Join(makeDir("admin", 0700, 1000), "admin.sock")
	metaSocket := filepath.Join(makeDir("agent", 0750, 10000), "metadata.sock")
	inbox, err := os.MkdirTemp("/dev/shm", "loki-role-inbox-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(inbox) })
	if os.Chown(inbox, 1000, 1000) != nil || os.Chmod(inbox, 0700) != nil {
		t.Fatal("fixture inbox")
	}
	runtimeRoot, err := os.MkdirTemp("/dev/shm", "loki-role-runtime-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeRoot) })
	if os.Chown(runtimeRoot, 1000, 1000) != nil || os.Chmod(runtimeRoot, 0700) != nil {
		t.Fatal("fixture runtime lease root")
	}
	resource := controlpolicy.ApplicationResource{ID: "c199846b-43ad-468c-9229-11976d4e5b3a", Alias: "demo", Repository: "github.com/fixture/app", Principals: []controlpolicy.ApplicationGrant{{Principal: "agent", Metadata: true, Write: true}}, Environments: []string{"common"}, Keys: map[string]string{"TOKEN": "secret", "PENDING": "secret"}, Network: "none"}
	migratedResource := controlpolicy.ApplicationResource{ID: "123e4567-e89b-42d3-a456-426614174511", Alias: "migrated", Repository: "owner/migration", Principals: []controlpolicy.ApplicationGrant{{Principal: "operator", Metadata: true, Write: true, Execute: true}}, Environments: []string{"production"}, Keys: map[string]string{"TOKEN": "secret", "EMPTY": "secret", "PUB": "variable"}, Network: "none"}
	layout := map[string]any{"uid": 1000, "launcher_uid": 0, "binary": provider, "root": data, "socket": jobs, "deployment_id": "1bbf75ed-1b51-46bd-8255-31536e945564", "policy_sha256": strings.Repeat("a", 64), "runner_id": "9af6c2e6-c1d4-41a3-a6e9-9fb3c07c67d2", "instance_id": "a203906e-1c9c-4cc5-acb7-a68d118c54da", "resources": []controlpolicy.ApplicationResource{resource}, "metadata_socket": metaSocket, "metadata_gid": 10000, "agent_uid": 10000, "principal": "agent", "admin_socket": adminSocket, "application_root": metadata, "inbox": inbox}
	layout["runtime_root"] = runtimeRoot
	layout["resources"] = []controlpolicy.ApplicationResource{resource, migratedResource}
	encoded, _ := json.Marshal(layout)
	layoutPath := filepath.Join(root, "layout.json")
	if os.WriteFile(layoutPath, encoded, 0644) != nil || os.Chmod(layoutPath, 0644) != nil {
		t.Fatal("fixture layout")
	}
	startBroker := func() func(bool) {
		t.Helper()
		cmd := exec.CommandContext(ctx, broker, "--layout", layoutPath)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent"}
		cmd.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 1000, Gid: 1000, Groups: []uint32{10000}}}
		var diagnostics bytes.Buffer
		cmd.Stdout = &diagnostics
		cmd.Stderr = &diagnostics
		if err := cmd.Start(); err != nil {
			t.Fatal(err)
		}
		done := make(chan error, 1)
		go func() { done <- cmd.Wait() }()
		var once sync.Once
		stop := func(abrupt bool) {
			once.Do(func() {
				if abrupt {
					_ = cmd.Process.Kill()
				} else {
					_ = cmd.Process.Signal(syscall.SIGTERM)
				}
				select {
				case err := <-done:
					if err != nil && !abrupt {
						t.Errorf("broker shutdown: %v", err)
					}
				case <-time.After(3 * time.Second):
					_ = cmd.Process.Kill()
					<-done
					t.Error("broker did not shut down")
				}
			})
		}
		t.Cleanup(func() { stop(false) })
		for {
			_, metaErr := os.Stat(metaSocket)
			_, adminErr := os.Stat(adminSocket)
			if metaErr == nil && adminErr == nil {
				conn, err := net.DialTimeout("unix", adminSocket, 50*time.Millisecond)
				if err == nil {
					peer, err := rpc.PeerCredentials(conn.(*net.UnixConn))
					conn.Close()
					if err == nil && int(peer.PID) == cmd.Process.Pid {
						break
					}
				}
			}
			select {
			case err := <-done:
				done <- err
				t.Fatalf("role broker did not start: %v %s", err, &diagnostics)
			case <-ctx.Done():
				t.Fatal("role broker readiness timed out")
			case <-time.After(10 * time.Millisecond):
			}
		}
		return stop
	}
	stopBroker := startBroker()
	client := admin.ApplicationClient{Socket: adminSocket, ExpectedUID: 1000}
	if _, err := client.Call(ctx, appvalues.AdminRequest{Action: "revision", Resource: "demo"}, nil); err != nil {
		t.Fatal("root administration was rejected", err)
	}
	uid := uint32(1000)
	if _, err := (rpc.Client{Socket: metaSocket, ExpectedUID: &uid}).Call(ctx, map[string]any{"operation": "inspect", "action": "resources"}); err == nil {
		t.Fatal("root used agent metadata socket")
	}
	self, err := os.Executable()
	if err != nil {
		t.Fatal(err)
	}
	binaryBytes, err := os.ReadFile(self)
	if err != nil {
		t.Fatal(err)
	}
	helper := filepath.Join(root, "role-client")
	if os.WriteFile(helper, binaryBytes, 0555) != nil || os.Chmod(helper, 0555) != nil {
		t.Fatal("role helper binary")
	}
	for _, role := range []struct {
		uid  uint32
		mode string
	}{{10000, "agent"}, {10001, "foreign"}} {
		child := exec.CommandContext(ctx, helper, "-test.run=^TestApplicationRoleClient$")
		child.Env = []string{"PATH=/usr/bin:/bin", "LOKI_ROLE_MODE=" + role.mode, "LOKI_ROLE_METADATA=" + metaSocket, "LOKI_ROLE_ADMIN=" + adminSocket, "LOKI_ROLE_JOBS=" + jobs, "LOKI_ROLE_PROVIDER=" + data, "LOKI_ROLE_INBOX=" + inbox}
		child.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: role.uid, Gid: role.uid, Groups: []uint32{10000}}}
		if out, err := child.CombinedOutput(); err != nil {
			t.Fatalf("%s role boundary failed: %v %s", role.mode, err, out)
		}
	}
	// Exercise a real process restart after a successful import consumed its
	// tmpfs input. Only durable metadata and the original provider receipt remain.
	providerCall := func(input string, args ...string) {
		t.Helper()
		command := exec.CommandContext(ctx, provider, append(args, "--isolated-root", data)...)
		command.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent"}
		command.Stdin = strings.NewReader(input)
		command.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 1000, Gid: 1000}}
		if err := command.Run(); err != nil {
			t.Fatalf("synthetic provider %s failed: %v", args[0], err)
		}
	}
	if _, err := client.Call(ctx, appvalues.AdminRequest{Action: "protection_init"}, []byte("synthetic-role-fixture-passphrase")); err != nil {
		t.Fatal("private manual initialization failed", err)
	}
	t.Cleanup(func() { providerCall("", "vault", "lock") })
	stageRaw, err := client.Call(ctx, appvalues.AdminRequest{Action: "stage", Resource: "demo", Principal: "agent"}, []byte("TOKEN=synthetic-private-restart-canary\n"))
	if err != nil {
		t.Fatal(err)
	}
	revisionRaw, err := client.Call(ctx, appvalues.AdminRequest{Action: "revision", Resource: "demo"}, nil)
	if err != nil {
		t.Fatal(err)
	}
	var stage struct {
		ID string `json:"stage_id"`
	}
	var revision struct {
		Revision string `json:"revision"`
	}
	if json.Unmarshal(stageRaw, &stage) != nil || json.Unmarshal(revisionRaw, &revision) != nil || stage.ID == "" || len(revision.Revision) != 64 {
		t.Fatal("private administration metadata was invalid")
	}
	request, _ := json.Marshal(map[string]any{"operation": "write", "action": "import_staged", "resource": "demo", "stage_id": stage.ID, "expected_revision": revision.Revision, "request_id": "efb1e1c4-8acb-43c8-9ba6-756b20cc50c3"})
	write := func(replayed string) {
		t.Helper()
		child := exec.CommandContext(ctx, helper, "-test.run=^TestApplicationRoleClient$")
		child.Env = []string{"PATH=/usr/bin:/bin", "LOKI_ROLE_MODE=agent", "LOKI_ROLE_METADATA=" + metaSocket, "LOKI_ROLE_ADMIN=" + adminSocket, "LOKI_ROLE_JOBS=" + jobs, "LOKI_ROLE_PROVIDER=" + data, "LOKI_ROLE_INBOX=" + inbox, "LOKI_ROLE_WRITE=" + string(request), "LOKI_ROLE_REPLAYED=" + replayed}
		child.SysProcAttr = &syscall.SysProcAttr{Credential: &syscall.Credential{Uid: 10000, Gid: 10000, Groups: []uint32{10000}}}
		if out, err := child.CombinedOutput(); err != nil {
			t.Fatalf("agent receipt replay failed: %v %s", err, out)
		}
	}
	write("false")
	migrationOperation := "123e4567-e89b-42d3-a456-426614174491"
	comparisonRequest := appvalues.AdminRequest{Action: "migration_compare", Resource: "demo", Revision: strings.Repeat("a", 64), RequestID: "123e4567-e89b-42d3-a456-426614174492"}
	expectedValues := []privateio.MigrationExpectedValue{{Name: "TOKEN", Value: []byte("synthetic-private-restart-canary")}}
	defer privateio.ClearMigrationExpected(expectedValues)
	if _, err := client.CompareMigration(ctx, migrationOperation, comparisonRequest, expectedValues, func(appvalues.MigrationComparisonBinding) error {
		t.Fatal("ordinary active broker exposed migration capability")
		return nil
	}); err == nil {
		t.Fatal("ordinary active broker admitted migration comparison")
	}
	if _, err := os.Stat(filepath.Join(inbox, stage.ID+".data")); !os.IsNotExist(err) {
		t.Fatal("staged plaintext survived import")
	}
	pending := func() string {
		t.Helper()
		raw, err := client.Call(ctx, appvalues.AdminRequest{Action: "stage", Resource: "demo", Principal: "agent"}, []byte("PENDING=synthetic-unconsumed-input\n"))
		var staged struct {
			ID string `json:"stage_id"`
		}
		if err != nil || json.Unmarshal(raw, &staged) != nil || staged.ID == "" {
			t.Fatal("pending stage failed", err)
		}
		return staged.ID
	}
	unlockAfterRestart := func(expired string) {
		t.Helper()
		if _, err := os.Stat(filepath.Join(inbox, expired+".data")); !os.IsNotExist(err) {
			t.Fatal("pending plaintext survived broker restart", err)
		}
		raw, err := client.Call(ctx, appvalues.AdminRequest{Action: "protection_status"}, nil)
		var status struct {
			State string `json:"state"`
		}
		if err != nil || json.Unmarshal(raw, &status) != nil || status.State != "locked" && status.State != "locked_manual" {
			t.Fatalf("restarted broker retained unlock: %s %v", raw, err)
		}
		if _, err := client.Call(ctx, appvalues.AdminRequest{Action: "protection_unlock"}, []byte("synthetic-role-fixture-passphrase")); err != nil {
			t.Fatal("explicit unlock after restart", err)
		}
	}
	// The root source owner keeps legacy bytes outside the broker/agent UID.
	// Its actual host journal checkpoints the encrypted backup and every write
	// before this private helper can send bytes or approve equality delivery.
	legacy := secret.Controller{StateDirectory: filepath.Join(root, "migration-source")}
	if _, err := legacy.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := legacy.CreateProfile(ctx, "legacy-app"); err != nil {
		t.Fatal(err)
	}
	for _, item := range []struct{ name, value string }{{"TOKEN", "source-session-canary\n'\"<&>"}, {"EMPTY", ""}, {"URL", "https://public-migration.example"}} {
		if _, err := legacy.SetSecret(ctx, "legacy-app", item.name, item.value, false); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := legacy.ManagedCredentials().Set(ctx, secret.ManagedGitHubAppPrivateKey, "synthetic-platform-owner"); err != nil {
		t.Fatal(err)
	}
	plan, err := legacy.PrepareApplicationMigration(ctx, []secret.MigrationProfileMapping{{Profile: "legacy-app", Resource: migratedResource.ID, Environment: "production", Keys: []secret.MigrationKeyMapping{{Source: "TOKEN"}, {Source: "EMPTY"}, {Source: "URL", Name: "PUB", Kind: "variable"}}}}, []controlpolicy.ApplicationResource{migratedResource}, nil)
	if err != nil {
		t.Fatal(err)
	}
	journalRoot := filepath.Join(root, "migration-journal")
	if _, err := lifecycle.EnsureFileStore(journalRoot); err != nil {
		t.Fatal(err)
	}
	journalLock, err := lifecycle.AcquireOperationLock(journalRoot)
	if err != nil {
		t.Fatal(err)
	}
	defer journalLock.Close()
	journal, err := lifecycle.OpenOperationJournal(journalRoot, journalLock, lifecycle.OperationJournalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	recoveryIdentity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	archiveDir := filepath.Join(root, "migration-backup")
	if err := os.Mkdir(archiveDir, 0700); err != nil {
		t.Fatal(err)
	}
	intent := lifecycle.SecretMigrationIntent{Plan: plan, Before: appvalues.ApplicationIdentity{Version: 1, UID: 1000, GID: 1000, AgentUID: 10000, MetadataGID: 10000, DeploymentID: "deployment-" + strings.Repeat("a", 32), RunnerID: layout["runner_id"].(string), ActiveGeneration: strings.Repeat("a", 32)}, CandidateGeneration: strings.Repeat("b", 32), ExpectedPolicy: strings.Repeat("a", 64), Policy: strings.Repeat("a", 64), Mode: "native", SourceRoot: legacy.StateDirectory, IdentityFile: filepath.Join(root, "migration-identity.json"), StateRoot: filepath.Join(root, "migration-state"), ServiceStateRoot: "/var/lib/loki/secrets", RuntimeRoot: runtimeRoot, Binary: provider, Recipient: recoveryIdentity.Recipient().String(), BackupPath: filepath.Join(archiveDir, "source.age")}
	migrationRecord, err := journal.BeginSecretMigration(intent, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	migrationOperation = lifecycle.MigrationOperationUUID(migrationRecord.ID)
	sourceOwner := hostmigration.SourceOwner{Journal: journal, ID: migrationRecord.ID}
	if err := sourceOwner.Backup(ctx); err != nil {
		t.Fatal(err)
	}
	expired := pending()
	stopBroker(false)
	layout["migration_operation"] = migrationOperation
	encoded, _ = json.Marshal(layout)
	if err := os.WriteFile(layoutPath, encoded, 0644); err != nil {
		t.Fatal(err)
	}
	stopAfterRestart := startBroker()
	unlockAfterRestart(expired)
	write("true")
	checkpointed := false
	comparisonRequest.RequestID = "123e4567-e89b-42d3-a456-426614174493"
	comparison, err := client.CompareMigration(ctx, migrationOperation, comparisonRequest, expectedValues, func(binding appvalues.MigrationComparisonBinding) error {
		if binding.Resource != resource.ID || binding.Operation != migrationOperation || binding.Attempt != comparisonRequest.RequestID {
			t.Fatal("private IPC returned a foreign binding")
		}
		checkpointed = true
		return nil
	})
	if err != nil || !comparison.Matched || comparison.Count != 1 || !checkpointed {
		t.Fatal("staged two-phase private comparison", "checkpointed", checkpointed, err)
	}
	checkpointError := errors.New("synthetic host checkpoint failure")
	comparisonRequest.RequestID = "123e4567-e89b-42d3-a456-426614174494"
	if result, err := client.CompareMigration(ctx, migrationOperation, comparisonRequest, expectedValues, func(appvalues.MigrationComparisonBinding) error { return checkpointError }); !errors.Is(err, checkpointError) || result.Matched {
		t.Fatal("failed host checkpoint authorized comparison delivery", err)
	}
	lostWriteAck := true
	sourceSession, err := sourceOwner.Session(client, nil)
	if err != nil {
		t.Fatal(err)
	}
	sourceSession.Checkpoint = func(event hostmigration.SourceEvent) error {
		encoded, err := json.Marshal(event)
		if err != nil || bytes.Contains(encoded, []byte("source-session-canary")) || bytes.Contains(encoded, []byte("public-migration.example")) || bytes.Contains(encoded, []byte("synthetic-platform-owner")) {
			t.Fatal("source values entered host journal metadata", err)
		}
		if event.Type == "write_completed" && event.Write.Name == "TOKEN" && lostWriteAck {
			lostWriteAck = false
			return checkpointError
		}
		return sourceOwner.Checkpoint(event)
	}
	if err := sourceSession.Transfer(ctx); !errors.Is(err, checkpointError) {
		t.Fatal("synthetic lost write acknowledgement not exercised", err)
	}
	reopened, err := lifecycle.OpenOperationJournal(journalRoot, journalLock, lifecycle.OperationJournalOptions{Failpoint: func(name string) error {
		if name == "after_publish:applying:switch" {
			return checkpointError
		}
		return nil
	}})
	if err != nil {
		t.Fatal(err)
	}
	journal = reopened
	current, found, err := journal.Active()
	if err != nil || !found || current.ID != migrationRecord.ID {
		t.Fatal("lost original host source operation", err)
	}
	sourceOwner.Journal = journal
	expired = pending()
	sourceSession, err = sourceOwner.Session(client, nil)
	if err != nil {
		t.Fatal(err)
	}
	producer := exec.CommandContext(ctx, worker, "host", "secret-migration-source")
	producer.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent"}
	producerInput, err := producer.StdinPipe()
	if err != nil {
		t.Fatal(err)
	}
	producerOutput, err := producer.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	var producerDiagnostics bytes.Buffer
	producer.Stderr = &producerDiagnostics
	if err := producer.Start(); err != nil {
		t.Fatal(err)
	}
	producerWaited := false
	t.Cleanup(func() {
		if !producerWaited {
			_ = producer.Process.Kill()
			_ = producer.Wait()
		}
	})
	producerChannel := &sourceMetadataCapture{input: producerOutput, output: producerInput}
	manifest := hostmigration.SourceTransferManifest{Version: 1, SourceRoot: legacy.StateDirectory, TargetSocket: client.Socket, TargetUID: client.ExpectedUID, Operation: migrationOperation, Policy: intent.Policy, Plan: plan, BaseEnvironment: nil, Writes: sourceSession.Writes}
	if err := hostmigration.WriteSourceTransferManifest(producerChannel, manifest); err != nil {
		t.Fatal("root source helper manifest", err)
	}
	if err := hostmigration.CheckpointSourceTransfer(ctx, producerChannel, migrationOperation, sourceOwner.Checkpoint); !errors.Is(err, checkpointError) {
		t.Fatal("sealed candidate ready checkpoint loss not exercised", err)
	}
	producerErr := producer.Wait()
	producerWaited = true
	if producerErr == nil {
		t.Fatal("root helper did not stop after host checkpoint NACK")
	}
	for _, value := range []string{"source-session-canary", "public-migration.example", "synthetic-platform-owner"} {
		if bytes.Contains(producerChannel.metadata.Bytes(), []byte(value)) || bytes.Contains(producerDiagnostics.Bytes(), []byte(value)) {
			t.Fatal("root source helper exposed values through host metadata channel")
		}
	}
	journal, err = lifecycle.OpenOperationJournal(journalRoot, journalLock, lifecycle.OperationJournalOptions{})
	if err != nil {
		t.Fatal(err)
	}
	if current, found, err := journal.Active(); err != nil || !found || current.Phase != lifecycle.PhaseSwitch {
		t.Fatal("source/private delivery did not cover every mapped slot", err)
	}
	if _, err := legacy.PrepareApplicationMigration(ctx, nil, plan.Resources, nil); !errors.Is(err, secret.ErrApplicationMigration) {
		t.Fatal("prepared a second migration over the fenced source", err)
	}
	write("fenced")
	exportRevision, err := client.Call(ctx, appvalues.AdminRequest{Action: "revision", Resource: migratedResource.Alias}, nil)
	if err != nil {
		t.Fatal("root application backup revision", err)
	}
	var backupRevision struct {
		Revision string `json:"revision"`
	}
	if json.Unmarshal(exportRevision, &backupRevision) != nil {
		t.Fatal("invalid application backup revision metadata")
	}
	backupRequest := appvalues.AdminRequest{Action: "backup_export", Resource: migratedResource.Alias, Revision: backupRevision.Revision, RequestID: migrationOperation, Recipient: recoveryIdentity.Recipient().String()}
	exportProof, err := client.ExportApplication(ctx, backupRequest, migratedResource.ID, intent.Policy)
	if err != nil {
		t.Fatal("root private application-only logical export", err)
	}
	stopAfterRestart(true)
	_ = startBroker()
	unlockAfterRestart(expired)
	write("fenced")
	if replayed, err := client.ExportApplication(ctx, backupRequest, migratedResource.ID, intent.Policy); err != nil || replayed != exportProof {
		t.Fatal("broker restart changed original encrypted logical export", err)
	}
	if _, err := client.Call(ctx, appvalues.AdminRequest{Action: "maintenance_end", RequestID: migrationOperation}, nil); err != nil {
		t.Fatal("original private application maintenance release", err)
	}
	write("true")
}

func TestApplicationRoleClient(t *testing.T) {
	mode := os.Getenv("LOKI_ROLE_MODE")
	if mode == "" {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	uid := uint32(1000)
	client := rpc.Client{Socket: os.Getenv("LOKI_ROLE_METADATA"), ExpectedUID: &uid}
	result, err := client.Call(ctx, map[string]any{"operation": "inspect", "action": "resources"})
	if mode == "foreign" {
		if err == nil {
			t.Fatal("foreign kernel UID admitted")
		}
		return
	}
	if err != nil || !bytes.Contains(result, []byte(`"alias":"demo"`)) {
		t.Fatal("configured agent could not inspect its resource", err)
	}
	if raw := os.Getenv("LOKI_ROLE_WRITE"); raw != "" {
		var request map[string]any
		if json.Unmarshal([]byte(raw), &request) != nil {
			t.Fatal("invalid fixture metadata")
		}
		out, err := client.Call(ctx, request)
		if os.Getenv("LOKI_ROLE_REPLAYED") == "fenced" {
			if err == nil || fault.Describe(err).Code != fault.CodeUnavailable {
				t.Fatal("agent mutation bypassed persistent application maintenance", err)
			}
			return
		}
		if err != nil {
			t.Fatal(err)
		}
		var response struct {
			Replayed bool `json:"replayed"`
			Count    int  `json:"count"`
		}
		if json.Unmarshal(out, &response) != nil || response.Replayed != (os.Getenv("LOKI_ROLE_REPLAYED") == "true") || response.Count != 1 || bytes.Contains(out, []byte("synthetic-private-restart-canary")) {
			t.Fatal("original receipt or value-free response was lost")
		}
	}
	for _, path := range []string{os.Getenv("LOKI_ROLE_PROVIDER"), os.Getenv("LOKI_ROLE_INBOX")} {
		if entries, err := os.ReadDir(path); err == nil {
			t.Fatalf("agent read private directory containing %d entries", len(entries))
		}
	}
	for _, socket := range []string{os.Getenv("LOKI_ROLE_ADMIN"), os.Getenv("LOKI_ROLE_JOBS")} {
		if conn, err := net.Dial("unix", socket); err == nil {
			conn.Close()
			t.Fatal("agent reached privileged socket")
		}
	}
	for _, request := range []map[string]any{
		{"operation": "inspect", "action": "resources", "profile": "platform"},
		{"operation": "inspect", "action": "resources", "env": nil},
		{"operation": "set_secret", "resource": "demo", "value": "unaccepted"},
	} {
		if _, err := client.Call(ctx, request); err == nil {
			t.Fatal("metadata authority expanded")
		}
	}
}
