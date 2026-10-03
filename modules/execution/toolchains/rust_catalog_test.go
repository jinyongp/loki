package toolchain

import (
	"os"
	"path/filepath"
	"testing"
)

func TestCatalogSupportsRustReleaseAndRejectsOrderingErrors(t *testing.T) {
	old := rustMetadataRelease(
		"stable", "1.97.1", "2026-08-06",
		[]string{"cargo", "rustc"}, []string{rustDefaultHost},
	)
	current := rustMetadataRelease(
		"stable", "1.98.1", "2026-09-03",
		[]string{"cargo", "rustc"}, []string{rustDefaultHost},
	)
	if err := (Catalog{
		Version: CatalogVersion,
		Rust:    []RustRelease{old, current},
	}).Validate(); err != nil {
		t.Fatal(err)
	}
	for _, releases := range [][]RustRelease{
		{current, old},
		{current, current},
	} {
		if err := (Catalog{Version: CatalogVersion, Rust: releases}).Validate(); err == nil {
			t.Fatalf("invalid Rust catalog ordering accepted: %#v", releases)
		}
	}
}

func TestProvisionCatalogReusesInstalledRustGeneration(t *testing.T) {
	release := rustMetadataRelease(
		"stable", "1.98.1", "2026-09-03",
		[]string{"cargo", "rustc"}, []string{rustDefaultHost},
	)
	store := generationStoreFixture(t)
	provisionRustMetadataGeneration(t, store, release)
	bundle := t.TempDir()
	if err := os.Mkdir(filepath.Join(bundle, "artifacts"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := ProvisionCatalog(t.Context(), Catalog{
		Version: CatalogVersion,
		Rust:    []RustRelease{release},
	}, bundle, store); err != nil {
		t.Fatalf("reuse installed Rust generation: %v", err)
	}
}

func TestCatalogBundleArtifactsIncludesRustComponents(t *testing.T) {
	release := rustMetadataRelease(
		"stable", "1.98.1", "2026-09-03",
		[]string{"cargo", "rust-src", "rustc"}, []string{rustDefaultHost, "wasm32-unknown-unknown"},
	)
	artifacts := catalogBundleArtifacts(Catalog{
		Version: CatalogVersion,
		Rust:    []RustRelease{release},
	})
	if len(artifacts) != len(release.Artifacts) {
		t.Fatalf("Rust bundle artifacts = %d, want %d", len(artifacts), len(release.Artifacts))
	}
	byFilename := make(map[string]bundleArtifact, len(artifacts))
	for _, artifact := range artifacts {
		byFilename[artifact.Filename] = artifact
	}
	for _, want := range release.Artifacts {
		got, ok := byFilename[want.Filename]
		if !ok || got.URL != want.URL || got.SHA256 != want.SHA256 {
			t.Fatalf("Rust bundle artifact %s = %#v", want.Filename, got)
		}
	}
}
