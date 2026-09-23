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

func TestInspectReadsValidatedBootstrapReleaseBinding(t *testing.T) {
	digest := strings.Repeat("a", 64)
	hostDigest := strings.Repeat("b", 64)
	path := writeInspectorFixture(t, "printf '%s\\n' '{\"release_tag\":\"v1.2.3\",\"release_manifest_sha256\":\""+digest+"\",\"host_binary_sha256\":\""+hostDigest+"\"}'")
	info, err := Inspect(t.Context(), path)
	if err != nil {
		t.Fatal(err)
	}
	if info.ReleaseTag != "v1.2.3" || info.ReleaseManifestSHA256 != digest || info.HostBinarySHA256 != hostDigest {
		t.Fatalf("bootstrap info = %#v", info)
	}
}

func TestInspectRejectsInvalidOutputAndSymlink(t *testing.T) {
	good := strings.Repeat("a", 64)
	for name, body := range map[string]string{
		"unknown-field": "printf '%s\\n' '{\"release_tag\":\"v1.2.3\",\"release_manifest_sha256\":\"" + good + "\",\"host_binary_sha256\":\"" + good + "\",\"extra\":true}'",
		"bad-tag":       "printf '%s\\n' '{\"release_tag\":\"1.2.3\",\"release_manifest_sha256\":\"" + good + "\",\"host_binary_sha256\":\"" + good + "\"}'",
		"bad-digest":    "printf '%s\\n' '{\"release_tag\":\"v1.2.3\",\"release_manifest_sha256\":\"bad\",\"host_binary_sha256\":\"" + good + "\"}'",
		"trailing-json": "printf '%s\\n' '{\"release_tag\":\"v1.2.3\",\"release_manifest_sha256\":\"" + good + "\",\"host_binary_sha256\":\"" + good + "\"} 1'",
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
