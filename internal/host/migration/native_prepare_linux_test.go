//go:build linux

package migration

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"filippo.io/age"
	"loki/internal/config"
	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/execution"
	hostapplication "loki/internal/host/application"
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
	application := hostapplication.PrepareOptions{UID: 10006, GID: 10006, AgentUID: 10000, MetadataGID: 10001, Principal: "agent", IdentityFile: filepath.Join(root, "identity.json"), StateRoot: filepath.Join(root, "app-state"), ServiceStateRoot: "/var/lib/loki-go/secrets", RuntimeRoot: "/run/loki-go/secrets", Binary: "/opt/loki/bin/devtools", Configuration: c, Contract: contract}
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
	options := NativePrepareOptions{Application: application, SourceRoot: source.StateDirectory, Recipient: identity.Recipient().String(), BackupPath: filepath.Join(directory, "source.age"), Mappings: []appvalues.MigrationProfileMapping{{Profile: "web", Resource: resource.ID, Environment: "common", Keys: []appvalues.MigrationKeyMapping{{Source: "TOKEN", Name: "TOKEN", Kind: "secret"}}}}}
	intent, err := PrepareNative(t.Context(), options)
	if err != nil || !intent.Valid() || intent.Mode != "native" || intent.Before != before.Identity || intent.Plan.Values != 1 || intent.Plan.Revision == 0 || intent.CandidateGeneration == before.Identity.ActiveGeneration || !strings.HasPrefix(intent.RuntimeRoot, "/run/loki-go/secret-migration-") {
		t.Fatal("native preparation did not bind the actual source and original identity", err)
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
	options.Recipient = "invalid-recipient"
	options.SourceRoot = filepath.Join(root, "missing-source")
	if _, err := PrepareNative(t.Context(), options); err == nil {
		t.Fatal("invalid recovery recipient accepted")
	}
	if _, err := os.Lstat(options.SourceRoot); !os.IsNotExist(err) {
		t.Fatal("invalid public input initialized source state", err)
	}
}
