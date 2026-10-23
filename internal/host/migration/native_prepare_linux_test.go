//go:build linux

package migration

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"loki/internal/config"
	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/execution"
	hostapplication "loki/internal/host/application"
	"loki/internal/host/lifecycle"
	secret "loki/modules/secrets"
)

func TestNativeMigrationPreparationPreservesSourceAndSelection(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("isolated root fixture supplies the actual native metadata owner")
	}
	c, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Root = "/workspace"
	contractPath := os.Getenv("LOKI_NATIVE_MIGRATION_TEST_CONTRACT")
	if contractPath == "" {
		contractPath = "../../../packaging/native/execution-contract.json"
	}
	raw, err := os.ReadFile(contractPath)
	if err != nil {
		t.Fatal(err)
	}
	contract, err := execution.Load(raw)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	runtimeParent, err := os.MkdirTemp("/dev/shm", "n-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(runtimeParent) })
	providerHost, brokerHost := os.Getenv("LOKI_SECRET_TEST_DEVTOOLS"), os.Getenv("LOKI_SECRET_TEST_BROKER")
	if providerHost == "" {
		providerHost = "/opt/loki/bin/devtools"
	}
	if brokerHost == "" {
		brokerHost = "/opt/loki/bin/loki-secret-broker"
	}
	_, providerErr := os.Stat(providerHost)
	_, brokerErr := os.Stat(brokerHost)
	withCandidate := providerErr == nil && brokerErr == nil
	if !withCandidate && os.Getenv("LOKI_REQUIRE_NATIVE_MIGRATION") == "1" {
		t.Fatal("mandatory native candidate executables are unavailable")
	}
	binary := "/opt/loki/bin/devtools"
	if withCandidate {
		directory := filepath.Join(root, "bin")
		if err := os.Mkdir(directory, 0755); err != nil {
			t.Fatal(err)
		}
		copyProgram := func(source, name string) string {
			t.Helper()
			body, err := os.ReadFile(source)
			path := filepath.Join(directory, name)
			if err != nil || os.WriteFile(path, body, 0555) != nil || os.Chmod(path, 0555) != nil {
				t.Fatal("owned native executable fixture", err)
			}
			return path
		}
		providerHost, brokerHost = copyProgram(providerHost, "devtools"), copyProgram(brokerHost, "loki-secret-broker")
		binary = providerHost
		if os.Getenv("LOKI_REQUIRE_NATIVE_MIGRATION") == "1" {
			// This mandatory test runs on a disposable runner. Use the installed
			// logical binary path: a provider below /tmp is hidden by PrivateTmp.
			if _, err := os.Lstat("/opt/loki"); !os.IsNotExist(err) {
				t.Fatal("preserving an existing /opt/loki; use an isolated native runner", err)
			}
			if err := os.Symlink(root, "/opt/loki"); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				if target, err := os.Readlink("/opt/loki"); err != nil || target != root {
					t.Error("fixture installation identity changed; preserving it", err)
				} else if err := os.Remove("/opt/loki"); err != nil {
					t.Error(err)
				}
			})
			binary = "/opt/loki/bin/devtools"
		}
	}
	application := hostapplication.PrepareOptions{UID: 10006, GID: 10006, AgentUID: 10000, MetadataGID: 10001, Principal: "agent", IdentityFile: filepath.Join(root, "identity.json"), StateRoot: filepath.Join(root, "app-state"), ServiceStateRoot: "/var/lib/loki-go/secrets", RuntimeRoot: "/run/loki-go/secrets", Binary: "/opt/loki/bin/devtools", Configuration: c, Contract: contract}
	application.Binary, application.RuntimeRoot = binary, filepath.Join(runtimeParent, "secrets")
	previousResource := controlpolicy.ApplicationResource{ID: "123e4567-e89b-42d3-a456-426614174592", Alias: "existing", Repository: "owner/existing", Network: "none", Environments: []string{"common"}, Keys: map[string]string{"OLD": "secret"}, Principals: []controlpolicy.ApplicationGrant{{Principal: "agent", Metadata: true}}}
	application.Resources = []controlpolicy.ApplicationResource{previousResource}
	before, err := hostapplication.PrepareState(application)
	if err != nil {
		t.Fatal(err)
	}
	source := secret.Controller{StateDirectory: filepath.Join(root, "source")}
	if _, err := source.Initialize(t.Context()); err != nil {
		t.Fatal(err)
	}
	if _, err := source.CreateProfile(t.Context(), "web"); err != nil {
		t.Fatal(err)
	}
	canary := "synthetic-native-prepare-value\nexact second line"
	if _, err := source.SetSecret(t.Context(), "web", "TOKEN", canary, false); err != nil {
		t.Fatal(err)
	}
	resource := controlpolicy.ApplicationResource{ID: "123e4567-e89b-42d3-a456-426614174591", Alias: "web", Repository: "owner/repo", Network: "none", Environments: []string{"common"}, Keys: map[string]string{"TOKEN": "secret"}, Principals: []controlpolicy.ApplicationGrant{{Principal: "agent", Metadata: true, Execute: true}}}
	application.Resources = []controlpolicy.ApplicationResource{resource}
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	directory := filepath.Join(root, "backup")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	options := NativePrepareOptions{Application: application, BeforeResources: []controlpolicy.ApplicationResource{previousResource}, SourceRoot: source.StateDirectory, Recipient: identity.Recipient().String(), BackupPath: filepath.Join(directory, "source.age"), Mappings: []appvalues.MigrationProfileMapping{{Profile: "web", Resource: resource.ID, Environment: "common", Keys: []appvalues.MigrationKeyMapping{{Source: "TOKEN", Name: "TOKEN", Kind: "secret"}}}}}
	intent, err := PrepareNative(t.Context(), options)
	if err != nil || !intent.Valid() || intent.Mode != "native" || intent.Before != before.Identity || intent.Plan.Values != 1 || intent.Plan.Revision == 0 || intent.CandidateGeneration == before.Identity.ActiveGeneration || !strings.HasPrefix(intent.RuntimeRoot, filepath.Join(runtimeParent, "secret-migration-")) {
		t.Fatal("native preparation did not bind the actual source and original identity", err)
	}
	if len(intent.BeforeResources) != 1 || intent.BeforeResources[0].ID != previousResource.ID || intent.BeforeResources[0].Alias != previousResource.Alias || intent.ExpectedPolicy == intent.Policy {
		t.Fatal("native journal intent lost the original catalog needed by rollback")
	}
	encoded, err := json.Marshal(intent)
	valueHash := sha256.Sum256([]byte(canary))
	if err != nil || bytes.Contains(encoded, []byte("synthetic-native-prepare-value")) || bytes.Contains(encoded, []byte(hex.EncodeToString(valueHash[:]))) || bytes.Contains(encoded, []byte(identity.String())) {
		t.Fatal("native public intent disclosed source or recovery material", err)
	}
	actual, err := hostapplication.ReadIdentity(application.IdentityFile)
	if err != nil || actual != before.Identity {
		t.Fatal("prepare changed selection", err)
	}
	if state, err := source.ApplicationMigrationState(t.Context()); err != nil || state != nil {
		t.Fatal("prepare froze the source", err)
	}
	if _, err := os.Lstat(intent.BackupPath); !os.IsNotExist(err) {
		t.Fatal("prepare created a backup before admission", err)
	}
	if entries, err := os.ReadDir(filepath.Join(application.StateRoot, "generations")); err != nil || len(entries) != 1 {
		t.Fatal("prepare materialized a candidate generation", err)
	}
	if _, err := os.Lstat(intent.RuntimeRoot); !os.IsNotExist(err) {
		t.Fatal("prepare materialized candidate runtime state", err)
	}
	if withCandidate {
		journalRoot := filepath.Join(root, "journal")
		if _, err := lifecycle.EnsureFileStore(journalRoot); err != nil {
			t.Fatal(err)
		}
		lock, err := lifecycle.AcquireOperationLock(journalRoot)
		if err != nil {
			t.Fatal(err)
		}
		defer lock.Close()
		journal, err := lifecycle.OpenOperationJournal(journalRoot, lock, lifecycle.OperationJournalOptions{})
		if err != nil {
			t.Fatal(err)
		}
		record, err := journal.BeginSecretMigration(intent, time.Now())
		if err != nil {
			t.Fatal(err)
		}
		owner := SourceOwner{Journal: journal, ID: record.ID}
		current := application
		current.Resources = options.BeforeResources
		for attempt := 0; attempt < 2; attempt++ {
			candidate, err := owner.PrepareNativeCandidate(current, brokerHost, providerHost)
			if err != nil || candidate.Operation != lifecycle.MigrationOperationUUID(record.ID) || candidate.Preparation.Identity.ActiveGeneration != intent.CandidateGeneration || candidate.Preparation.Layout.PolicySHA256 != intent.Policy {
				t.Fatal("original native candidate binding/replay", err)
			}
			if entries, err := os.ReadDir(filepath.Join(candidate.Preparation.HostGeneration, "dvt")); err != nil || len(entries) != 0 {
				t.Fatal("native preparation initialized cryptographic state", err)
			}
			if attempt == 0 && os.Getenv("LOKI_REQUIRE_NATIVE_MIGRATION") == "1" {
				t.Cleanup(func() {
					ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
					defer cancel()
					if err := candidate.Stop(ctx); err != nil {
						t.Error("owned preparation fixture cleanup", err)
					}
				})
				if err := candidate.Start(t.Context()); err != nil {
					metadata, _ := nativeCommand(t.Context(), "/usr/bin/journalctl", "--unit="+candidate.unit(), "--no-pager", "--output=cat", "--lines=20")
					t.Logf("owned uninitialized candidate startup: %s", metadata)
					t.Fatal("original owner's prepared chroot could not start the real broker", err)
				}
				if state, err := candidate.Status(t.Context()); err != nil || state != "uninitialized" {
					t.Fatal("owner preparation implicitly initialized protection", state, err)
				}
				if err := owner.StopNativeCandidate(t.Context()); err != nil {
					t.Fatal("original owner could not stop its prepared broker", err)
				}
				if err := os.Chmod(candidate.RootDirectory, 0700); err != nil {
					t.Fatal(err)
				}
			}
			if attempt == 1 {
				if info, err := os.Stat(candidate.RootDirectory); err != nil || info.Mode().Perm() != 0755 {
					t.Fatal("original-operation recovery did not restore a searchable chroot", err)
				}
			}
		}
		if actual, err := hostapplication.ReadIdentity(application.IdentityFile); err != nil || actual != before.Identity {
			t.Fatal("inactive native preparation changed the selector", err)
		}
		if err := owner.Backup(t.Context()); err != nil {
			t.Fatal("native original encrypted source backup", err)
		}
		backedUp, active, err := journal.Active()
		if err != nil || !active || backedUp.SecretMigration.Archive == nil || !backedUp.SecretMigration.Archive.Published || lifecycle.VerifyMigrationSourceArchive(intent.BackupPath, backedUp.SecretMigration.Archive) != nil {
			t.Fatal("native source archive publication was not independently verified", err)
		}
		if _, err := owner.SelectNativeCandidate(t.Context(), current); err == nil {
			t.Fatal("native selection bypassed actual private value comparison")
		}
		if _, err := source.SetSecret(t.Context(), "web", "TOKEN", "must-remain-fenced", true); err == nil {
			t.Fatal("native source writer escaped the original pending gate")
		}
		if _, err := journal.RequestSecretMigrationRollback(record.ID, time.Now()); err != nil {
			t.Fatal(err)
		}
		if _, err := owner.PrepareNativeCandidate(current, brokerHost, providerHost); err == nil {
			t.Fatal("native candidate preparation resumed after rollback choice")
		}
		if err := owner.FinishNativeSource(t.Context()); err == nil {
			t.Fatal("native source gate released before terminal host evidence")
		}
		if _, err := journal.CompleteSecretMigration(record.ID, true, time.Now()); err != nil {
			t.Fatal(err)
		}
		for attempt := 0; attempt < 2; attempt++ {
			if err := owner.FinishNativeSource(t.Context()); err != nil {
				t.Fatal("native original source disposition/ACK replay", err)
			}
		}
		if fence, err := source.ApplicationMigrationState(t.Context()); err != nil || fence != nil {
			t.Fatal("native source rollback did not reopen the original store", err)
		}
		pending, active, err := journal.Active()
		if err != nil || !active || pending.ID != record.ID || pending.Maintenance.Released {
			t.Fatal("native source ACK bypassed the remaining app/Job gate owners", err)
		}
	}
	options.Recipient = "invalid-recipient"
	options.SourceRoot = filepath.Join(root, "missing-source")
	if _, err := PrepareNative(t.Context(), options); err == nil {
		t.Fatal("invalid recovery recipient accepted")
	}
	if _, err := os.Lstat(options.SourceRoot); !os.IsNotExist(err) {
		t.Fatal("invalid public input initialized source state", err)
	}
}
