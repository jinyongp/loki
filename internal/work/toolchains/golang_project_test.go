package toolchain

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProjectResolverSelectsGoFromNearestModule(t *testing.T) {
	store := generationStoreFixture(t)
	old := goRelease("1.26.3", strings.Repeat("2", 64))
	current := goRelease("1.27.1", strings.Repeat("3", 64))
	provisionGoMetadataGeneration(t, store, old)

	root := t.TempDir()
	project := filepath.Join(root, "app")
	if err := os.MkdirAll(filepath.Join(project, "pkg"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "go.mod"), []byte(
		"module example.com/app\n\ngo 1.26.0\ntoolchain go1.26.3\n",
	), 0644); err != nil {
		t.Fatal(err)
	}

	resolver := ProjectResolver{
		Root: root, Store: store,
		Catalog: Catalog{Version: CatalogVersion, Go: []GoRelease{old, current}},
	}
	selected, err := resolver.Resolve("app/pkg")
	if err != nil {
		t.Fatal(err)
	}
	want := ProjectSelection{Family: "go", Version: old.Version, GenerationID: old.GenerationID()}
	if len(selected) != 1 || selected[0] != want {
		t.Fatalf("Go project selection = %#v, want %#v", selected, want)
	}
}

func TestProjectResolverGoWorkspaceOverridesNestedModuleRequirement(t *testing.T) {
	store := generationStoreFixture(t)
	old := goRelease("1.26.3", strings.Repeat("4", 64))
	current := goRelease("1.27.1", strings.Repeat("5", 64))
	provisionGoMetadataGeneration(t, store, old)
	provisionGoMetadataGeneration(t, store, current)

	root := t.TempDir()
	module := filepath.Join(root, "module")
	if err := os.MkdirAll(filepath.Join(module, "pkg"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "go.work"), []byte(
		"go 1.27.1\n\nuse ./module\n",
	), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(module, "go.mod"), []byte(
		"module example.com/module\n\ngo 1.26.0\ntoolchain go1.26.3\n",
	), 0644); err != nil {
		t.Fatal(err)
	}
	resolver := ProjectResolver{
		Root: root, Store: store,
		Catalog: Catalog{Version: CatalogVersion, Go: []GoRelease{old, current}},
	}
	selected, err := resolver.Resolve("module/pkg")
	if err != nil {
		t.Fatal(err)
	}
	want := ProjectSelection{Family: "go", Version: current.Version, GenerationID: current.GenerationID()}
	if len(selected) != 1 || selected[0] != want {
		t.Fatalf("Go workspace selection = %#v, want %#v", selected, want)
	}
}

func TestProjectResolverRejectsInvalidGoToolchainDeclarations(t *testing.T) {
	for name, body := range map[string]string{
		"older-toolchain":  "module example.com/app\n\ngo 1.27.1\ntoolchain go1.26.3\n",
		"custom-toolchain": "module example.com/app\n\ngo 1.27.1\ntoolchain custom-go\n",
	} {
		t.Run(name, func(t *testing.T) {
			root := t.TempDir()
			if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte(body), 0644); err != nil {
				t.Fatal(err)
			}
			resolver := ProjectResolver{
				Root: root, Store: generationStoreFixture(t),
				Catalog: Catalog{Version: CatalogVersion, Go: []GoRelease{
					goRelease("1.27.1", strings.Repeat("6", 64)),
				}},
			}
			if _, err := resolver.Resolve("."); err == nil {
				t.Fatalf("invalid Go declaration accepted: %s", body)
			}
		})
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.work"), []byte("use ./module\n"), 0644); err != nil {
		t.Fatal(err)
	}
	resolver := ProjectResolver{
		Root: root, Store: generationStoreFixture(t),
		Catalog: Catalog{Version: CatalogVersion, Go: []GoRelease{
			goRelease("1.27.1", strings.Repeat("7", 64)),
		}},
	}
	if _, err := resolver.Resolve("."); err == nil || !strings.Contains(err.Error(), "no go directive") {
		t.Fatalf("go.work without go directive error = %v", err)
	}
}

func TestProjectResolverGoModWithoutGoDirectiveUsesLegacyMinimum(t *testing.T) {
	store := generationStoreFixture(t)
	release := goRelease("1.27.1", strings.Repeat("8", 64))
	provisionGoMetadataGeneration(t, store, release)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "go.mod"), []byte("module example.com/app\n"), 0644); err != nil {
		t.Fatal(err)
	}
	resolver := ProjectResolver{
		Root: root, Store: store,
		Catalog: Catalog{Version: CatalogVersion, Go: []GoRelease{release}},
	}
	selected, err := resolver.Resolve(".")
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].Version != release.Version {
		t.Fatalf("Go legacy minimum selection = %#v", selected)
	}
}
