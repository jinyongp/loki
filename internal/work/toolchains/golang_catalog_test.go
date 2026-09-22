package toolchain

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func provisionGoMetadataGeneration(t *testing.T, store GenerationStore, release GoRelease) {
	t.Helper()
	if _, err := store.Provision(t.Context(), release.GenerationID(), func(context.Context, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestRepositoryManagedGoCatalog(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "packaging", "go", "toolchain-catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := LoadCatalog(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Go) != 1 || catalog.Go[0].Version != "1.27.1" ||
		catalog.Go[0].SHA256 != "63d339f0da5ab53635a56f2490a7984dfe12dfcff22ad749f63edaf590168445" {
		t.Fatalf("Go catalog = %#v", catalog.Go)
	}
}

func TestCatalogSupportsGoAndRejectsOrderingErrors(t *testing.T) {
	old := goRelease("1.26.3", strings.Repeat("d", 64))
	current := goRelease("1.27.1", strings.Repeat("e", 64))
	if err := (Catalog{Version: CatalogVersion, Go: []GoRelease{old, current}}).Validate(); err != nil {
		t.Fatal(err)
	}
	for _, releases := range [][]GoRelease{{current, old}, {current, current}} {
		if err := (Catalog{Version: CatalogVersion, Go: releases}).Validate(); err == nil {
			t.Fatalf("invalid Go catalog ordering accepted: %#v", releases)
		}
	}
}

func TestProvisionCatalogReusesInstalledGoGeneration(t *testing.T) {
	store := generationStoreFixture(t)
	release := goRelease("1.27.1", strings.Repeat("f", 64))
	provisionGoMetadataGeneration(t, store, release)
	bundle := t.TempDir()
	if err := os.Mkdir(filepath.Join(bundle, "artifacts"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := ProvisionCatalog(t.Context(), Catalog{
		Version: CatalogVersion,
		Go:      []GoRelease{release},
	}, bundle, store); err != nil {
		t.Fatalf("reuse installed Go generation: %v", err)
	}
}

func TestCatalogBundleArtifactsIncludesGoRelease(t *testing.T) {
	release := goRelease("1.27.1", strings.Repeat("1", 64))
	artifacts := catalogBundleArtifacts(Catalog{Version: CatalogVersion, Go: []GoRelease{release}})
	if len(artifacts) != 1 {
		t.Fatalf("Go bundle artifacts = %#v", artifacts)
	}
	got := artifacts[0]
	if got.Filename != release.Filename() || got.URL != release.URL || got.SHA256 != release.SHA256 {
		t.Fatalf("Go bundle artifact = %#v", got)
	}
}
