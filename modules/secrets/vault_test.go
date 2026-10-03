package secret

import (
	"context"
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
