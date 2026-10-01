package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loki/internal/host/lifecycle"
)

func TestCandidatePrerequisiteFailurePreservesCurrentCLIAndRestoreSkipsRepair(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Now().UTC()
	activeRaw := []byte("#!/bin/sh\necho active\n")
	candidateRaw := []byte("#!/bin/sh\necho candidate\n")
	active := managedCLIGenerationFixture(t, "1.2.3", now.Add(-time.Hour), activeRaw)
	candidate := managedCLIGenerationFixture(t, "1.3.0", now, candidateRaw)
	activePaths, err := resolveHostCLIInstallPaths(false, active.ID)
	if err != nil {
		t.Fatal(err)
	}
	candidatePaths, err := resolveHostCLIInstallPaths(false, candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	writeManagedCLIBinary(t, activePaths.Binary, activeRaw)
	writeManagedCLIBinary(t, candidatePaths.Binary, candidateRaw)
	if err = os.MkdirAll(filepath.Dir(activePaths.Link), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(activePaths.Binary, activePaths.Link); err != nil {
		t.Fatal(err)
	}
	calls := 0
	assets := &managedHostCLIReleaseAssets{RepairAppliance: func(_ context.Context, binary string) error {
		calls++
		if binary != candidatePaths.Binary {
			t.Fatalf("repair did not use candidate: %s", binary)
		}
		return errors.New("required package unavailable")
	}}
	if err = assets.Activate(t.Context(), candidate); err == nil {
		t.Fatal("activation ignored prerequisite failure")
	}
	if err = assets.ValidateCurrent(t.Context(), active); err != nil {
		t.Fatalf("failed repair changed current CLI: %v", err)
	}
	if err = assets.Restore(t.Context(), active); err != nil {
		t.Fatal(err)
	}
	if calls != 1 {
		t.Fatalf("rollback repeated OS repair: %d", calls)
	}
	writeManagedCLIBinary(t, candidatePaths.Binary, []byte("tampered"))
	if err = assets.Activate(t.Context(), candidate); err == nil {
		t.Fatal("unverified candidate accepted")
	}
	if calls != 1 {
		t.Fatal("unverified candidate executed")
	}
}

func managedCLIGenerationFixture(t *testing.T, version string, releasedAt time.Time, raw []byte) lifecycle.Generation {
	t.Helper()
	sum := sha256.Sum256(raw)
	generation, err := lifecycle.NewGeneration(lifecycle.GenerationSpec{
		Version: version, ReleasedAt: releasedAt.UTC().Truncate(time.Second),
		HostBinaryDigest: "sha256:" + hex.EncodeToString(sum[:]),
		CoreImageDigest:  "sha256:" + strings.Repeat("b", 64),
		ConfigSchema:     1, PolicySchema: 1, ToolchainSchema: 1, StateSchema: 1,
		Reads: lifecycle.Compatibility{
			Config: lifecycle.SchemaRange{Min: 1, Max: 1}, Policy: lifecycle.SchemaRange{Min: 1, Max: 1},
			Toolchain: lifecycle.SchemaRange{Min: 1, Max: 1}, State: lifecycle.SchemaRange{Min: 1, Max: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return generation
}

func writeManagedCLIBinary(t *testing.T, path string, raw []byte) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, raw, 0755); err != nil {
		t.Fatal(err)
	}
}

func TestManagedHostCLIReleaseAssetsSwitchAndRestore(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	activeRaw := []byte("#!/bin/sh\necho active\n")
	candidateRaw := []byte("#!/bin/sh\necho candidate\n")
	active := managedCLIGenerationFixture(t, "1.2.3", now.Add(-time.Hour), activeRaw)
	candidate := managedCLIGenerationFixture(t, "1.3.0", now, candidateRaw)
	activePaths, err := resolveHostCLIInstallPaths(false, active.ID)
	if err != nil {
		t.Fatal(err)
	}
	candidatePaths, err := resolveHostCLIInstallPaths(false, candidate.ID)
	if err != nil {
		t.Fatal(err)
	}
	writeManagedCLIBinary(t, activePaths.Binary, activeRaw)
	writeManagedCLIBinary(t, candidatePaths.Binary, candidateRaw)
	if err = os.MkdirAll(filepath.Dir(activePaths.Link), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(activePaths.Binary, activePaths.Link); err != nil {
		t.Fatal(err)
	}

	assets := &managedHostCLIReleaseAssets{}
	if err = assets.ValidateCurrent(t.Context(), active); err != nil {
		t.Fatal(err)
	}
	if err = assets.Activate(t.Context(), candidate); err != nil {
		t.Fatal(err)
	}
	target, err := managedHostCLILinkTarget(candidatePaths)
	if err != nil || target != candidatePaths.Binary {
		t.Fatalf("candidate CLI target = %q err=%v", target, err)
	}
	if err = assets.Restore(t.Context(), active); err != nil {
		t.Fatal(err)
	}
	target, err = managedHostCLILinkTarget(activePaths)
	if err != nil || target != activePaths.Binary {
		t.Fatalf("restored CLI target = %q err=%v", target, err)
	}
}

func TestManagedHostCLIReleaseAssetsRejectMissingStagedCandidate(t *testing.T) {
	t.Setenv("HOME", t.TempDir())
	now := time.Date(2026, 9, 26, 12, 0, 0, 0, time.UTC)
	activeRaw := []byte("#!/bin/sh\necho active\n")
	candidateRaw := []byte("#!/bin/sh\necho candidate\n")
	active := managedCLIGenerationFixture(t, "1.2.3", now.Add(-time.Hour), activeRaw)
	candidate := managedCLIGenerationFixture(t, "1.3.0", now, candidateRaw)
	activePaths, err := resolveHostCLIInstallPaths(false, active.ID)
	if err != nil {
		t.Fatal(err)
	}
	writeManagedCLIBinary(t, activePaths.Binary, activeRaw)
	if err = os.MkdirAll(filepath.Dir(activePaths.Link), 0755); err != nil {
		t.Fatal(err)
	}
	if err = os.Symlink(activePaths.Binary, activePaths.Link); err != nil {
		t.Fatal(err)
	}

	assets := &managedHostCLIReleaseAssets{}
	if err = assets.Activate(t.Context(), candidate); err == nil || !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("missing staged candidate error = %v", err)
	}
	target, targetErr := managedHostCLILinkTarget(activePaths)
	if targetErr != nil || target != activePaths.Binary {
		t.Fatalf("failed switch changed active CLI target = %q err=%v", target, targetErr)
	}
}
