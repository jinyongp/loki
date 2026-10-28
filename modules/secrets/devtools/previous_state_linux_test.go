//go:build linux

package devtools

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
)

func TestRealPreviousProviderState(t *testing.T) {
	previous, binary := os.Getenv("LOKI_DEVTOOLS_PREVIOUS_BINARY"), os.Getenv("LOKI_SECRET_TEST_DEVTOOLS")
	if previous == "" || binary == "" {
		if os.Getenv("LOKI_REQUIRE_SECRET_PROVIDER") == "1" {
			t.Fatal("both reviewed providers are required for upgrade verification")
		}
		t.Skip("native values suite supplies both reviewed providers")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	root := filepath.Join(t.TempDir(), "provider")
	legacy := func(input string, args ...string) json.RawMessage {
		t.Helper()
		cmd := exec.CommandContext(ctx, previous, append(args, "--isolated-root", root)...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=/nonexistent", "GOMAXPROCS=2"}
		cmd.Stdin = strings.NewReader(input)
		raw, err := cmd.Output()
		if err != nil {
			t.Fatalf("previous provider %s failed: %v", args[0], err)
		}
		var envelope struct {
			OK   bool            `json:"ok"`
			Data json.RawMessage `json:"data"`
		}
		if json.Unmarshal(raw, &envelope) != nil || !envelope.OK {
			t.Fatal("invalid previous provider response")
		}
		return envelope.Data
	}
	const app = "app-123e4567e89b42d3a456426614174461"
	const passphrase = "synthetic-upgrade-passphrase"
	const request = "123e4567-e89b-42d3-a456-426614174462"
	const value = "synthetic-upgrade-secret"
	legacy(passphrase, "vault", "init", "--backend", "passphrase", "--passphrase-stdin")
	var before struct {
		Revision string `json:"revision"`
	}
	if json.Unmarshal(legacy("", "values", "revision", "--profile", app), &before) != nil || before.Revision == "" {
		t.Fatal("previous revision unavailable")
	}
	legacy(value, "secret", "set", "TOKEN", "--stdin", "--create-only", "--profile", app, "--revision", before.Revision, "--request-id", request)
	legacy("", "vault", "lock")
	p, err := New(ctx, binary, root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_, _ = p.Protection(cleanup, "lock", nil)
	})
	if _, err := p.Protection(ctx, "unlock", []byte(passphrase)); err != nil {
		t.Fatal("unlock previous state:", err)
	}
	metadata, err := p.Metadata(ctx, app, "common", "secret")
	if err != nil || len(metadata.Items) != 1 || metadata.Items[0].Name != "TOKEN" {
		t.Fatal("previous metadata unavailable:", err)
	}
	replayed, err := p.SetSecret(ctx, app, "common", "TOKEN", []byte(value), before.Revision, request, false)
	if err != nil || !replayed.Replayed {
		t.Fatal("previous request was not replayed:", err)
	}
	saved, err := p.Request(ctx, app, request)
	if err != nil || saved.State != "completed" || saved.Result == nil {
		t.Fatal("previous receipt unavailable:", err)
	}
	// Prove recovery through a logical archive without requiring an in-place
	// binary downgrade of protected state.
	identity, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	private := t.TempDir()
	archive, keyfile := filepath.Join(private, "previous.age"), filepath.Join(private, "identity")
	if err := os.WriteFile(keyfile, []byte(identity.String()+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := p.ExportApplication(ctx, app, identity.Recipient().String(), archive); err != nil {
		t.Fatal("export upgraded state:", err)
	}
	target, err := New(ctx, binary, filepath.Join(t.TempDir(), "restored"))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
		defer stop()
		_, _ = target.Protection(cleanup, "lock", nil)
	})
	if _, err := target.Protection(ctx, "init", []byte(passphrase)); err != nil {
		t.Fatal(err)
	}
	preview, err := target.PreviewApplicationImport(ctx, app, archive, keyfile)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := target.ImportApplication(ctx, app, archive, keyfile, preview.Digest, "123e4567-e89b-42d3-a456-426614174463"); err != nil {
		t.Fatal("restore upgraded state:", err)
	}
	restored, err := target.Metadata(ctx, app, "common", "secret")
	if err != nil || len(restored.Items) != 1 || restored.Items[0].Name != "TOKEN" {
		t.Fatal("restored metadata unavailable:", err)
	}
}
