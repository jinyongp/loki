package toolchain

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

func provisionStorageGeneration(t *testing.T, store GenerationStore, id string, bytes int) Generation {
	t.Helper()
	generation, err := store.Provision(t.Context(), id, func(_ context.Context, root string) error {
		return os.WriteFile(filepath.Join(root, "payload"), []byte(strings.Repeat("x", bytes)), 0644)
	})
	if err != nil {
		t.Fatal(err)
	}
	return generation
}

func ageGeneration(t *testing.T, generation Generation, when time.Time) {
	t.Helper()
	path := filepath.Join(generation.Path, "generation.json")
	if err := os.Chtimes(path, when, when); err != nil {
		t.Fatal(err)
	}
}

func TestGenerationStoreCollectPreservesProtectedAndDurableReferences(t *testing.T) {
	now := time.Date(2026, time.September, 22, 1, 0, 0, 0, time.UTC)
	store := generationStoreFixture(t)
	store.Limits = GenerationLimits{
		MaxBytes: 1 << 20, MaxGenerations: 8, Retention: time.Hour,
	}
	protectedID := strings.Repeat("1", 64)
	referencedID := strings.Repeat("2", 64)
	staleID := strings.Repeat("3", 64)
	protected := provisionStorageGeneration(t, store, protectedID, 32)
	referenced := provisionStorageGeneration(t, store, referencedID, 32)
	stale := provisionStorageGeneration(t, store, staleID, 32)
	old := now.Add(-2 * time.Hour)
	for _, generation := range []Generation{protected, referenced, stale} {
		ageGeneration(t, generation, old)
	}

	store.Protected = []string{protectedID}
	lease, err := store.Acquire(referencedID, "job-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa")
	if err != nil {
		t.Fatal(err)
	}
	result, err := store.Collect(t.Context(), now)
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Removed) != 1 || result.Removed[0] != staleID ||
		result.Before.Protected != 1 || result.Before.Referenced != 1 ||
		result.After.Generations != 2 {
		t.Fatalf("collection result = %#v", result)
	}
	if _, err = store.Lookup(protectedID); err != nil {
		t.Fatalf("protected generation was reclaimed: %v", err)
	}
	if _, err = store.Lookup(referencedID); err != nil {
		t.Fatalf("referenced generation was reclaimed: %v", err)
	}
	if _, err = store.Lookup(staleID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("stale generation still exists: %v", err)
	}

	// A launcher crash closes the advisory lock but deliberately leaves the
	// durable ref. GC must still retain the generation until owner cleanup.
	if err = lease.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Collect(t.Context(), now.Add(time.Minute)); err != nil {
		t.Fatal(err)
	}
	if _, err = store.Lookup(referencedID); err != nil {
		t.Fatalf("durably referenced generation was reclaimed after lease close: %v", err)
	}
	if err = store.ReleaseOwner(referencedID, "job-aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"); err != nil {
		t.Fatal(err)
	}
	result, err = store.Collect(t.Context(), now.Add(2*time.Minute))
	if err != nil {
		t.Fatal(err)
	}
	if len(result.Removed) != 1 || result.Removed[0] != referencedID {
		t.Fatalf("post-owner cleanup collection = %#v", result)
	}
}

func TestGenerationStoreProvisionReclaimsOldestUnreferencedForQuota(t *testing.T) {
	now := time.Now().UTC()
	store := generationStoreFixture(t)
	store.Limits = GenerationLimits{
		MaxBytes: 1 << 20, MaxGenerations: 8, Retention: 24 * time.Hour,
	}
	protectedID := strings.Repeat("4", 64)
	reclaimableID := strings.Repeat("5", 64)
	protected := provisionStorageGeneration(t, store, protectedID, 32)
	reclaimable := provisionStorageGeneration(t, store, reclaimableID, 32)
	ageGeneration(t, protected, now.Add(-2*time.Hour))
	ageGeneration(t, reclaimable, now.Add(-time.Hour))

	store.Limits.MaxGenerations = 2
	store.Protected = []string{protectedID}
	newID := strings.Repeat("6", 64)
	newGeneration := provisionStorageGeneration(t, store, newID, 32)
	if _, err := store.Lookup(protectedID); err != nil {
		t.Fatalf("protected generation missing after quota collection: %v", err)
	}
	if _, err := store.Lookup(reclaimableID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("quota did not reclaim unreferenced generation: %v", err)
	}
	if _, err := store.Lookup(newID); err != nil {
		t.Fatalf("new generation was not published: %v", err)
	}

	store.Protected = []string{protectedID, newGeneration.ID}
	blockedID := strings.Repeat("7", 64)
	_, err := store.Provision(t.Context(), blockedID, func(_ context.Context, root string) error {
		return os.WriteFile(filepath.Join(root, "payload"), []byte("blocked"), 0644)
	})
	if !errors.Is(err, ErrGenerationQuotaExceeded) {
		t.Fatalf("protected quota error = %v", err)
	}
	if _, err = store.Lookup(blockedID); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("quota-rejected generation became visible: %v", err)
	}
}

func TestGenerationStoreConcurrentProvisionDoesNotOversubscribeQuota(t *testing.T) {
	store := generationStoreFixture(t)
	store.Limits = GenerationLimits{
		MaxBytes: 1 << 20, MaxGenerations: 1, Retention: 24 * time.Hour,
	}
	ids := []string{strings.Repeat("9", 64), strings.Repeat("a", 64)}
	start := make(chan struct{})
	results := make(chan error, len(ids))
	for _, id := range ids {
		id := id
		go func() {
			<-start
			_, err := store.Provision(t.Context(), id, func(_ context.Context, root string) error {
				return os.WriteFile(filepath.Join(root, "payload"), []byte("generation"), 0644)
			})
			results <- err
		}()
	}
	close(start)
	var success int
	for range ids {
		err := <-results
		if err != nil {
			t.Fatalf("concurrent provision error = %v", err)
		}
		success++
	}
	if success != len(ids) {
		t.Fatalf("concurrent provision successes = %d, want %d", success, len(ids))
	}
	usage, err := store.Usage()
	if err != nil {
		t.Fatal(err)
	}
	if usage.Generations != 1 {
		t.Fatalf("concurrent provision oversubscribed generations: %#v", usage)
	}
}

func TestGenerationAcquireSerializesWithGenerationMutation(t *testing.T) {
	store := generationStoreFixture(t)
	id := strings.Repeat("8", 64)
	provisionStorageGeneration(t, store, id, 16)
	release, err := store.lock(t.Context(), "generation-"+id)
	if err != nil {
		t.Fatal(err)
	}
	defer release()

	ctx, cancel := context.WithTimeout(t.Context(), 30*time.Millisecond)
	defer cancel()
	if _, err = store.AcquireContext(ctx, id, "job-bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"); !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("AcquireContext while generation lock held = %v", err)
	}
}
