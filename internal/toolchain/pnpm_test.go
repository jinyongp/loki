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

func pnpmRelease(version, checksum string) PnpmRelease {
	return PnpmRelease{
		Version: version,
		URL:     "https://github.com/pnpm/pnpm/releases/download/v" + version + "/pnpm-linux-x64.tar.gz",
		SHA256:  checksum,
	}
}

func writePnpmArchive(t *testing.T) (string, string) {
	t.Helper()
	root := t.TempDir()
	path := filepath.Join(root, "pnpm")
	if err := os.WriteFile(path, []byte("#!/bin/sh\nprintf 'pnpm fixture\n'\n"), 0755); err != nil {
		t.Fatal(err)
	}
	archive := filepath.Join(t.TempDir(), "pnpm-linux-x64.tar.gz")
	command := exec.Command("tar", "-czf", archive, "-C", root, "pnpm")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("create pnpm archive: %v: %s", err, output)
	}
	payload, err := os.ReadFile(archive)
	if err != nil {
		t.Fatal(err)
	}
	return archive, fmt.Sprintf("%x", sha256.Sum256(payload))
}

func TestParsePnpmPackageManagerAndVersionScheme(t *testing.T) {
	for raw, want := range map[string]Selector{
		"pnpm@12.5.1":                {Kind: SelectorExact, Value: "12.5.1"},
		"pnpm@12.5.1+sha512.fixture": {Kind: SelectorExact, Value: "12.5.1"},
		"pnpm@12.5":                  {Kind: SelectorPartial, Value: "12.5"},
		"pnpm@12":                    {Kind: SelectorPartial, Value: "12"},
		"pnpm@latest":                {Kind: SelectorFloating, Value: "*"},
	} {
		got, err := ParsePnpmPackageManager(raw)
		if err != nil || got != want {
			t.Fatalf("ParsePnpmPackageManager(%q) = %#v, %v; want %#v", raw, got, err, want)
		}
	}
	for _, raw := range []string{"npm@12.5.1", "pnpm@", "pnpm@012", "pnpm@12.5.1-beta.1"} {
		if _, err := ParsePnpmPackageManager(raw); err == nil {
			t.Fatalf("invalid packageManager %q accepted", raw)
		}
	}
}

func TestPnpmProviderUsesInstalledMatchUntilExplicitUpdate(t *testing.T) {
	store := generationStoreFixture(t)
	provider := PnpmProvider{Store: store}
	old := pnpmRelease("12.4.2", strings.Repeat("d", 64))
	current := pnpmRelease("12.5.1", strings.Repeat("e", 64))
	if _, err := store.Provision(t.Context(), old.GenerationID(), func(context.Context, string) error { return nil }); err != nil {
		t.Fatal(err)
	}

	ordinary, err := provider.Resolve("pnpm@12", []PnpmRelease{old, current}, false)
	if err != nil {
		t.Fatal(err)
	}
	if ordinary.Resolution.Version != old.Version || !ordinary.Resolution.Installed || ordinary.Resolution.Acquire {
		t.Fatalf("ordinary pnpm resolution = %#v", ordinary)
	}
	update, err := provider.Resolve("pnpm@12", []PnpmRelease{old, current}, true)
	if err != nil {
		t.Fatal(err)
	}
	if update.Resolution.Version != current.Version || update.Resolution.Installed || !update.Resolution.Acquire {
		t.Fatalf("pnpm update resolution = %#v", update)
	}
}

func TestPnpmProviderProvisionNativeExecutableWithoutNodeDependency(t *testing.T) {
	const version = "12.5.1"
	source, checksum := writePnpmArchive(t)
	release := pnpmRelease(version, checksum)
	store := generationStoreFixture(t)
	provider := PnpmProvider{Store: store}

	plan, err := provider.Resolve("pnpm@12.5.1", []PnpmRelease{release}, false)
	if err != nil {
		t.Fatal(err)
	}
	generation, err := provider.Provision(t.Context(), plan, source)
	if err != nil {
		t.Fatal(err)
	}
	executable, err := plan.Executable(generation)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(executable)
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != 0555 {
		t.Fatalf("pnpm executable = %q, %v, %v", executable, info, err)
	}
	if strings.Contains(strings.ToLower(executable), "corepack") {
		t.Fatalf("pnpm executable unexpectedly uses Corepack: %s", executable)
	}
}

func TestPnpmReleaseRejectsUntrustedIdentityAndTamperedArtifact(t *testing.T) {
	valid := pnpmRelease("12.5.1", strings.Repeat("f", 64))
	for _, release := range []PnpmRelease{
		{Version: "v12.5.1", URL: valid.URL, SHA256: valid.SHA256},
		{Version: valid.Version, URL: "https://example.test/pnpm-linux-x64.tar.gz", SHA256: valid.SHA256},
		{Version: valid.Version, URL: valid.URL + "?mirror=1", SHA256: valid.SHA256},
	} {
		if err := release.Validate(); err == nil {
			t.Fatalf("untrusted pnpm release accepted: %#v", release)
		}
	}

	source, checksum := writePnpmArchive(t)
	release := pnpmRelease("12.5.1", checksum)
	provider := PnpmProvider{Store: generationStoreFixture(t)}
	plan, err := provider.Resolve("pnpm@12.5.1", []PnpmRelease{release}, false)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(source, []byte("tampered"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err = provider.Provision(t.Context(), plan, source); err == nil || !strings.Contains(err.Error(), "checksum mismatch") {
		t.Fatalf("tampered pnpm artifact error = %v", err)
	}
}
