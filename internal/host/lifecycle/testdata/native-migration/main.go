// This executable exists only in the disposable native acceptance VM.
package main

import (
	"context"
	"crypto/rand"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"time"

	"filippo.io/age"
	"loki/internal/admin"
	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/host/lifecycle"
	"loki/internal/platform/repository"
	"loki/internal/state"
	"loki/internal/tools"
)

const operator = "/var/lib/loki-migration-operator"

func main() {
	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Second)
	defer cancel()
	if os.Geteuid() != 0 || len(os.Args) != 2 {
		os.Exit(2)
	}
	if err := run(ctx, os.Args[1]); err != nil {
		fmt.Fprintln(os.Stderr, "native migration fixture failed:", err)
		os.Exit(1)
	}
}

func write(path string, value any, private bool) error {
	body, err := json.Marshal(value)
	if err != nil {
		return err
	}
	defer clear(body)
	mode := os.FileMode(0640)
	if private {
		mode = 0600
	}
	if err := os.WriteFile(path, body, mode); err != nil {
		return err
	}
	if err := os.Chmod(path, mode); err != nil {
		return err
	}
	if !private {
		return os.Chown(path, 0, 21001)
	}
	return nil
}

func run(ctx context.Context, action string) error {
	switch action {
	case "seed-compose":
		return seedLegacy(ctx, "/var/lib/loki/runtime")
	case "configure":
		if err := os.Mkdir(operator, 0700); err != nil {
			return err
		}
		identity, err := age.GenerateX25519Identity()
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(operator, "identity"), []byte(identity.String()), 0600); err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(operator, "recipient"), []byte(identity.Recipient().String()), 0600); err != nil {
			return err
		}
		var nonce [48]byte
		if _, err := rand.Read(nonce[:]); err != nil {
			return err
		}
		pass := []byte(base64.RawURLEncoding.EncodeToString(nonce[:]))
		clear(nonce[:])
		defer clear(pass)
		if err := os.WriteFile(filepath.Join(operator, "passphrase"), pass, 0600); err != nil {
			return err
		}
		if err := seedLegacy(ctx, "/var/lib/loki-go/runtime"); err != nil {
			return err
		}
		repo, err := repository.New("/srv/workspace/loki")
		if err != nil {
			return err
		}
		defer repo.Close()
		repoID, err := repo.Identity(".")
		if err != nil {
			return err
		}
		resource := controlpolicy.ApplicationResource{ID: "9af6c2e6-c1d4-41a3-a6e9-9fb3c07c67d2", Alias: "fixture", Repository: repoID, Environments: []string{"common"}, Keys: map[string]string{"TOKEN": "secret", "API_URL": "variable"}, Network: "none", Principals: []controlpolicy.ApplicationGrant{{Principal: "agent", Metadata: true, Write: true, Execute: true}}}
		mapping := []appvalues.MigrationProfileMapping{{Profile: "legacy-app", Resource: resource.ID, Environment: "common", Keys: []appvalues.MigrationKeyMapping{{Source: "API_URL", Name: "API_URL", Kind: "variable"}, {Source: "TOKEN", Name: "TOKEN", Kind: "secret"}}}}
		if err := write(filepath.Join(operator, "mapping.json"), map[string]any{"resources": []controlpolicy.ApplicationResource{resource}, "mappings": mapping}, true); err != nil {
			return err
		}
		if err := write("/etc/loki-go/migration-tools.json", tools.Config{Schema: 1, Release: tools.Release, Host: tools.Host{Kind: "local"}, Mode: tools.Full, Tools: []tools.Selection{{ID: "secrets", Enabled: true}}}, false); err != nil {
			return err
		}
		path := "/etc/loki-go/mcp.json"
		body, err := os.ReadFile(path)
		var layout map[string]any
		if err != nil || json.Unmarshal(body, &layout) != nil {
			return errors.New("native MCP fixture layout unavailable")
		}
		layout["Tools"], layout["ToolsConfigPath"] = []string{"secrets"}, "/etc/loki-go/migration-tools.json"
		if err := write(path, layout, false); err != nil {
			return err
		}
		if err := os.WriteFile("/etc/loki-go/token", []byte("synthetic-native-migration-token-1234567890123"), 0640); err != nil {
			return err
		}
		if err := os.Chmod("/etc/loki-go/token", 0640); err != nil {
			return err
		}
		return os.Chown("/etc/loki-go/token", 0, 21001)
	case "initialize-candidate":
		lock, err := lifecycle.AcquireOperationLock("/var/lib/loki-go/native-lifecycle")
		if err != nil {
			return err
		}
		defer lock.Close()
		j, err := lifecycle.OpenOperationJournal("/var/lib/loki-go/native-lifecycle", lock, lifecycle.OperationJournalOptions{})
		if err != nil {
			return err
		}
		r, found, err := j.Active()
		if err != nil || !found || r.SecretMigration == nil || r.Phase != lifecycle.PhaseMigrate {
			return errors.New("original migration is not awaiting manual protection")
		}
		pass, err := os.ReadFile(filepath.Join(operator, "passphrase"))
		if err != nil {
			return err
		}
		defer clear(pass)
		_, err = (admin.ApplicationClient{Socket: filepath.Join(r.SecretMigration.Intent.RuntimeRoot, "admin/control.sock"), ExpectedUID: r.SecretMigration.Intent.Before.UID}).Call(ctx, appvalues.AdminRequest{Action: "protection_init"}, pass)
		return err
	case "verify", "verify-rollback":
		lock, err := lifecycle.AcquireOperationLock("/var/lib/loki-go/native-lifecycle")
		if err != nil {
			return err
		}
		defer lock.Close()
		j, err := lifecycle.OpenOperationJournal("/var/lib/loki-go/native-lifecycle", lock, lifecycle.OperationJournalOptions{})
		if err != nil {
			return err
		}
		if _, found, err := j.Active(); err != nil || found {
			return errors.New("native migration still requires release recovery")
		}
		records, err := j.List()
		if err != nil || len(records) != 1 {
			return errors.New("native original-operation history was not retained")
		}
		want := lifecycle.OperationSucceeded
		if action == "verify-rollback" {
			want = lifecycle.OperationRolledBack
		}
		for _, r := range records {
			if r.SecretMigration == nil || !r.Maintenance.Released || r.SecretMigration.Archive == nil || !r.SecretMigration.Archive.Published {
				return errors.New("native source archive or release proof is missing")
			}
			if err := lifecycle.VerifyMigrationSourceArchive(r.SecretMigration.Intent.BackupPath, r.SecretMigration.Archive); err != nil {
				return err
			}
			if r.State != want {
				return errors.New("native original operation did not retain the scenario completion")
			}
		}
		return nil
	default:
		return errors.New("unsupported native migration fixture action")
	}
}

// Test-only source construction survives removal of the public legacy writer.
// Random values stay in memory and the actual encrypted legacy store.
func seedLegacy(ctx context.Context, directory string) error {
	if err := os.MkdirAll(directory, 0700); err != nil {
		return err
	}
	entries, err := os.ReadDir(directory)
	if err != nil {
		return err
	}
	if len(entries) == 0 {
		if err := os.Chmod(directory, 0700); err != nil {
			return err
		}
	}
	var nonce [48]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	token := []byte(base64.RawURLEncoding.EncodeToString(nonce[:]))
	clear(nonce[:])
	defer clear(token)
	source := state.Store{Dir: directory}
	if _, err := source.Initialize(ctx, json.RawMessage(`{"version":1,"profiles":{},"requests":{}}`)); err != nil {
		return err
	}
	_, err = source.Update(ctx, nil, func(raw json.RawMessage) (json.RawMessage, error) {
		var doc map[string]any
		if json.Unmarshal(raw, &doc) != nil {
			return nil, errors.New("legacy fixture document unavailable")
		}
		profiles, ok := doc["profiles"].(map[string]any)
		if !ok {
			return nil, errors.New("legacy fixture profiles unavailable")
		}
		profiles["legacy-app"] = map[string]any{"secrets": map[string]any{"TOKEN": string(token), "API_URL": "  https://public.example.invalid/?a=<value>&b=2\n"}}
		return json.Marshal(doc)
	})
	return err
}
