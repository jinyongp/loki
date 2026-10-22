//go:build linux

package application

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loki/internal/control/appvalues"
	provider "loki/modules/secrets/devtools"
)

func TestRealManualProtectionDoesNotInitializeOrExtendOnStatus(t *testing.T) {
	binary := os.Getenv("LOKI_SECRET_TEST_DEVTOOLS")
	if binary == "" {
		if os.Getenv("LOKI_REQUIRE_SECRET_PROVIDER") == "1" {
			t.Fatal("pinned provider required")
		}
		t.Skip("native values suite supplies the pinned provider")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	root := t.TempDir()
	if os.Chmod(root, 0700) != nil {
		t.Fatal("private fixture root")
	}
	inbox, err := os.MkdirTemp("/dev/shm", "loki-protection-stage-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(inbox)
	data := filepath.Join(root, "dvt")
	p, err := provider.New(ctx, binary, data)
	if err != nil {
		t.Fatal(err)
	}
	s, err := New(Options{Provider: p, Root: root, Inbox: inbox, DeploymentID: "1bbf75ed-1b51-46bd-8255-31536e945564", PolicySHA256: strings.Repeat("b", 64)})
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LockOnStart(ctx); err != nil {
		t.Fatal(err)
	}
	call := func(action string, body []byte) map[string]any {
		t.Helper()
		out, err := s.Admin(ctx, appvalues.AdminRequest{Action: "protection_" + action}, body)
		if err != nil {
			t.Fatalf("protection %s failed: %v", action, err)
		}
		return out
	}
	if out := call("status", nil); out["state"] != "uninitialized" {
		t.Fatal("service initialized the vault")
	}
	if _, err := s.Admin(ctx, appvalues.AdminRequest{Action: "protection_unlock"}, []byte("wrong-before-init")); err == nil {
		t.Fatal("uninitialized vault accepted unlock")
	}
	if out := call("status", nil); out["state"] != "uninitialized" {
		t.Fatal("failed unlock initialized the vault")
	}
	pass := []byte("synthetic-manual-private-passphrase")
	defer clear(pass)
	if out := call("init", pass); out["state"] != "unlocked" {
		t.Fatal("explicit passphrase initialization did not unlock")
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_ = exec.CommandContext(cleanup, binary, "vault", "lock", "--isolated-root", data).Run()
	})
	before, err := p.ProtectionStatus(ctx)
	if err != nil || before.IdleExpiresAt == nil {
		t.Fatal("warm session omitted idle metadata", err)
	}
	call("status", nil)
	call("status", nil)
	after, err := p.ProtectionStatus(ctx)
	if err != nil || after.IdleExpiresAt == nil || !after.IdleExpiresAt.Equal(*before.IdleExpiresAt) {
		t.Fatal("status extended the key session")
	}
	call("lock", nil)
	if out := call("status", nil); out["state"] != "locked_manual" && out["state"] != "locked" {
		t.Fatal("explicit lock was not retained")
	}
	if _, err := s.Admin(ctx, appvalues.AdminRequest{Action: "protection_unlock"}, []byte("wrong-after-init")); err == nil {
		t.Fatal("wrong passphrase unlocked the vault")
	}
	call("unlock", pass)
	call("recover", nil)
	// A new broker application object cannot implicitly create a new session.
	call("lock", nil)
	call("unlock", pass)
	s, err = New(s.options)
	if err != nil {
		t.Fatal(err)
	}
	if err := s.LockOnStart(ctx); err != nil {
		t.Fatal(err)
	}
	if out := call("status", nil); out["state"] != "locked_manual" && out["state"] != "locked" {
		t.Fatal("service reconstruction unlocked the vault")
	}
	audit, err := os.ReadFile(filepath.Join(root, "audit.jsonl"))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(audit, []byte(`"principal":"service"`)) {
		t.Fatal("restart lock omitted service attribution")
	}
	for _, secret := range [][]byte{pass, []byte("wrong-before-init"), []byte("wrong-after-init")} {
		if bytes.Contains(audit, secret) {
			t.Fatal("passphrase appeared in protection audit")
		}
	}
}
