package toolchain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestRepositoryManagedToolchainCatalog(t *testing.T) {
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "packaging", "go", "toolchain-catalog.json"))
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
	if len(catalog.Python) != 1 || catalog.Python[0].Version != "3.14.7" ||
		catalog.Python[0].Build != "20260901" ||
		catalog.Python[0].SHA256 != "3959f92825141e04adf44982d3a83ee57af0877e893b0796e04c1468749d9b04" {
		t.Fatalf("Python catalog = %#v", catalog.Python)
	}
	if len(catalog.UV) != 1 || catalog.UV[0].Version != "0.12.17" ||
		catalog.UV[0].SHA256 != "fa82fd8dde8e8eefdecada6aa0889666556cfceb690d06e0c3bca49eb3070a63" {
		t.Fatalf("uv catalog = %#v", catalog.UV)
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
