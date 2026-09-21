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

func pnpmRelease(version, checksum string, nodeMin, nodeMax uint64) PnpmRelease {
	return PnpmRelease{
		Version:      version,
		URL:          "https://github.com/pnpm/pnpm/releases/download/v" + version + "/pnpm-linux-x64.tar.gz",
		SHA256:       checksum,
		NodeMajorMin: nodeMin,
		NodeMajorMax: nodeMax,
	}
}

func selectedNodePlan(version string) NodePlan {
	release := nodeRelease(version, strings.Repeat("a", 64))
	return NodePlan{
		Resolution: VersionResolution{
			Selector: Selector{Kind: SelectorExact, Value: version},
			Version:  version,
		},
		Release:      release,
		GenerationID: release.GenerationID(),
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
		"pnpm@12.5.1": {Kind: SelectorExact, Value: "12.5.1"},
		"pnpm@12.5":   {Kind: SelectorPartial, Value: "12.5"},
		"pnpm@12":     {Kind: SelectorPartial, Value: "12"},
		"pnpm@latest": {Kind: SelectorFloating, Value: "*"},
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

func TestPnpmProviderFiltersBySelectedNodeCompatibility(t *testing.T) {
	store := generationStoreFixture(t)
	provider := PnpmProvider{Store: store}
	node := selectedNodePlan("26.9.0")
	incompatible := pnpmRelease("11.27.0", strings.Repeat("b", 64), 20, 24)
	current := pnpmRelease("12.5.1", strings.Repeat("c", 64), 22, 26)

	plan, err := provider.Resolve("pnpm@12", []PnpmRelease{incompatible, current}, node, false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.Release.Version != "12.5.1" || plan.NodeVersion != "26.9.0" ||
		plan.NodeGenerationID != node.GenerationID || !plan.Resolution.Acquire {
		t.Fatalf("pnpm compatibility plan = %#v", plan)
	}

	if _, err = provider.Resolve("pnpm@11", []PnpmRelease{incompatible}, node, false); err == nil ||
		!strings.Contains(err.Error(), "compatible") {
		t.Fatalf("incompatible pnpm release error = %v", err)
	}
}

func TestPnpmProviderUsesInstalledMatchUntilExplicitUpdate(t *testing.T) {
	store := generationStoreFixture(t)
	provider := PnpmProvider{Store: store}
	node := selectedNodePlan("26.9.0")
	old := pnpmRelease("12.4.2", strings.Repeat("d", 64), 22, 26)
	current := pnpmRelease("12.5.1", strings.Repeat("e", 64), 22, 26)
	if _, err := store.Provision(t.Context(), old.GenerationID(), func(context.Context, string) error { return nil }); err != nil {
		t.Fatal(err)
	}

	ordinary, err := provider.Resolve("pnpm@12", []PnpmRelease{old, current}, node, false)
	if err != nil {
		t.Fatal(err)
	}
	if ordinary.Resolution.Version != old.Version || !ordinary.Resolution.Installed || ordinary.Resolution.Acquire {
		t.Fatalf("ordinary pnpm resolution = %#v", ordinary)
	}
	update, err := provider.Resolve("pnpm@12", []PnpmRelease{old, current}, node, true)
	if err != nil {
		t.Fatal(err)
	}
	if update.Resolution.Version != current.Version || update.Resolution.Installed || !update.Resolution.Acquire {
		t.Fatalf("pnpm update resolution = %#v", update)
	}
}

func TestPnpmProviderProvisionNativeExecutable(t *testing.T) {
	const version = "12.5.1"
	source, checksum := writePnpmArchive(t)
	release := pnpmRelease(version, checksum, 22, 26)
	store := generationStoreFixture(t)
	provider := PnpmProvider{Store: store}
	node := selectedNodePlan("26.9.0")

	plan, err := provider.Resolve("pnpm@12.5.1", []PnpmRelease{release}, node, false)
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
	valid := pnpmRelease("12.5.1", strings.Repeat("f", 64), 22, 26)
	for _, release := range []PnpmRelease{
		{Version: "v12.5.1", URL: valid.URL, SHA256: valid.SHA256, NodeMajorMin: 22, NodeMajorMax: 26},
		{Version: valid.Version, URL: "https://example.test/pnpm-linux-x64.tar.gz", SHA256: valid.SHA256, NodeMajorMin: 22, NodeMajorMax: 26},
		{Version: valid.Version, URL: valid.URL + "?mirror=1", SHA256: valid.SHA256, NodeMajorMin: 22, NodeMajorMax: 26},
		{Version: valid.Version, URL: valid.URL, SHA256: valid.SHA256, NodeMajorMin: 27, NodeMajorMax: 26},
	} {
		if err := release.Validate(); err == nil {
			t.Fatalf("untrusted pnpm release accepted: %#v", release)
		}
	}

	source, checksum := writePnpmArchive(t)
	release := pnpmRelease("12.5.1", checksum, 22, 26)
	provider := PnpmProvider{Store: generationStoreFixture(t)}
	plan, err := provider.Resolve("pnpm@12.5.1", []PnpmRelease{release}, selectedNodePlan("26.9.0"), false)
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
