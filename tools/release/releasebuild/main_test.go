package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loki/internal/host/releases"
)

type fakeBuildRunner struct{}

func (fakeBuildRunner) BuildHost(_ context.Context, _, output, _, _, _ string) error {
	return os.WriteFile(output, []byte("host-binary"), 0755)
}

func (fakeBuildRunner) BuildBootstrap(_ context.Context, _, output, tag, manifest string) error {
	raw, err := os.ReadFile(manifest)
	if err != nil {
		return err
	}
	return os.WriteFile(output, []byte("bootstrap:"+tag+":"+digest(raw)), 0755)
}

func TestAssembleBuildsReleaseBoundArtifactSet(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	parent := t.TempDir()
	notes := filepath.Join(parent, "notes.md")
	if err = os.WriteFile(notes, []byte("# Loki 0.1.0\n\nInitial automated release.\n"), 0644); err != nil {
		t.Fatal(err)
	}
	output := filepath.Join(parent, "release")
	coreDigest := strings.Repeat("a", 64)
	browserDigest := strings.Repeat("b", 64)
	err = assemble(t.Context(), options{
		Output: output, Version: "0.1.0",
		SourceRevision: strings.Repeat("c", 40),
		ReleasedAt:     "2026-09-23T01:00:00Z",
		CoreImage:      "ghcr.io/jinyongp/loki@sha256:" + coreDigest,
		BrowserImage:   "ghcr.io/jinyongp/loki-browser@sha256:" + browserDigest,
		ReleaseNotes:   notes, SourceRoot: root,
	}, fakeBuildRunner{})
	if err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{
		"loki-linux-amd64", "loki-bootstrap-linux-amd64",
		"host-assets.tar.gz", "toolchain-catalog.json",
		"provenance.bundle.json", "notices.tar.gz", "release-notes.md",
		"effective-policy.json", "effective-config.toml",
		"release-manifest.json", "release-index.json",
	} {
		info, statErr := os.Stat(filepath.Join(output, name))
		if statErr != nil || !info.Mode().IsRegular() || info.Size() == 0 {
			t.Fatalf("release output %s invalid: info=%v err=%v", name, info, statErr)
		}
	}
	manifestRaw, err := os.ReadFile(filepath.Join(output, "release-manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	manifest, err := releases.LoadReleaseManifest(manifestRaw)
	if err != nil {
		t.Fatal(err)
	}
	if manifest.Generation.Spec.Version != "0.1.0" ||
		manifest.Generation.Spec.CoreImageDigest != "sha256:"+coreDigest ||
		len(manifest.Generation.Spec.Components) != 1 ||
		manifest.Generation.Spec.Components[0].Name != "browser" ||
		manifest.Generation.Spec.Components[0].Digest != "sha256:"+browserDigest {
		t.Fatalf("release manifest generation = %#v", manifest.Generation.Spec)
	}
	if manifest.Runtime.DockerMin != "28.0.0" || manifest.Runtime.ComposeMin != "2.39.0" {
		t.Fatalf("runtime requirements = %#v", manifest.Runtime)
	}
	indexRaw, err := os.ReadFile(filepath.Join(output, "release-index.json"))
	if err != nil {
		t.Fatal(err)
	}
	index, err := releases.LoadReleaseIndex(indexRaw)
	if err != nil {
		t.Fatal(err)
	}
	if len(index.Entries) != 1 || index.Entries[0].Release != "0.1.0" {
		t.Fatalf("release index = %#v", index)
	}
	if _, err = index.Entries[0].VerifyManifest(manifestRaw); err != nil {
		t.Fatal(err)
	}
}

func TestBuildHostAssetsIsDeterministic(t *testing.T) {
	first, err := buildHostAssets()
	if err != nil {
		t.Fatal(err)
	}
	second, err := buildHostAssets()
	if err != nil {
		t.Fatal(err)
	}
	if string(first) != string(second) {
		t.Fatal("host asset archive is not deterministic")
	}
}

func TestImageDigestRejectsMutableOrWrongRepository(t *testing.T) {
	for _, value := range []string{
		"ghcr.io/jinyongp/loki:latest",
		"ghcr.io/example/loki@sha256:" + strings.Repeat("a", 64),
		"https://ghcr.io/jinyongp/loki@sha256:" + strings.Repeat("a", 64),
	} {
		if _, err := imageDigest(value, "loki"); err == nil {
			t.Fatalf("image ref accepted: %s", value)
		}
	}
}
