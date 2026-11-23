package toolchain

import (
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func TestRepositoryManagedToolchainCatalog(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "packaging", "native", "toolchain-catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := LoadCatalog(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Node) != 2 || catalog.Node[0].Version != "26.9.0" || catalog.Node[1].Version != "26.13.1" {
		t.Fatalf("Node.js catalog = %#v", catalog.Node)
	}
	if len(catalog.Pnpm) != 2 || catalog.Pnpm[0].Version != "12.5.1" || catalog.Pnpm[1].Version != "12.21.0" {
		t.Fatalf("pnpm catalog = %#v", catalog.Pnpm)
	}
	if len(catalog.Python) != 2 || catalog.Python[0].Version != "3.14.7" || catalog.Python[1].Version != "3.14.10" ||
		catalog.Python[1].Build != "20261110" ||
		catalog.Python[1].SHA256 != "c2ff2e97422f5d3896a16e0317fd212ba977bbf2688d93a6f7a03d817506bd30" {
		t.Fatalf("Python catalog = %#v", catalog.Python)
	}
	if len(catalog.UV) != 2 || catalog.UV[0].Version != "0.12.17" || catalog.UV[1].Version != "0.13.9" ||
		catalog.UV[1].SHA256 != "a7fbaff2b9a271d85c1bdb540941090fd1ce011797b4abeb48e4dcd39c2471d5" {
		t.Fatalf("uv catalog = %#v", catalog.UV)
	}
	if len(catalog.Rust) != 2 || catalog.Rust[0].Version != "1.98.1" || catalog.Rust[1].Version != "1.100.0" {
		t.Fatalf("Rust catalog = %#v", catalog.Rust)
	}
}

func TestCatalogRejectsDuplicateOrUnsortedReleases(t *testing.T) {
	node := nodeRelease("26.9.0", strings.Repeat("a", 64))
	pnpm := pnpmRelease("12.5.1", strings.Repeat("b", 64))
	for _, catalog := range []Catalog{
		{Version: CatalogVersion, Node: []NodeRelease{node, node}},
		{Version: CatalogVersion, Node: []NodeRelease{
			node,
			nodeRelease("26.8.2", strings.Repeat("c", 64)),
		}},
		{Version: CatalogVersion, Node: []NodeRelease{node}, Pnpm: []PnpmRelease{pnpm, pnpm}},
	} {
		if err := catalog.Validate(); err == nil {
			t.Fatalf("invalid catalog accepted: %#v", catalog)
		}
	}
}

func TestProvisionCatalogReusesInstalledGenerationsWithoutBundleArtifacts(t *testing.T) {
	store := generationStoreFixture(t)
	node := nodeRelease("26.9.0", strings.Repeat("d", 64))
	pnpm := pnpmRelease("12.5.1", strings.Repeat("e", 64))
	provisionProjectNode(t, store, node)
	provisionProjectPnpm(t, store, pnpm)

	bundle := t.TempDir()
	if err := os.Mkdir(filepath.Join(bundle, "artifacts"), 0755); err != nil {
		t.Fatal(err)
	}
	catalog := Catalog{
		Version: CatalogVersion,
		Node:    []NodeRelease{node},
		Pnpm:    []PnpmRelease{pnpm},
	}
	if err := ProvisionCatalog(t.Context(), catalog, bundle, store); err != nil {
		t.Fatalf("reuse installed catalog generations: %v", err)
	}
}

func TestProvisionCatalogProtectsCurrentGenerationsAndCollectsOldOnQuota(t *testing.T) {
	store := generationStoreFixture(t)
	old := nodeRelease("22.23.4", strings.Repeat("a", 64))
	current := nodeRelease("26.9.0", strings.Repeat("b", 64))
	provisionProjectNode(t, store, old)
	provisionProjectNode(t, store, current)
	store.Limits = GenerationLimits{
		MaxBytes: 1 << 20, MaxGenerations: 1, Retention: 24 * time.Hour,
	}

	bundle := t.TempDir()
	if err := os.Mkdir(filepath.Join(bundle, "artifacts"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := ProvisionCatalog(t.Context(), Catalog{
		Version: CatalogVersion,
		Node:    []NodeRelease{current},
	}, bundle, store); err != nil {
		t.Fatal(err)
	}
	if _, err := store.Lookup(current.GenerationID()); err != nil {
		t.Fatalf("current catalog generation was reclaimed: %v", err)
	}
	if _, err := store.Lookup(old.GenerationID()); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("retired catalog generation survived quota collection: %v", err)
	}
}

func TestCatalogSupportsPythonAndUVWithoutNode(t *testing.T) {
	python := pythonRelease("3.14.7", "20260901", strings.Repeat("6", 64))
	uv := uvRelease("0.12.17", strings.Repeat("7", 64))
	catalog := Catalog{
		Version: CatalogVersion,
		Python:  []PythonRelease{python},
		UV:      []UVRelease{uv},
	}
	if err := catalog.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestProvisionCatalogReusesInstalledPythonAndUVGenerations(t *testing.T) {
	store := generationStoreFixture(t)
	python := pythonRelease("3.14.7", "20260901", strings.Repeat("8", 64))
	uv := uvRelease("0.12.17", strings.Repeat("9", 64))
	provisionProjectPython(t, store, python)
	provisionProjectUV(t, store, uv)

	bundle := t.TempDir()
	if err := os.Mkdir(filepath.Join(bundle, "artifacts"), 0755); err != nil {
		t.Fatal(err)
	}
	catalog := Catalog{
		Version: CatalogVersion,
		Python:  []PythonRelease{python},
		UV:      []UVRelease{uv},
	}
	if err := ProvisionCatalog(t.Context(), catalog, bundle, store); err != nil {
		t.Fatalf("reuse installed Python/uv generations: %v", err)
	}
}
