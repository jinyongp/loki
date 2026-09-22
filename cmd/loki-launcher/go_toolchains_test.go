package main

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"loki/internal/work/jobs"
	"loki/internal/work/toolchains"
)

func TestLauncherToolchainResolverAcceptsGoGeneration(t *testing.T) {
	root := filepath.Join(t.TempDir(), "store")
	cleanupGenerationStore(t, root)
	store := toolchain.GenerationStore{Root: root}
	id := strings.Repeat("9", 64)
	version := "1.27.1"
	generation, err := store.Provision(t.Context(), id, func(_ context.Context, generationRoot string) error {
		base := filepath.Join(generationRoot, "opt", "loki", "toolchain", "go", version, "bin")
		if err := os.MkdirAll(base, 0755); err != nil {
			return err
		}
		for _, name := range []string{"go", "gofmt"} {
			if err := os.WriteFile(filepath.Join(base, name), []byte("#!/bin/sh\n"), 0755); err != nil {
				return err
			}
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}

	resolver := launcherToolchainResolver{store: store}
	owner := strings.Repeat("b", 32)
	refs := []jobs.ToolchainRef{{Family: "go", Version: version, GenerationID: id}}
	resolved, err := resolver.Resolve(t.Context(), owner, refs)
	if err != nil {
		t.Fatal(err)
	}
	if len(resolved.Mounts) != 1 || resolved.Mounts[0].Family != "go" || resolved.Mounts[0].Source != generation.Root {
		t.Fatalf("resolved Go mounts = %#v", resolved.Mounts)
	}
	if err = resolved.Close(); err != nil {
		t.Fatal(err)
	}
	if err = resolver.Cleanup(owner, refs); err != nil {
		t.Fatal(err)
	}

	if _, err = resolver.Resolve(t.Context(), owner, []jobs.ToolchainRef{{
		Family: "go", Version: "go1.27.1", GenerationID: id,
	}}); err == nil {
		t.Fatal("non-canonical Go Job version was accepted")
	}
}
