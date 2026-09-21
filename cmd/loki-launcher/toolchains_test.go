package main

import (
	"context"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	applauncher "loki/internal/app/launcher"
	"loki/internal/toolchain"
	"loki/internal/work/jobs"
)

func cleanupGenerationStore(t *testing.T, root string) {
	t.Helper()
	t.Cleanup(func() {
		_ = filepath.WalkDir(root, func(path string, entry fs.DirEntry, err error) error {
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
}

func provisionLauncherNode(t *testing.T, store toolchain.GenerationStore, id, version string) toolchain.Generation {
	t.Helper()
	generation, err := store.Provision(t.Context(), id, func(_ context.Context, root string) error {
		base := filepath.Join(root, "opt", "loki", "toolchain", "node", version, "bin")
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
	return generation
}

func TestLauncherToolchainResolverLeasesImmutableGeneration(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	cleanupGenerationStore(t, root)
	store := toolchain.GenerationStore{Root: root}
	id := strings.Repeat("a", 64)
	generation := provisionLauncherNode(t, store, id, "26.9.0")
	resolver := launcherToolchainResolver{store: store}

	const owner = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	refs := []jobs.ToolchainRef{{
		Family: "node", Version: "26.9.0", GenerationID: id,
	}}
	resolved, err := resolver.Resolve(t.Context(), owner, refs)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.Mounts) != 1 ||
		resolved.Mounts[0].Family != "node" || resolved.Mounts[0].Source != generation.Root ||
		resolved.Close == nil || resolved.Discard == nil {
		t.Fatalf("resolved toolchains = %#v", resolved)
	}
	inUse, err := store.InUse(id)
	if err != nil || !inUse {
		t.Fatalf("leased generation = %v, %v", inUse, err)
	}
	if err = resolved.Close(); err != nil {
		t.Fatal(err)
	}
	inUse, err = store.InUse(id)
	if err != nil || !inUse {
		t.Fatalf("closed launcher lease lost durable reference = %v, %v", inUse, err)
	}
	if err = resolver.Cleanup(owner, refs); err != nil {
		t.Fatal(err)
	}
	inUse, err = store.InUse(id)
	if err != nil || inUse {
		t.Fatalf("terminal generation cleanup = %v, %v", inUse, err)
	}
}

func TestLauncherToolchainResolverRejectsGenerationVersionMismatch(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	cleanupGenerationStore(t, root)
	store := toolchain.GenerationStore{Root: root}
	id := strings.Repeat("b", 64)
	provisionLauncherNode(t, store, id, "26.9.0")
	resolver := launcherToolchainResolver{store: store}

	_, err := resolver.Resolve(t.Context(), "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb", []jobs.ToolchainRef{{
		Family: "node", Version: "22.23.4", GenerationID: id,
	}})
	if err == nil {
		t.Fatal("generation/version mismatch was accepted")
	}
	inUse, inUseErr := store.InUse(id)
	if inUseErr != nil || inUse {
		t.Fatalf("mismatched generation leaked a lease: %v, %v", inUse, inUseErr)
	}
}

var _ applauncher.ToolchainResolver = launcherToolchainResolver{}
