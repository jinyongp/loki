package toolchain

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func provisionRustMetadataGeneration(t *testing.T, store GenerationStore, release RustRelease) {
	t.Helper()
	if _, err := store.Provision(t.Context(), release.GenerationID(), func(context.Context, string) error { return nil }); err != nil {
		t.Fatal(err)
	}
}

func TestProjectResolverSelectsRustToolchainTOML(t *testing.T) {
	release := rustMetadataRelease(
		"stable", "1.98.1", "2026-09-03",
		[]string{"cargo", "clippy", "rust-docs", "rustc", "rustfmt"},
		[]string{rustDefaultHost, "wasm32-unknown-unknown"},
	)
	store := generationStoreFixture(t)
	provisionRustMetadataGeneration(t, store, release)

	root := t.TempDir()
	project := filepath.Join(root, "app")
	if err := os.MkdirAll(filepath.Join(project, "src"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(project, "rust-toolchain.toml"), []byte(
		"[toolchain]\nchannel = \"1.98\"\nprofile = \"default\"\ntargets = [\"wasm32-unknown-unknown\"]\n",
	), 0644); err != nil {
		t.Fatal(err)
	}

	resolver := ProjectResolver{
		Root:    root,
		Store:   store,
		Catalog: Catalog{Version: CatalogVersion, Rust: []RustRelease{release}},
	}
	selected, err := resolver.Resolve("app/src")
	if err != nil {
		t.Fatal(err)
	}
	want := ProjectSelection{Family: "rust", Version: release.Version, GenerationID: release.GenerationID()}
	if len(selected) != 1 || selected[0] != want {
		t.Fatalf("Rust project selection = %#v, want %#v", selected, want)
	}
}

func TestProjectResolverRustToolchainLegacyFileWinsOverTOML(t *testing.T) {
	old := rustMetadataRelease(
		"stable", "1.97.1", "2026-08-06",
		[]string{"cargo", "clippy", "rust-docs", "rustc", "rustfmt"}, []string{rustDefaultHost},
	)
	current := rustMetadataRelease(
		"stable", "1.98.1", "2026-09-03",
		[]string{"cargo", "clippy", "rust-docs", "rustc", "rustfmt"}, []string{rustDefaultHost},
	)
	store := generationStoreFixture(t)
	provisionRustMetadataGeneration(t, store, old)
	provisionRustMetadataGeneration(t, store, current)

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "rust-toolchain"), []byte("1.97.1\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "rust-toolchain.toml"), []byte(
		"[toolchain]\nchannel = \"1.98.1\"\nprofile = \"minimal\"\n",
	), 0644); err != nil {
		t.Fatal(err)
	}
	resolver := ProjectResolver{
		Root:    root,
		Store:   store,
		Catalog: Catalog{Version: CatalogVersion, Rust: []RustRelease{old, current}},
	}
	selected, err := resolver.Resolve(".")
	if err != nil {
		t.Fatal(err)
	}
	if len(selected) != 1 || selected[0].Version != old.Version {
		t.Fatalf("legacy Rust declaration did not win: %#v", selected)
	}
}

func TestProjectResolverRejectsRustPathAndUnavailableComponent(t *testing.T) {
	for name, body := range map[string]string{
		"path":          "[toolchain]\npath = \"../custom-rust\"\n",
		"unknown-field": "[toolchain]\nchannel = \"stable\"\nfuture-setting = true\n",
	} {
		t.Run(name, func(t *testing.T) {
			if _, err := parseRustProjectRequest([]byte(body), false); err == nil {
				t.Fatalf("invalid Rust toolchain declaration accepted: %s", body)
			}
		})
	}

	release := rustMetadataRelease(
		"stable", "1.98.1", "2026-09-03",
		[]string{"cargo", "rustc"}, []string{rustDefaultHost},
	)
	store := generationStoreFixture(t)
	provisionRustMetadataGeneration(t, store, release)
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "rust-toolchain.toml"), []byte(
		"[toolchain]\nchannel = \"stable\"\nprofile = \"minimal\"\ncomponents = [\"rust-src\"]\n",
	), 0644); err != nil {
		t.Fatal(err)
	}
	resolver := ProjectResolver{
		Root:    root,
		Store:   store,
		Catalog: Catalog{Version: CatalogVersion, Rust: []RustRelease{release}},
	}
	if _, err := resolver.Resolve("."); err == nil || !strings.Contains(err.Error(), "no administrator-permitted release") {
		t.Fatalf("unavailable Rust component error = %v", err)
	}
}
