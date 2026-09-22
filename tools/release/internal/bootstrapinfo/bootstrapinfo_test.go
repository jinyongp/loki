package bootstrapinfo

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func writeInspectorFixture(t *testing.T, body string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "bootstrap")
	if err := os.WriteFile(path, []byte("#!/bin/sh\n"+body+"\n"), 0755); err != nil {
		t.Fatal(err)
	}
	return path
}

func TestInspectReadsValidatedBootstrapTrust(t *testing.T) {
	digest := strings.Repeat("a", 64)
	path := writeInspectorFixture(t, "printf '%s\\n' '{\"metadata_url\":\"https://jinyongp.dev/loki/tuf/\",\"trusted_root_sha256\":\""+digest+"\"}'")
	info, err := Inspect(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if info.MetadataURL != "https://jinyongp.dev/loki/tuf/" || info.TrustedRootSHA256 != digest {
		t.Fatalf("bootstrap info = %#v", info)
	}
}

func TestInspectRejectsInvalidOutputAndSymlink(t *testing.T) {
	for name, body := range map[string]string{
		"unknown-field": "printf '%s\\n' '{\"metadata_url\":\"https://example.test/tuf/\",\"trusted_root_sha256\":\"" + strings.Repeat("a", 64) + "\",\"extra\":true}'",
		"http":          "printf '%s\\n' '{\"metadata_url\":\"http://example.test/tuf/\",\"trusted_root_sha256\":\"" + strings.Repeat("a", 64) + "\"}'",
		"bad-digest":    "printf '%s\\n' '{\"metadata_url\":\"https://example.test/tuf/\",\"trusted_root_sha256\":\"bad\"}'",
		"trailing-json": "printf '%s\\n' '{\"metadata_url\":\"https://example.test/tuf/\",\"trusted_root_sha256\":\"" + strings.Repeat("a", 64) + "\"} 1'",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := Inspect(t.Context(), writeInspectorFixture(t, body)); err == nil {
				t.Fatal("invalid bootstrap info was accepted")
			}
		})
	}

	target := writeInspectorFixture(t, "exit 0")
	link := filepath.Join(t.TempDir(), "bootstrap")
	if err := os.Symlink(target, link); err != nil {
		t.Fatal(err)
	}
	if _, err := Inspect(t.Context(), link); err == nil {
		t.Fatal("symlink bootstrap was accepted")
	}
}
