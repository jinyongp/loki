package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loki/internal/work/toolchains"
)

func provisionAcceptanceGo(t *testing.T, store toolchain.GenerationStore, release toolchain.GoRelease) toolchain.Generation {
	t.Helper()
	generation, err := store.Provision(t.Context(), release.GenerationID(), func(_ context.Context, root string) error {
		base := filepath.Join(root, "opt", "loki", "toolchain", "go", release.Version, "bin")
		if err := os.MkdirAll(base, 0755); err != nil {
			return err
		}
		for _, name := range []string{"go", "gofmt"} {
			body := "#!/bin/sh\nprintf '" + name + "-" + release.Version + "\\n'\n"
			if err := os.WriteFile(filepath.Join(base, name), []byte(body), 0755); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	return generation
}

func TestManagedGoAcceptanceSelectsMultiplePermittedGenerationsThroughSameShims(t *testing.T) {
	store := acceptanceStore(t)
	old := toolchain.GoRelease{
		Version: "1.26.3",
		URL:     "https://go.dev/dl/go1.26.3.linux-amd64.tar.gz",
		SHA256:  strings.Repeat("a", 64),
	}
	current := toolchain.GoRelease{
		Version: "1.27.1",
		URL:     "https://go.dev/dl/go1.27.1.linux-amd64.tar.gz",
		SHA256:  strings.Repeat("b", 64),
	}
	generations := map[string]toolchain.Generation{}
	oldGeneration := provisionAcceptanceGo(t, store, old)
	generations[oldGeneration.ID] = oldGeneration

	workspace := t.TempDir()
	legacy := filepath.Join(workspace, "legacy")
	currentProject := filepath.Join(workspace, "current")
	for _, path := range []string{legacy, currentProject} {
		if err := os.Mkdir(path, 0755); err != nil {
			t.Fatal(err)
		}
	}
	if err := os.WriteFile(filepath.Join(legacy, "go.mod"), []byte(
		"module example.com/legacy\n\ngo 1.26.0\ntoolchain go1.26.3\n",
	), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(currentProject, "go.mod"), []byte(
		"module example.com/current\n\ngo 1.27.1\n",
	), 0644); err != nil {
		t.Fatal(err)
	}

	resolver := toolchain.ProjectResolver{
		Root: workspace, Store: store,
		Catalog: toolchain.Catalog{
			Version: toolchain.CatalogVersion,
			Go:      []toolchain.GoRelease{old, current},
		},
	}
	legacySelection, err := resolver.Resolve("legacy")
	if err != nil {
		t.Fatal(err)
	}
	if len(legacySelection) != 1 || legacySelection[0].Version != old.Version {
		t.Fatalf("legacy Go selection = %#v", legacySelection)
	}
	legacyMount := filepath.Join(t.TempDir(), "managed")
	mountAcceptanceSelections(t, legacyMount, legacySelection, generations)
	if got := runResolvedShim(t, legacyMount, "go"); got != "go-1.26.3" {
		t.Fatalf("legacy Go shim = %q", got)
	}

	currentGeneration := provisionAcceptanceGo(t, store, current)
	generations[currentGeneration.ID] = currentGeneration
	currentSelection, err := resolver.Resolve("current")
	if err != nil {
		t.Fatal(err)
	}
	if len(currentSelection) != 1 || currentSelection[0].Version != current.Version {
		t.Fatalf("current Go selection = %#v", currentSelection)
	}
	currentMount := filepath.Join(t.TempDir(), "managed")
	mountAcceptanceSelections(t, currentMount, currentSelection, generations)
	if got := runResolvedShim(t, currentMount, "go"); got != "go-1.27.1" {
		t.Fatalf("current Go shim = %q", got)
	}
	if got := runResolvedShim(t, currentMount, "gofmt"); got != "gofmt-1.27.1" {
		t.Fatalf("current gofmt shim = %q", got)
	}
}
