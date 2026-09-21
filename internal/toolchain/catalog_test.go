package toolchain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryManagedToolchainCatalog(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "packaging", "go", "toolchain-catalog.json"))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := LoadCatalog(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Node) != 1 || catalog.Node[0].Version != "26.9.0" {
		t.Fatalf("Node.js catalog = %#v", catalog.Node)
	}
	if len(catalog.Pnpm) != 1 || catalog.Pnpm[0].Version != "12.5.1" {
		t.Fatalf("pnpm catalog = %#v", catalog.Pnpm)
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
