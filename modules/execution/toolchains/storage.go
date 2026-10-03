package toolchain

import (
	"context"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"
	"time"
)

var ErrGenerationQuotaExceeded = errors.New("toolchain generation storage quota exceeded")

type GenerationLimits struct {
	MaxBytes       int64
	MaxGenerations int
	Retention      time.Duration
}

func DefaultGenerationLimits() GenerationLimits {
	return GenerationLimits{
		MaxBytes:       32 << 30,
		MaxGenerations: 128,
		Retention:      30 * 24 * time.Hour,
	}
}

func normalizeGenerationLimits(limits GenerationLimits) (GenerationLimits, error) {
	defaults := DefaultGenerationLimits()
	if limits.MaxBytes == 0 {
		limits.MaxBytes = defaults.MaxBytes
	}
	if limits.MaxGenerations == 0 {
		limits.MaxGenerations = defaults.MaxGenerations
	}
	if limits.Retention == 0 {
		limits.Retention = defaults.Retention
	}
	if limits.MaxBytes < 1 || limits.MaxBytes > 1<<40 ||
		limits.MaxGenerations < 1 || limits.MaxGenerations > 4096 ||
		limits.Retention < time.Millisecond || limits.Retention > 365*24*time.Hour {
		return GenerationLimits{}, errors.New("toolchain generation storage limits are outside the supported range")
	}
	return limits, nil
}

type GenerationUsage struct {
	Bytes       int64
	Generations int
	Referenced  int
	Protected   int
}

type GenerationGCResult struct {
	Before         GenerationUsage
	After          GenerationUsage
	Removed        []string
	ReclaimedBytes int64
}

type generationStorageEntry struct {
	ID          string
	Bytes       int64
	PublishedAt time.Time
	Referenced  bool
	Protected   bool
}

func (s GenerationStore) normalizedLimits() (GenerationLimits, error) {
	return normalizeGenerationLimits(s.Limits)
}

func (s GenerationStore) StoragePolicy() (GenerationLimits, error) {
	return s.normalizedLimits()
}

func (s GenerationStore) protectedSet() (map[string]bool, error) {
	result := make(map[string]bool, len(s.Protected))
	for _, id := range s.Protected {
		if err := validateGenerationID(id); err != nil {
			return nil, fmt.Errorf("invalid protected toolchain generation: %w", err)
		}
		result[id] = true
	}
	return result, nil
}

func (s GenerationStore) validateStorageLayout() error {
	if err := s.validate(); err != nil {
		return err
	}
	for _, path := range []string{s.Root, filepath.Join(s.Root, "generations")} {
		info, err := os.Lstat(path)
		if err != nil {
			return err
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return errors.New("toolchain generation storage layout is invalid")
		}
	}
	return nil
}

func (s GenerationStore) storageEntries() ([]generationStorageEntry, error) {
	if err := s.validateStorageLayout(); err != nil {
		return nil, err
	}
	protected, err := s.protectedSet()
	if err != nil {
		return nil, err
	}
	entries, err := os.ReadDir(filepath.Join(s.Root, "generations"))
	if err != nil {
		return nil, err
	}
	result := make([]generationStorageEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || validateGenerationID(entry.Name()) != nil {
			return nil, errors.New("toolchain generation directory contains an invalid entry")
		}
		generation, lookupErr := s.Lookup(entry.Name())
		if lookupErr != nil {
			return nil, lookupErr
		}
		bytes, usageErr := generationDiskUsage(generation.Path)
		if usageErr != nil {
			return nil, usageErr
		}
		metadata, statErr := os.Stat(filepath.Join(generation.Path, "generation.json"))
		if statErr != nil {
			return nil, statErr
		}
		referenced, referenceErr := s.InUse(generation.ID)
		if referenceErr != nil {
			return nil, referenceErr
		}
		result = append(result, generationStorageEntry{
			ID: generation.ID, Bytes: bytes, PublishedAt: metadata.ModTime().UTC(),
			Referenced: referenced, Protected: protected[generation.ID],
		})
	}
	sort.Slice(result, func(i, j int) bool {
		if result[i].PublishedAt.Equal(result[j].PublishedAt) {
			return result[i].ID < result[j].ID
		}
		return result[i].PublishedAt.Before(result[j].PublishedAt)
	})
	return result, nil
}

func generationDiskUsage(root string) (int64, error) {
	var bytes int64
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		info, err := entry.Info()
		if err != nil {
			return err
		}
		if !info.IsDir() {
			if info.Size() > 0 && bytes > (1<<62)-info.Size() {
				return errors.New("toolchain generation storage accounting overflow")
			}
			bytes += info.Size()
		}
		return nil
	})
	return bytes, err
}

func generationUsage(entries []generationStorageEntry) GenerationUsage {
	var usage GenerationUsage
	for _, entry := range entries {
		usage.Bytes += entry.Bytes
		usage.Generations++
		if entry.Referenced {
			usage.Referenced++
		}
		if entry.Protected {
			usage.Protected++
		}
	}
	return usage
}

func (s GenerationStore) Usage() (GenerationUsage, error) {
	entries, err := s.storageEntries()
	if err != nil {
		return GenerationUsage{}, err
	}
	return generationUsage(entries), nil
}

func (s GenerationStore) Collect(ctx context.Context, now time.Time) (GenerationGCResult, error) {
	if err := ctx.Err(); err != nil {
		return GenerationGCResult{}, err
	}
	limits, err := s.normalizedLimits()
	if err != nil {
		return GenerationGCResult{}, err
	}
	if now.IsZero() {
		return GenerationGCResult{}, errors.New("toolchain generation collection time is required")
	}
	now = now.UTC()
	releaseStorage, err := s.lock(ctx, "storage")
	if err != nil {
		return GenerationGCResult{}, err
	}
	defer releaseStorage()
	return s.collectLocked(ctx, now, limits, 0, 0)
}

func (s GenerationStore) collectLocked(
	ctx context.Context,
	now time.Time,
	limits GenerationLimits,
	reserveBytes int64,
	reserveGenerations int,
) (GenerationGCResult, error) {
	entries, err := s.storageEntries()
	if err != nil {
		return GenerationGCResult{}, err
	}
	result := GenerationGCResult{Before: generationUsage(entries)}
	bytes := result.Before.Bytes + reserveBytes
	count := result.Before.Generations + reserveGenerations
	for _, entry := range entries {
		if err := ctx.Err(); err != nil {
			return result, err
		}
		expired := !now.Before(entry.PublishedAt.Add(limits.Retention))
		overQuota := bytes > limits.MaxBytes || count > limits.MaxGenerations
		if entry.Referenced || entry.Protected || (!expired && !overQuota) {
			continue
		}
		releaseGeneration, locked, lockErr := s.tryLock("generation-" + entry.ID)
		if lockErr != nil {
			return result, lockErr
		}
		if !locked {
			continue
		}
		inUse, inUseErr := s.InUse(entry.ID)
		if inUseErr != nil {
			releaseGeneration()
			return result, inUseErr
		}
		if inUse {
			releaseGeneration()
			continue
		}
		generation, lookupErr := s.Lookup(entry.ID)
		if lookupErr != nil {
			releaseGeneration()
			return result, lookupErr
		}
		if err = makeTreeWritable(generation.Path); err == nil {
			err = os.RemoveAll(generation.Path)
		}
		releaseGeneration()
		if err != nil {
			return result, err
		}
		result.Removed = append(result.Removed, entry.ID)
		result.ReclaimedBytes += entry.Bytes
		bytes -= entry.Bytes
		count--
	}
	if bytes > limits.MaxBytes || count > limits.MaxGenerations {
		return result, ErrGenerationQuotaExceeded
	}
	after, err := s.storageEntries()
	if err != nil {
		return result, err
	}
	result.After = generationUsage(after)
	return result, nil
}
