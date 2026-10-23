//go:build linux

package application

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	provider "loki/modules/secrets/devtools"
)

func verifyApplicationRestore(t *testing.T, ctx context.Context, binary string, serving *Service, resource controlpolicy.ApplicationResource, identity, ciphertext []byte) {
	t.Helper()
	body, err := appvalues.EncodeApplicationRestore(identity, ciphertext)
	if err != nil {
		t.Fatal(err)
	}
	defer clear(body)
	sum := sha256.Sum256(ciphertext)
	const operation = "3583c818-a495-4b1c-a301-9ad5514c9942"
	q := AdminRequest{Action: "backup_import_preview", Resource: resource.Alias, Operation: operation, ArchiveSHA256: hex.EncodeToString(sum[:]), RequestID: "3188d59a-dfeb-4585-92af-ef59fe752d4c"}
	if _, err := serving.Admin(ctx, q, body); err == nil {
		t.Fatal("ordinary serving generation accepted logical restore")
	}
	root := t.TempDir()
	if err := os.Chmod(root, 0700); err != nil {
		t.Fatal(err)
	}
	inbox, err := os.MkdirTemp("/dev/shm", "loki-restore-test-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(inbox)
	p, err := provider.New(ctx, binary, filepath.Join(root, "dvt"))
	if err != nil {
		t.Fatal(err)
	}
	options := Options{Provider: p, Root: root, Inbox: inbox, DeploymentID: "restore-candidate", PolicySHA256: strings.Repeat("b", 64), Resources: []controlpolicy.ApplicationResource{resource}, RestoreOperation: operation}
	s, err := New(options)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := s.Admin(ctx, q, body); err == nil {
		t.Fatal("logical restore escaped original maintenance gate")
	}
	if _, err := s.Admin(ctx, AdminRequest{Action: "maintenance_begin", RequestID: operation}, nil); err != nil {
		t.Fatal(err)
	}
	beforeInit, err := s.Admin(ctx, q, body)
	if err != nil {
		t.Fatal("read-only restore preview before initialization", err)
	}
	applyBeforeInit := q
	applyBeforeInit.Action, applyBeforeInit.Revision = "backup_import", beforeInit["digest"].(string)
	if _, err := s.Admin(ctx, applyBeforeInit, body); err == nil {
		t.Fatal("restore applied without manual initialization")
	}
	if status, err := s.Admin(ctx, AdminRequest{Action: "protection_status"}, nil); err != nil || status["state"] != "uninitialized" {
		t.Fatal("restore implicitly initialized protection", err)
	}
	pass := []byte("synthetic-new-restore-protection")
	defer clear(pass)
	if _, err := s.Admin(ctx, AdminRequest{Action: "protection_init"}, pass); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		c, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		_, _ = p.Protection(c, "lock", nil)
	})
	preview, err := s.Admin(ctx, q, body)
	if err != nil || preview["target_exists"] != false {
		t.Fatal("actual candidate import preview", err)
	}
	q.Action, q.Revision = "backup_import", preview["digest"].(string)
	result, err := s.Admin(ctx, q, body)
	if err != nil || result["changed"] != true || result["replayed"] != false {
		t.Fatal("actual app-only candidate import", err)
	}
	if result, err := s.Admin(ctx, q, body); err != nil || result["replayed"] != true {
		t.Fatal("lost import ACK did not replay original receipt", err)
	}
	changed := q
	changed.RequestID = "9861d99c-1a42-4ec0-a49b-7d4290b4f6dc"
	if _, err := s.Admin(ctx, changed, body); err == nil {
		t.Fatal("new request replaced restored profile")
	}
	changed = q
	changed.Operation = changed.RequestID
	if _, err := s.Admin(ctx, changed, body); err == nil {
		t.Fatal("another operation adopted candidate restore")
	}
	entries, err := os.ReadDir(inbox)
	if err != nil || len(entries) != 0 {
		t.Fatal("logical restore left private identity or ciphertext staging", err)
	}
	orphan, err := os.MkdirTemp(inbox, ".restore-"+strings.ReplaceAll(operation, "-", "")+"-")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(orphan, "identity"), identity, 0600); err != nil {
		t.Fatal(err)
	}
	s, err = New(options)
	if err != nil {
		t.Fatal("candidate restart orphan cleanup", err)
	}
	if _, err := os.Lstat(orphan); !os.IsNotExist(err) {
		t.Fatal("restart retained private backup identity", err)
	}
	if err := s.LockOnStart(ctx); err != nil {
		t.Fatal(err)
	}
	if result, err := s.Admin(ctx, AdminRequest{Action: "protection_status"}, nil); err != nil || result["state"] == "unlocked" {
		t.Fatal("restored generation retained a live protection session", err)
	}
}
