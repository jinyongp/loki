package toolchain

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func provisionProjectNode(t *testing.T, store GenerationStore, release NodeRelease) {
	t.Helper()
	_, err := store.Provision(t.Context(), release.GenerationID(), func(_ context.Context, root string) error {
		base := filepath.Join(root, "opt", "loki", "toolchain", "node", release.Version, "bin")
		if err := os.MkdirAll(base, 0755); err != nil {
			return err
		}
		for _, name := range []string{"node", "npm", "npx"} {
			if err := os.WriteFile(filepath.Join(base, name), []byte("#!/bin/sh\n"), 0755); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func provisionProjectPnpm(t *testing.T, store GenerationStore, release PnpmRelease) {
	t.Helper()
	_, err := store.Provision(t.Context(), release.GenerationID(), func(_ context.Context, root string) error {
		base := filepath.Join(root, "opt", "loki", "toolchain", "pnpm", release.Version)
		if err := os.MkdirAll(base, 0755); err != nil {
			return err
		}
		return os.WriteFile(filepath.Join(base, "pnpm"), []byte("#!/bin/sh\n"), 0755)
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestProjectResolverSelectsIndependentProjectGenerations(t *testing.T) {
	store := generationStoreFixture(t)
	node22 := nodeRelease("22.23.4", strings.Repeat("a", 64))
	node26 := nodeRelease("26.9.0", strings.Repeat("b", 64))
	pnpm := pnpmRelease("12.5.1", strings.Repeat("c", 64))
	provisionProjectNode(t, store, node22)
	provisionProjectNode(t, store, node26)
	provisionProjectPnpm(t, store, pnpm)

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, "older", "nested"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "current"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "older", ".node-version"), []byte("22\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "current", ".nvmrc"), []byte("26\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "current", "package.json"), []byte("{\"packageManager\":\"pnpm@12.5.1\"}"), 0644); err != nil {
		t.Fatal(err)
	}
	resolver := ProjectResolver{
		Root: root, Store: store,
		Catalog: Catalog{Version: CatalogVersion, Node: []NodeRelease{node22, node26}, Pnpm: []PnpmRelease{pnpm}},
	}

	older, err := resolver.Resolve("older/nested")
	if err != nil {
		t.Fatal(err)
	}
	if len(older) != 1 || older[0].Family != "node" || older[0].Version != node22.Version ||
		older[0].GenerationID != node22.GenerationID() {
		t.Fatalf("older project selection = %#v", older)
	}
	current, err := resolver.Resolve("current")
	if err != nil {
		t.Fatal(err)
	}
	if len(current) != 2 ||
		current[0] != (ProjectSelection{Family: "node", Version: node26.Version, GenerationID: node26.GenerationID()}) ||
		current[1] != (ProjectSelection{Family: "pnpm", Version: pnpm.Version, GenerationID: pnpm.GenerationID()}) {
		t.Fatalf("current project selection = %#v", current)
	}
}

func TestProjectResolverAllowsStandalonePnpmWithoutNodeSelection(t *testing.T) {
	store := generationStoreFixture(t)
	pnpm := pnpmRelease("12.5.1", strings.Repeat("e", 64))
	provisionProjectPnpm(t, store, pnpm)
	root := t.TempDir()
	project := filepath.Join(root, "pnpm-only")
	if err := os.Mkdir(project, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "package.json"), []byte("{\"packageManager\":\"pnpm@12.5.1\"}"), 0644); err != nil {
		t.Fatal(err)
	}
	resolver := ProjectResolver{
		Root: root, Store: store,
		Catalog: Catalog{Version: CatalogVersion, Node: []NodeRelease{
			nodeRelease("26.9.0", strings.Repeat("f", 64)),
		}, Pnpm: []PnpmRelease{pnpm}},
	}
	selected, err := resolver.Resolve("pnpm-only")
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0] != (ProjectSelection{
		Family: "pnpm", Version: pnpm.Version, GenerationID: pnpm.GenerationID(),
	}) {
		t.Fatalf("standalone pnpm selection = %#v", selected)
	}
}

func TestProjectResolverRejectsConflictingOrEscapingDeclarations(t *testing.T) {
	store := generationStoreFixture(t)
	node := nodeRelease("26.9.0", strings.Repeat("d", 64))
	provisionProjectNode(t, store, node)
	root := t.TempDir()
	project := filepath.Join(root, "project")
	if err := os.Mkdir(project, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".node-version"), []byte("26\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".nvmrc"), []byte("25\n"), 0644); err != nil {
		t.Fatal(err)
	}
	resolver := ProjectResolver{Root: root, Store: store, Catalog: Catalog{Version: CatalogVersion, Node: []NodeRelease{node}}}
	if _, err := resolver.Resolve("project"); err == nil || !strings.Contains(err.Error(), "conflicting") {
		t.Fatalf("conflicting selector error = %v", err)
	}
	if _, err := resolver.Resolve("../outside"); err == nil {
		t.Fatal("escaping project cwd was accepted")
	}
}

func provisionProjectPython(t *testing.T, store GenerationStore, release PythonRelease) {
	t.Helper()
	_, err := store.Provision(t.Context(), release.GenerationID(), func(_ context.Context, root string) error {
		base := filepath.Join(root, "opt", "loki", "toolchain", "python", release.Version, "bin")
		if err := os.MkdirAll(base, 0755); err != nil {
			return err
		}
		minor := strings.Join(strings.Split(release.Version, ".")[:2], ".")
		for _, name := range []string{"python3", "python" + minor} {
			if err := os.WriteFile(filepath.Join(base, name), []byte("#!/bin/sh\n"), 0755); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func provisionProjectUV(t *testing.T, store GenerationStore, release UVRelease) {
	t.Helper()
	_, err := store.Provision(t.Context(), release.GenerationID(), func(_ context.Context, root string) error {
		base := filepath.Join(root, "opt", "loki", "toolchain", "uv", release.Version)
		if err := os.MkdirAll(base, 0755); err != nil {
			return err
		}
		for _, name := range []string{"uv", "uvx"} {
			if err := os.WriteFile(filepath.Join(base, name), []byte("#!/bin/sh\n"), 0755); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
}

func TestProjectResolverSelectsPythonAndUVWithinProjectBoundary(t *testing.T) {
	store := generationStoreFixture(t)
	python313 := pythonRelease("3.13.15", "20260805", strings.Repeat("1", 64))
	python314 := pythonRelease("3.14.7", "20260901", strings.Repeat("2", 64))
	uv := uvRelease("0.12.17", strings.Repeat("3", 64))
	provisionProjectPython(t, store, python313)
	provisionProjectPython(t, store, python314)
	provisionProjectUV(t, store, uv)

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".python-version"), []byte("3.13\n"), 0644); err != nil {
		t.Fatal(err)
	}
	project := filepath.Join(root, "app")
	if err := os.MkdirAll(filepath.Join(project, "src"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, ".python-version"), []byte("3.14\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "pyproject.toml"), []byte("[project]\nrequires-python = \">=3.14,<3.15\"\n[tool.uv]\n"), 0644); err != nil {
		t.Fatal(err)
	}
	resolver := ProjectResolver{
		Root: root, Store: store,
		Catalog: Catalog{Version: CatalogVersion, Python: []PythonRelease{python313, python314}, UV: []UVRelease{uv}},
	}
	selected, err := resolver.Resolve("app/src")
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 2 ||
		selected[0] != (ProjectSelection{Family: "python", Version: python314.Version, GenerationID: python314.GenerationID()}) ||
		selected[1] != (ProjectSelection{Family: "uv", Version: uv.Version, GenerationID: uv.GenerationID()}) {
		t.Fatalf("Python/uv selection = %#v", selected)
	}
}

func TestProjectResolverRejectsPythonPinRequirementConflict(t *testing.T) {
	store := generationStoreFixture(t)
	python313 := pythonRelease("3.13.15", "20260805", strings.Repeat("4", 64))
	python314 := pythonRelease("3.14.7", "20260901", strings.Repeat("5", 64))
	provisionProjectPython(t, store, python313)
	provisionProjectPython(t, store, python314)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, ".python-version"), []byte("3.14\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "pyproject.toml"), []byte("[project]\nrequires-python = \"<3.14\"\n"), 0644); err != nil {
		t.Fatal(err)
	}
	resolver := ProjectResolver{
		Root: root, Store: store,
		Catalog: Catalog{Version: CatalogVersion, Python: []PythonRelease{python313, python314}},
	}
	if _, err := resolver.Resolve("."); err == nil {
		t.Fatal("conflicting Python pin and requires-python were accepted")
	}
}
