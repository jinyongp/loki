package toolchain

import (
	"context"
	"crypto/sha256"
	"fmt"
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
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "packaging", "native", "toolchain-catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := LoadCatalog(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Go) != 2 || catalog.Go[0].Version != "1.27.1" || catalog.Go[1].Version != "1.27.4" ||
		catalog.Go[1].SHA256 != "7226736c2339719cda28fbf3a54d71aa63bcefe376e98cef00034268dab24840" {
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

func TestProvisionCatalogInstallsEveryGoRelease(t *testing.T) {
	store := generationStoreFixture(t)
	bundle := t.TempDir()
	artifacts := filepath.Join(bundle, "artifacts")
	if err := os.Mkdir(artifacts, 0755); err != nil {
		t.Fatal(err)
	}
	catalog := Catalog{Version: CatalogVersion}
	for _, version := range []string{"1.27.1", "1.27.4"} {
		source := filepath.Join(artifacts, "go"+version+".linux-amd64.tar.gz")
		writeTarGzip(t, source, []tarEntry{
			{name: "go/bin/go", data: []byte("#!/bin/sh\n"), mode: 0755},
			{name: "go/bin/gofmt", data: []byte("#!/bin/sh\n"), mode: 0755},
			{name: "go/VERSION", data: []byte("go" + version + "\n"), mode: 0644},
		})
		payload, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		catalog.Go = append(catalog.Go, goRelease(version, fmt.Sprintf("%x", sha256.Sum256(payload))))
	}
	if err := ProvisionCatalog(t.Context(), catalog, bundle, store); err != nil {
		t.Fatal(err)
	}
	for _, release := range catalog.Go {
		if _, err := store.Lookup(release.GenerationID()); err != nil {
			t.Fatalf("missing Go %s generation: %v", release.Version, err)
		}
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
