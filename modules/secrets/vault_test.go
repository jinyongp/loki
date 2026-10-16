package secret

import (
	"context"
	"loki/internal/fault"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestEncryptedVaultLifecycle(t *testing.T) {
	ctx := context.Background()
	directory := filepath.Join(t.TempDir(), "vault")
	if err := os.Mkdir(directory, 0700); err != nil {
		t.Fatal(err)
	}
	c := Controller{StateDirectory: directory}
	if _, err := c.Initialize(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := c.CreateProfile(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ImportValues(ctx, "web", map[string]string{"TOKEN": "synthetic-private"}); err != nil {
		t.Fatal(err)
	}
	plan, err := c.ResolveEnvironment(ctx, "web", []string{"TOKEN"})
	if err != nil || plan.Entries()[0] != "TOKEN=synthetic-private" {
		t.Fatal(plan.Names(), err)
	}
	data, err := os.ReadFile(filepath.Join(c.StateDirectory, "store.json"))
	if err != nil || strings.Contains(string(data), "synthetic-private") {
		t.Fatal("encrypted store exposed plaintext", err)
	}
	if _, err = c.RemoveSecret(ctx, "web", "TOKEN"); err != nil {
		t.Fatal(err)
	}
	if _, err = c.RemoveProfile(ctx, "web"); err != nil {
		t.Fatal(err)
	}
}

func TestPartialVaultHasPublicRecoveryGuidance(t *testing.T) {
	dir := t.TempDir()
	if err := os.Chmod(dir, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "master.key"), make([]byte, 32), 0600); err != nil {
		t.Fatal(err)
	}
	_, err := (Controller{StateDirectory: dir}).Initialize(t.Context())
	detail := fault.Describe(err)
	if detail.Code != fault.CodeUnavailable || detail.Retryable || !strings.Contains(detail.Message, "Application secrets") || !strings.Contains(detail.NextAction, "loki backups list") || strings.Contains(detail.Message, dir) {
		t.Fatalf("unhelpful/private recovery guidance: %+v", detail)
	}
}
