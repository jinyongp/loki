package toolchain

import (
	"context"
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func uvRelease(version, checksum string) UVRelease {
	return UVRelease{
		Version: version,
		URL:     "https://github.com/astral-sh/uv/releases/download/" + version + "/uv-x86_64-unknown-linux-gnu.tar.gz",
		SHA256:  checksum,
	}
}

func writeUVArchive(t *testing.T, version string) (string, string) {
	t.Helper()
	root := t.TempDir()
	tree := filepath.Join(root, "uv-x86_64-unknown-linux-gnu")
	if err := os.Mkdir(tree, 0755); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"uv", "uvx"} {
		if err := os.WriteFile(filepath.Join(tree, name), []byte("#!/bin/sh\nprintf '"+name+" "+version+"\n'\n"), 0755); err != nil {
			t.Fatal(err)
		}
	}
	archive := filepath.Join(t.TempDir(), "uv-x86_64-unknown-linux-gnu.tar.gz")
	command := exec.Command("tar", "-czf", archive, "-C", root, filepath.Base(tree))
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create uv archive: %v: %s", err, output)
	}
	payload, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	return archive, fmt.Sprintf("%x", sha256.Sum256(payload))
}

func TestUVProviderUsesInstalledMatchUntilExplicitUpdate(t *testing.T) {
	store := generationStoreFixture(t)
	old := uvRelease("0.12.16", strings.Repeat("a", 64))
	current := uvRelease("0.12.17", strings.Repeat("b", 64))
	if _, err := store.Provision(t.Context(), old.GenerationID(), func(context.Context, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
	provider := UVProvider{Store: store}
	ordinary, err := provider.Resolve("0.12", []UVRelease{old, current}, false)
	if err != nil {
		t.Fatal(err)
	}
	if ordinary.Resolution.Version != old.Version || !ordinary.Resolution.Installed || ordinary.Resolution.Acquire {
		t.Fatalf("ordinary uv resolution = %#v", ordinary)
	}
	update, err := provider.Resolve("0.12", []UVRelease{old, current}, true)
	if err != nil {
		t.Fatal(err)
	}
	if update.Resolution.Version != current.Version || update.Resolution.Installed || !update.Resolution.Acquire {
		t.Fatalf("uv update resolution = %#v", update)
	}
}

func TestUVProviderProvisionStandaloneBinary(t *testing.T) {
	const version = "0.12.17"
	source, checksum := writeUVArchive(t, version)
	release := uvRelease(version, checksum)
	provider := UVProvider{Store: generationStoreFixture(t)}
	plan, err := provider.Resolve("0.12", []UVRelease{release}, false)
	if err != nil {
		t.Fatal(err)
	}
	generation, err := provider.Provision(t.Context(), plan, source)
	if err != nil {
		t.Fatal(err)
	}
	base := filepath.Join(generation.Root, "opt", "loki", "toolchain", "uv", version)
	for _, name := range []string{"uv", "uvx"} {
		info, err := os.Stat(filepath.Join(base, name))
		if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0555 {
			t.Fatalf("%s = %v, %v", name, info, err)
		}
	}
}

func TestUVReleaseRejectsUntrustedIdentityAndTamperedArtifact(t *testing.T) {
	valid := uvRelease("0.12.17", strings.Repeat("c", 64))
	for _, release := range []UVRelease{
		{Version: "v0.12.17", URL: valid.URL, SHA256: valid.SHA256},
		{Version: valid.Version, URL: "https://example.test/uv-x86_64-unknown-linux-gnu.tar.gz", SHA256: valid.SHA256},
		{Version: valid.Version, URL: valid.URL + "?mirror=1", SHA256: valid.SHA256},
	} {
		if err := release.Validate(); err == nil {
			t.Fatalf("invalid uv release accepted: %#v", release)
		}
	}

	source, checksum := writeUVArchive(t, "0.12.17")
	release := uvRelease("0.12.17", checksum)
	provider := UVProvider{Store: generationStoreFixture(t)}
	plan, err := provider.Resolve("0.12.17", []UVRelease{release}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(source, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = provider.Provision(t.Context(), plan, source); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("tampered uv artifact error = %v", err)
	}
}
