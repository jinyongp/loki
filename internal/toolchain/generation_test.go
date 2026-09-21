package toolchain

import (
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func generationStoreFixture(t *testing.T) GenerationStore {
	t.Helper()
	store := GenerationStore{Root: filepath.Join(t.TempDir(), "generation-store")}
	t.Cleanup(func() { _ = makeTreeWritable(store.Root) })
	return store
}

func generationInstallFixture(t *testing.T) (Manifest, string) {
	t.Helper()
	bundle := t.TempDir()
	artifacts := filepath.Join(bundle, "artifacts")
	if err := os.Mkdir(artifacts, 0755); err != nil {
		t.Fatal(err)
	}
	payload := []byte("#!/bin/sh\necho generation\n")
	if err := os.WriteFile(filepath.Join(artifacts, "tool"), payload, 0600); err != nil {
		t.Fatal(err)
	}
	sum := fmt.Sprintf("%x", sha256.Sum256(payload))
	manifest := Manifest{
		Version:  ManifestVersion,
		Platform: Platform{ID: "ubuntu", Version: "24.04", Arch: "amd64"},
		AptPackages: []AptPackage{
			{Name: "git", Version: "1"},
		},
		Artifacts: []Artifact{
			{
				Name: "tool", Version: "1", Filename: "tool",
				URL: "https://example.test/tool", SHA256: sum, Format: "file",
				InstallPath: "/opt/loki/toolchain/tool/1/tool",
				Links:       map[string]string{"tool": "."},
			},
		},
	}
	return manifest, bundle
}

func TestGenerationStorePublishesImmutableInstallerResult(t *testing.T) {
	manifest, bundle := generationInstallFixture(t)
	store := generationStoreFixture(t)
	id := strings.Repeat("a", 64)
	var calls atomic.Int32
	install := func(ctx context.Context, root string) error {
		calls.Add(1)
		return InstallArtifacts(ctx, manifest, bundle, root)
	}

	generation, err := store.Provision(t.Context(), id, install)
	if err != nil {
		t.Fatal(err)
	}
	if generation.ID != id || generation.Path != filepath.Join(store.Root, "generations", id) ||
		generation.Root != filepath.Join(generation.Path, "root") || !sha256Text.MatchString(generation.TreeSHA256) {
		t.Fatalf("generation = %#v", generation)
	}
	for path, mode := range map[string]os.FileMode{
		generation.Path: 0555,
		generation.Root: 0555,
		filepath.Join(generation.Root, "opt", "loki", "toolchain", "tool", "1", "tool"):                    0555,
		filepath.Join(generation.Root, "opt", "loki", "toolchain", "tool", "1", "tool.loki-artifact.json"): 0444,
	} {
		info, statErr := os.Lstat(path)
		if statErr != nil || info.Mode().Perm() != mode {
			t.Fatalf("mode %s = %v, %v; want %04o", path, info, statErr, mode)
		}
	}
	digest, err := treeDigest(generation.Root)
	if err != nil || digest != generation.TreeSHA256 {
		t.Fatalf("tree digest = %q, %v; want %q", digest, err, generation.TreeSHA256)
	}

	reused, err := store.Provision(t.Context(), id, func(context.Context, string) error {
		t.Fatal("published generation ran installer again")
		return nil
	})
	if err != nil || reused != generation || calls.Load() != 1 {
		t.Fatalf("reused generation = %#v, %v calls=%d", reused, err, calls.Load())
	}
}

func TestGenerationStoreFailureNeverPublishesPartialGeneration(t *testing.T) {
	store := generationStoreFixture(t)
	id := strings.Repeat("b", 64)
	expected := errors.New("synthetic install failure")
	_, err := store.Provision(t.Context(), id, func(_ context.Context, root string) error {
		if writeErr := os.MkdirAll(filepath.Join(root, "partial"), 0755); writeErr != nil {
			return writeErr
		}
		if writeErr := os.WriteFile(filepath.Join(root, "partial", "tool"), []byte("partial"), 0755); writeErr != nil {
			return writeErr
		}
		return expected
	})
	if !errors.Is(err, expected) {
		t.Fatalf("provision error = %v", err)
	}
	if _, err = store.Lookup(id); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("partial generation became visible: %v", err)
	}
	entries, err := os.ReadDir(filepath.Join(store.Root, "staging"))
	if err != nil || len(entries) != 0 {
		t.Fatalf("staging residue = %#v, %v", entries, err)
	}
}

func TestGenerationStoreDeduplicatesConcurrentProvisioning(t *testing.T) {
	store := generationStoreFixture(t)
	id := strings.Repeat("c", 64)
	var calls atomic.Int32
	install := func(_ context.Context, root string) error {
		calls.Add(1)
		if err := os.WriteFile(filepath.Join(root, "tool"), []byte("tool"), 0755); err != nil {
			return err
		}
		time.Sleep(30 * time.Millisecond)
		return nil
	}

	const workers = 8
	results := make(chan Generation, workers)
	errs := make(chan error, workers)
	var group sync.WaitGroup
	for range workers {
		group.Add(1)
		go func() {
			defer group.Done()
			generation, err := store.Provision(t.Context(), id, install)
			results <- generation
			errs <- err
		}()
	}
	group.Wait()
	close(results)
	close(errs)
	for err := range errs {
		if err != nil {
			t.Fatal(err)
		}
	}
	for generation := range results {
		if generation.ID != id || generation.Path != filepath.Join(store.Root, "generations", id) {
			t.Fatalf("generation = %#v", generation)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("installer calls = %d, want 1", calls.Load())
	}
}

func TestGenerationStoreArtifactLockAndReferences(t *testing.T) {
	store := generationStoreFixture(t)
	id := strings.Repeat("d", 64)
	if _, err := store.Provision(t.Context(), id, func(_ context.Context, root string) error {
		return os.WriteFile(filepath.Join(root, "tool"), []byte("tool"), 0755)
	}); err != nil {
		t.Fatal(err)
	}

	artifact := strings.Repeat("e", 64)
	release, err := store.LockArtifact(t.Context(), artifact)
	if err != nil {
		t.Fatal(err)
	}
	waitCtx, cancel := context.WithTimeout(t.Context(), 40*time.Millisecond)
	defer cancel()
	if _, err = store.LockArtifact(waitCtx, artifact); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("contended artifact lock = %v", err)
	}
	release()
	release, err = store.LockArtifact(t.Context(), artifact)
	if err != nil {
		t.Fatal(err)
	}
	release()

	lease, err := store.Acquire(id)
	if err != nil {
		t.Fatal(err)
	}
	inUse, err := store.InUse(id)
	if err != nil || !inUse {
		t.Fatalf("in-use generation = %v, %v", inUse, err)
	}
	if err = lease.Release(); err != nil {
		t.Fatal(err)
	}
	inUse, err = store.InUse(id)
	if err != nil || inUse {
		t.Fatalf("released generation = %v, %v", inUse, err)
	}

	refDir := filepath.Join(store.Root, "refs", id)
	if err = os.MkdirAll(refDir, 0700); err != nil {
		t.Fatal(err)
	}
	stale := filepath.Join(refDir, strings.Repeat("f", 32)+".ref")
	if err = os.WriteFile(stale, nil, 0600); err != nil {
		t.Fatal(err)
	}
	inUse, err = store.InUse(id)
	if err != nil || inUse {
		t.Fatalf("stale reference = %v, %v", inUse, err)
	}
	if _, err = os.Stat(stale); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale reference was not collected: %v", err)
	}
}
