package devtools

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
	"time"

	"filippo.io/age"
)

func TestRealApplicationOnlyEncryptedProfileTransfer(t *testing.T) {
	binary := os.Getenv("LOKI_SECRET_TEST_DEVTOOLS")
	if binary == "" {
		if os.Getenv("LOKI_REQUIRE_SECRET_PROVIDER") == "1" {
			t.Fatal("pinned provider required")
		}
		t.Skip("native values suite supplies the pinned provider")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 60*time.Second)
	defer cancel()
	newProvider := func() *Provider {
		t.Helper()
		root := t.TempDir()
		if err := os.Chmod(root, 0700); err != nil {
			t.Fatal(err)
		}
		p, err := New(ctx, binary, filepath.Join(root, "dvt"))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := p.Protection(ctx, "init", []byte("synthetic-profile-transfer-passphrase")); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() {
			cleanup, stop := context.WithTimeout(context.Background(), 3*time.Second)
			defer stop()
			_, _ = p.Protection(cleanup, "lock", nil)
		})
		return p
	}
	source, target := newProvider(), newProvider()
	const app = "app-123e4567e89b42d3a456426614174441"
	revision := func(p *Provider, env string) string {
		t.Helper()
		r, err := p.Revision(ctx, app, env)
		if err != nil {
			t.Fatal(err)
		}
		return r
	}
	if _, err := source.SetSecret(ctx, app, "common", "TOKEN", []byte("private-logical-transfer\nsecond line"), revision(source, "common"), "123e4567-e89b-42d3-a456-426614174442", false); err != nil {
		t.Fatal(err)
	}
	if _, err := source.SetPublic(ctx, app, "common", "API_URL", "https://public.example", revision(source, "common"), "123e4567-e89b-42d3-a456-426614174443"); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Environment(ctx, app, "production", "create", revision(source, "common"), "123e4567-e89b-42d3-a456-426614174444"); err != nil {
		t.Fatal(err)
	}
	if _, err := source.SetSecret(ctx, app, "production", "EMPTY", []byte{}, revision(source, "production"), "123e4567-e89b-42d3-a456-426614174445", false); err != nil {
		t.Fatal(err)
	}
	if _, err := source.Bundle(ctx, app, "execution", revision(source, "common"), "123e4567-e89b-42d3-a456-426614174446", []string{"TOKEN", "API_URL", "EMPTY"}); err != nil {
		t.Fatal(err)
	}
	id, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	private := t.TempDir()
	if err := os.Chmod(private, 0700); err != nil {
		t.Fatal(err)
	}
	archive, identity := filepath.Join(private, "app.age"), filepath.Join(private, "identity.txt")
	if err := os.WriteFile(identity, []byte(id.String()+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := source.ExportApplication(ctx, app, id.Recipient().String(), archive); err != nil {
		t.Fatal("export", err)
	}
	body, err := os.ReadFile(archive)
	if err != nil || bytes.Contains(body, []byte("private-logical-transfer")) || bytes.Contains(body, []byte("public.example")) || bytes.Contains(body, []byte(id.String())) {
		t.Fatal("logical export exposed plaintext/identity", err)
	}
	preview, err := target.PreviewApplicationImport(ctx, app, archive, identity)
	if err != nil || preview.TargetExists || preview.Profile != app || preview.SourceProfile != app {
		t.Fatal("preview", err)
	}
	const request = "123e4567-e89b-42d3-a456-426614174447"
	first, err := target.ImportApplication(ctx, app, archive, identity, preview.Digest, request)
	if err != nil || !first.Changed || first.Replayed {
		t.Fatal("apply", err)
	}
	replay, err := target.ImportApplication(ctx, app, archive, identity, preview.Digest, request)
	if err != nil || !replay.Replayed || replay.Digest != first.Digest {
		t.Fatal("lost apply ACK replay", err)
	}
	if _, err := target.ImportApplication(ctx, app, archive, identity, preview.Digest, "123e4567-e89b-42d3-a456-426614174448"); err == nil {
		t.Fatal("different request replaced an existing application profile")
	}
	for _, tc := range []struct {
		env, kind string
		count     int
	}{{"common", "secret", 1}, {"common", "variable", 1}, {"production", "secret", 2}} {
		metadata, err := target.Metadata(ctx, app, tc.env, tc.kind)
		sourceMetadata, sourceErr := source.Metadata(ctx, app, tc.env, tc.kind)
		if err != nil || sourceErr != nil || len(metadata.Items) != tc.count || len(sourceMetadata.Items) != tc.count || !reflect.DeepEqual(metadata.Items, sourceMetadata.Items) {
			t.Fatal("logical restore lost kind/environment/fallback metadata", tc, "target count", len(metadata.Items), "source count", len(sourceMetadata.Items), err, sourceErr)
		}
	}
	if err := source.ExportApplication(ctx, "loki", id.Recipient().String(), filepath.Join(private, "forbidden.age")); err == nil {
		t.Fatal("exported a coordination profile")
	}
	// Seed a task only in this synthetic root to prove that a mixed archive
	// cannot enter the application restore path, even under an app-shaped name.
	var taskResult json.RawMessage
	if err := source.invoke(ctx, []string{"task", "add", "--profile", app, "--request-id", "123e4567-e89b-42d3-a456-426614174449", "--stdin"}, []byte(`{"title":"synthetic coordination state must not restore"}`), &taskResult); err != nil {
		t.Fatal("mixed-archive fixture", err)
	}
	clear(taskResult)
	mixed := filepath.Join(private, "mixed.age")
	if err := source.ExportApplication(ctx, app, id.Recipient().String(), mixed); err == nil {
		t.Fatal("accepted a logical export containing task state")
	}
	const freshApp = "app-123e4567e89b42d3a456426614174450"
	if _, err := target.PreviewApplicationImport(ctx, freshApp, mixed, identity); err == nil {
		t.Fatal("accepted a mixed task/value archive preview")
	}
	if _, err := target.ImportApplication(ctx, freshApp, mixed, identity, preview.Digest, "123e4567-e89b-42d3-a456-426614174451"); err == nil {
		t.Fatal("restored task state through the application adapter")
	}
}
