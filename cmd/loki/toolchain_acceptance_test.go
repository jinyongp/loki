package main

import (
	"context"
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"loki/internal/work/toolchains"
)

func acceptanceStore(t *testing.T) toolchain.GenerationStore {
	t.Helper()
	store := toolchain.GenerationStore{Root: filepath.Join(t.TempDir(), "store")}
	t.Cleanup(func() {
		_ = filepath.WalkDir(store.Root, func(path string, entry fs.DirEntry, err error) error {
			if err != nil || entry.Type()&os.ModeSymlink != 0 {
				return nil
			}
			info, statErr := entry.Info()
			if statErr != nil {
				return nil
			}
			mode := info.Mode().Perm() | 0200
			if info.IsDir() {
				mode |= 0700
			}
			_ = os.Chmod(path, mode)
			return nil
		})
	})
	return store
}

func provisionAcceptanceNode(t *testing.T, store toolchain.GenerationStore, release toolchain.NodeRelease) toolchain.Generation {
	t.Helper()
	generation, err := store.Provision(t.Context(), release.GenerationID(), func(_ context.Context, root string) error {
		base := filepath.Join(root, "opt", "loki", "toolchain", "node", release.Version, "bin")
		if err := os.MkdirAll(base, 0755); err != nil {
			return err
		}
		for _, name := range []string{"node", "npm", "npx"} {
			body := "#!/bin/sh\nprintf '" + name + "-" + release.Version + "\n'\n"
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

func provisionAcceptancePnpm(t *testing.T, store toolchain.GenerationStore, release toolchain.PnpmRelease) toolchain.Generation {
	t.Helper()
	generation, err := store.Provision(t.Context(), release.GenerationID(), func(_ context.Context, root string) error {
		base := filepath.Join(root, "opt", "loki", "toolchain", "pnpm", release.Version)
		if err := os.MkdirAll(base, 0755); err != nil {
			return err
		}
		body := "#!/bin/sh\nprintf 'pnpm-" + release.Version + "\n'\n"
		return os.WriteFile(filepath.Join(base, "pnpm"), []byte(body), 0755)
	})
	if err != nil {
		t.Fatal(err)
	}
	return generation
}

func mountAcceptanceSelections(t *testing.T, root string, selections []toolchain.ProjectSelection, generations map[string]toolchain.Generation) {
	t.Helper()
	if err := os.MkdirAll(root, 0755); err != nil {
		t.Fatal(err)
	}
	for _, selection := range selections {
		generation, ok := generations[selection.GenerationID]
		if !ok {
			t.Fatalf("missing generation for selection %#v", selection)
		}
		if err := os.Symlink(generation.Root, filepath.Join(root, selection.Family)); err != nil {
			t.Fatal(err)
		}
	}
}

func runResolvedShim(t *testing.T, root, command string) string {
	t.Helper()
	target, err := resolveToolchainShim(root, command)
	if err != nil {
		t.Fatal(err)
	}
	output, err := exec.Command(target).CombinedOutput()
	if err != nil {
		t.Fatalf("%s shim target failed: %v: %s", command, err, output)
	}
	return strings.TrimSpace(string(output))
}

func acceptanceRustRelease(version, date, digestSeed string) toolchain.RustRelease {
	host := "x86_64-unknown-linux-gnu"
	artifact := func(component, target string, index int) toolchain.RustArtifact {
		filename := component + "-" + version
		if target != "" {
			filename += "-" + target
		}
		filename += ".tar.xz"
		return toolchain.RustArtifact{
			Component:       component,
			Target:          target,
			Filename:        filename,
			URL:             "https://static.rust-lang.org/dist/" + date + "/" + filename,
			SHA256:          strings.Repeat(digestSeed, 63) + string(rune('0'+index)),
			StripComponents: 2,
		}
	}
	return toolchain.RustRelease{
		Channel: "stable",
		Version: version,
		Date:    date,
		Host:    host,
		Artifacts: []toolchain.RustArtifact{
			artifact("cargo", host, 1),
			artifact("rust-std", host, 2),
			artifact("rustc", host, 3),
		},
	}
}

func provisionAcceptanceRust(t *testing.T, store toolchain.GenerationStore, release toolchain.RustRelease) toolchain.Generation {
	t.Helper()
	generation, err := store.Provision(t.Context(), release.GenerationID(), func(_ context.Context, root string) error {
		base := filepath.Join(root, "opt", "loki", "toolchain", "rust", release.Version, "active", "bin")
		if err := os.MkdirAll(base, 0755); err != nil {
			return err
		}
		for _, name := range []string{"rustc", "cargo"} {
			body := "#!/bin/sh\nprintf '" + name + "-" + release.Version + "\n'\n"
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

func TestManagedRustAcceptanceTwoProjectsUseDifferentVersionsThroughSameShims(t *testing.T) {
	store := acceptanceStore(t)
	rust97 := acceptanceRustRelease("1.97.1", "2026-08-06", "a")
	rust98 := acceptanceRustRelease("1.98.1", "2026-09-03", "b")
	generations := map[string]toolchain.Generation{}
	for _, release := range []toolchain.RustRelease{rust97, rust98} {
		generation := provisionAcceptanceRust(t, store, release)
		generations[generation.ID] = generation
	}

	workspace := t.TempDir()
	for name, version := range map[string]string{
		"legacy":  rust97.Version,
		"current": rust98.Version,
	} {
		project := filepath.Join(workspace, name)
		if err := os.Mkdir(project, 0755); err != nil {
			t.Fatal(err)
		}
		declaration := "[toolchain]\nchannel = \"" + version + "\"\nprofile = \"minimal\"\n"
		if err := os.WriteFile(filepath.Join(project, "rust-toolchain.toml"), []byte(declaration), 0644); err != nil {
			t.Fatal(err)
		}
	}

	resolver := toolchain.ProjectResolver{
		Root:  workspace,
		Store: store,
		Catalog: toolchain.Catalog{
			Version: toolchain.CatalogVersion,
			Rust:    []toolchain.RustRelease{rust97, rust98},
		},
	}
	legacy, err := resolver.Resolve("legacy")
	if err != nil {
		t.Fatal(err)
	}
	current, err := resolver.Resolve("current")
	if err != nil {
		t.Fatal(err)
	}
	if len(legacy) != 1 || len(current) != 1 ||
		legacy[0].Family != "rust" || current[0].Family != "rust" ||
		legacy[0].Version == current[0].Version ||
		legacy[0].GenerationID == current[0].GenerationID {
		t.Fatalf("Rust project selections = %#v / %#v", legacy, current)
	}

	legacyMount := filepath.Join(t.TempDir(), "managed")
	currentMount := filepath.Join(t.TempDir(), "managed")
	mountAcceptanceSelections(t, legacyMount, legacy, generations)
	mountAcceptanceSelections(t, currentMount, current, generations)
	if got := runResolvedShim(t, legacyMount, "rustc"); got != "rustc-1.97.1" {
		t.Fatalf("legacy rustc shim = %q", got)
	}
	if got := runResolvedShim(t, legacyMount, "cargo"); got != "cargo-1.97.1" {
		t.Fatalf("legacy cargo shim = %q", got)
	}
	if got := runResolvedShim(t, currentMount, "rustc"); got != "rustc-1.98.1" {
		t.Fatalf("current rustc shim = %q", got)
	}
	if got := runResolvedShim(t, currentMount, "cargo"); got != "cargo-1.98.1" {
		t.Fatalf("current cargo shim = %q", got)
	}
}

func TestManagedToolchainAcceptanceTwoProjectsUseDifferentVersionsThroughSameShims(t *testing.T) {
	store := acceptanceStore(t)
	node22 := toolchain.NodeRelease{
		Version: "22.23.4",
		URL:     "https://nodejs.org/download/release/v22.23.4/node-v22.23.4-linux-x64.tar.xz",
		SHA256:  strings.Repeat("a", 64),
	}
	node26 := toolchain.NodeRelease{
		Version: "26.9.0",
		URL:     "https://nodejs.org/download/release/v26.9.0/node-v26.9.0-linux-x64.tar.xz",
		SHA256:  strings.Repeat("b", 64),
	}
	pnpm11 := toolchain.PnpmRelease{
		Version: "11.27.0",
		URL:     "https://github.com/pnpm/pnpm/releases/download/v11.27.0/pnpm-linux-x64.tar.gz",
		SHA256:  strings.Repeat("c", 64),
	}
	pnpm12 := toolchain.PnpmRelease{
		Version: "12.5.1",
		URL:     "https://github.com/pnpm/pnpm/releases/download/v12.5.1/pnpm-linux-x64.tar.gz",
		SHA256:  strings.Repeat("d", 64),
	}
	generations := map[string]toolchain.Generation{}
	for _, release := range []toolchain.NodeRelease{node22, node26} {
		generation := provisionAcceptanceNode(t, store, release)
		generations[generation.ID] = generation
	}
	for _, release := range []toolchain.PnpmRelease{pnpm11, pnpm12} {
		generation := provisionAcceptancePnpm(t, store, release)
		generations[generation.ID] = generation
	}

	workspace := t.TempDir()
	for name, versions := range map[string][2]string{
		"legacy":  {"22", "pnpm@11.27.0"},
		"current": {"26", "pnpm@12.5.1"},
	} {
		project := filepath.Join(workspace, name)
		if err := os.Mkdir(project, 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(project, ".node-version"), []byte(versions[0]+"\n"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(project, "package.json"), []byte("{\"packageManager\":\""+versions[1]+"\"}"), 0644); err != nil {
			t.Fatal(err)
		}
	}

	resolver := toolchain.ProjectResolver{
		Root:  workspace,
		Store: store,
		Catalog: toolchain.Catalog{
			Version: toolchain.CatalogVersion,
			Node:    []toolchain.NodeRelease{node22, node26},
			Pnpm:    []toolchain.PnpmRelease{pnpm11, pnpm12},
		},
	}
	legacy, err := resolver.Resolve("legacy")
	if err != nil {
		t.Fatal(err)
	}
	current, err := resolver.Resolve("current")
	if err != nil {
		t.Fatal(err)
	}
	if len(legacy) != 2 || len(current) != 2 {
		t.Fatalf("project selections = %#v / %#v", legacy, current)
	}
	if legacy[0].Version == current[0].Version || legacy[1].Version == current[1].Version {
		t.Fatalf("projects did not resolve distinct versions: %#v / %#v", legacy, current)
	}

	legacyMount := filepath.Join(t.TempDir(), "managed")
	currentMount := filepath.Join(t.TempDir(), "managed")
	mountAcceptanceSelections(t, legacyMount, legacy, generations)
	mountAcceptanceSelections(t, currentMount, current, generations)

	if got := runResolvedShim(t, legacyMount, "node"); got != "node-22.23.4" {
		t.Fatalf("legacy Node shim = %q", got)
	}
	if got := runResolvedShim(t, legacyMount, "pnpm"); got != "pnpm-11.27.0" {
		t.Fatalf("legacy pnpm shim = %q", got)
	}
	if got := runResolvedShim(t, currentMount, "node"); got != "node-26.9.0" {
		t.Fatalf("current Node shim = %q", got)
	}
	if got := runResolvedShim(t, currentMount, "pnpm"); got != "pnpm-12.5.1" {
		t.Fatalf("current pnpm shim = %q", got)
	}
}
