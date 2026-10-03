package management

import (
	"context"
	"errors"
	"fmt"
	"io"
	"loki/internal/tools"
	"os"
	"path/filepath"
	"slices"
	"time"
)

// acquire prepares immutable resources without changing desired exposure or
// current installation pointers. Its caller holds the host mutation lock.
func (s Store) acquire(ctx context.Context, artifact tools.Artifact, expected *tools.Manifest, progress io.Writer) (Installation, error) {
	if err := artifact.Validate(); err != nil {
		return Installation{}, err
	}
	if progress == nil {
		progress = io.Discard
	}
	generation, err := s.Generation(artifact)
	if err != nil {
		return Installation{}, err
	}
	if err := os.MkdirAll(filepath.Dir(generation), 0700); err != nil {
		return Installation{}, err
	}
	if err := verifyOwner(generation, artifact); errors.Is(err, os.ErrNotExist) {
		staging, err := os.MkdirTemp(filepath.Dir(generation), ".staging-")
		if err != nil {
			return Installation{}, err
		}
		defer os.RemoveAll(staging)
		archive, err := os.CreateTemp(s.Root, ".artifact-")
		if err != nil {
			return Installation{}, err
		}
		defer os.Remove(archive.Name())
		if s.ArchiveDirectory == "" {
			fmt.Fprintf(progress, "Downloading %s %s for %s/%s...\n", artifact.Module, artifact.Release, artifact.Target.OS, artifact.Target.Arch)
			err = download(ctx, artifact, archive, progress)
		} else {
			fmt.Fprintf(progress, "Checking local %s archive against its trusted release receipt...\n", artifact.Module)
			err = localArtifact(ctx, s.ArchiveDirectory, artifact, archive)
		}
		closeErr := archive.Close()
		if err != nil {
			return Installation{}, err
		}
		if closeErr != nil {
			return Installation{}, closeErr
		}
		fmt.Fprintf(progress, "Installing %s into its managed generation...\n", artifact.Module)
		if err := extractArtifact(archive.Name(), artifact, staging); err != nil {
			return Installation{}, err
		}
		// Full-mode generations are mounted read-only into distinct service
		// identities. Archive members are already normalized to 0644/0755;
		// only this program root needs traversal by those service identities.
		if artifact.Target.Mode == tools.Full {
			if err := os.Chmod(staging, 0755); err != nil {
				return Installation{}, err
			}
		}
		if err := atomicJSON(filepath.Join(staging, ".loki-owner.json"), Owner{Schema: 1, Identity: artifact.Identity()}); err != nil {
			return Installation{}, err
		}
		if _, err := os.Lstat(generation); err == nil {
			return Installation{}, fmt.Errorf("generation exists without valid ownership")
		} else if !errors.Is(err, os.ErrNotExist) {
			return Installation{}, err
		}
		if err := os.Rename(staging, generation); err != nil {
			return Installation{}, err
		}
	} else if err != nil {
		return Installation{}, err
	}
	data, err := os.ReadFile(filepath.Join(generation, "module.json"))
	if err != nil {
		return Installation{}, err
	}
	manifest, err := tools.ParseManifest(data)
	if err != nil {
		return Installation{}, err
	}
	if manifest.ID != artifact.Module || manifest.Release != artifact.Release || !slices.Contains(manifest.Targets, artifact.Target) {
		return Installation{}, fmt.Errorf("artifact manifest differs from release catalog")
	}
	if expected != nil && !sameTargetManifest(manifest, *expected, artifact.Target) {
		return Installation{}, fmt.Errorf("artifact module metadata differs from trusted catalog")
	}
	if artifact.Target.Mode == tools.Full {
		if _, err := loadFullPayload(generation, artifact); err != nil {
			return Installation{}, fmt.Errorf("full artifact runtime resources are invalid: %w", err)
		}
	}
	installation := Installation{Artifact: artifact, Manifest: manifest}
	record, err := generationMetadata(generation)
	if errors.Is(err, os.ErrNotExist) {
		err = atomicJSON(filepath.Join(generation, ".loki-generation.json"), generationRecord{Schema: 1, PreparedAt: time.Now().UTC(), Installation: installation})
	} else if err == nil && (record.Installation.Artifact.Identity() != artifact.Identity() || !sameManifest(record.Installation.Manifest, manifest)) {
		err = fmt.Errorf("generation metadata differs from verified installation")
	}
	if err != nil {
		return Installation{}, err
	}
	return installation, nil
}

// Native producers record only the target they actually prepared. A trusted
// release catalog can combine those targets while preserving one module
// contract. Archive support must be a subset of the catalog, and all authority
// fields remain identical; only the support list is projected for comparison.
func sameTargetManifest(actual, expected tools.Manifest, target tools.Target) bool {
	if !slices.Contains(actual.Targets, target) || !slices.Contains(expected.Targets, target) {
		return false
	}
	for _, declared := range actual.Targets {
		if !slices.Contains(expected.Targets, declared) {
			return false
		}
	}
	actual.Targets, expected.Targets = []tools.Target{target}, []tools.Target{target}
	return sameManifest(actual, expected)
}
