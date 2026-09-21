package compose

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"loki/internal/host/lifecycle"
)

type noActiveJobs struct{}

func (noActiveJobs) ActiveJobs(context.Context) ([]string, error) { return nil, nil }

func TestDistinctReleaseTransactionalUpdateAndRollbackAcceptance(t *testing.T) {
	backend, runner, workspace := composeBackendFixture(t)
	if err := os.WriteFile(filepath.Join(workspace, "preserve.txt"), []byte("workspace-data"), 0600); err != nil {
		t.Fatal(err)
	}

	stateRoot := filepath.Join(t.TempDir(), "state")
	store, err := lifecycle.EnsureFileStore(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	releaseA := composeGeneration(t, "1.0.0", "1")
	releaseB := composeGeneration(t, "2.0.0", "2")
	if releaseA.ID == releaseB.ID || releaseA.Spec.CoreImageDigest == releaseB.Spec.CoreImageDigest {
		t.Fatalf("acceptance releases are not distinct: A=%#v B=%#v", releaseA, releaseB)
	}
	installation := lifecycle.InstallationState{Scope: "system", Workspace: workspace}
	now := time.Date(2026, 9, 21, 13, 0, 0, 0, time.UTC)
	tick := now
	clock := func() time.Time {
		tick = tick.Add(time.Millisecond)
		return tick
	}
	if err = store.InitializeInstall(t.Context(), releaseA, installation, clock()); err != nil {
		t.Fatal(err)
	}

	engine := &lifecycle.TransactionEngine{Store: store, Backend: backend, Now: clock}
	manager := lifecycle.Manager{
		Store: store, Jobs: noActiveJobs{}, Applier: engine, Maintainer: engine, Now: clock,
	}
	installPlan, err := manager.Prepare(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if _, err = manager.Apply(t.Context(), lifecycle.ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	runtimeA, found, err := backend.loadRuntime()
	if err != nil || !found {
		t.Fatalf("release A runtime = %#v found=%v err=%v", runtimeA, found, err)
	}
	if runtimeA.GenerationID != releaseA.ID ||
		runtimeA.CoreImage != defaultCoreRepository+"@"+releaseA.Spec.CoreImageDigest {
		t.Fatalf("release A runtime = %#v", runtimeA)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil || snapshot.Installed == nil || snapshot.Installed.ID != releaseA.ID ||
		snapshot.Host.ActiveGenerationID != releaseA.ID || installPlan.CandidateGenerationID != releaseA.ID {
		t.Fatalf("release A lifecycle snapshot = %#v plan=%#v err=%v", snapshot, installPlan, err)
	}

	if err = store.SaveAvailable(t.Context(), releaseB); err != nil {
		t.Fatal(err)
	}
	updatePlan, err := manager.Prepare(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if updatePlan.ActiveGenerationID != releaseA.ID || updatePlan.CandidateGenerationID != releaseB.ID {
		t.Fatalf("distinct update plan = %#v", updatePlan)
	}
	if _, err = manager.Apply(t.Context(), lifecycle.ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	runtimeB, found, err := backend.loadRuntime()
	if err != nil || !found {
		t.Fatalf("release B runtime = %#v found=%v err=%v", runtimeB, found, err)
	}
	if runtimeB.GenerationID != releaseB.ID ||
		runtimeB.CoreImage != defaultCoreRepository+"@"+releaseB.Spec.CoreImageDigest ||
		runtimeB.CoreImage == runtimeA.CoreImage {
		t.Fatalf("release B runtime = %#v; release A = %#v", runtimeB, runtimeA)
	}
	snapshot, err = store.Snapshot(t.Context())
	if err != nil || snapshot.Installed == nil || snapshot.Installed.ID != releaseB.ID ||
		snapshot.Host.ActiveGenerationID != releaseB.ID {
		t.Fatalf("release B lifecycle snapshot = %#v err=%v", snapshot, err)
	}

	if err = manager.Rollback(t.Context(), lifecycle.MutationOptions{}); err != nil {
		t.Fatal(err)
	}
	rolledBack, found, err := backend.loadRuntime()
	if err != nil || !found {
		t.Fatalf("rolled-back runtime = %#v found=%v err=%v", rolledBack, found, err)
	}
	if rolledBack.GenerationID != releaseA.ID || rolledBack.CoreImage != runtimeA.CoreImage {
		t.Fatalf("rolled-back runtime = %#v; want release A %#v", rolledBack, runtimeA)
	}
	snapshot, err = store.Snapshot(t.Context())
	if err != nil || snapshot.Installed == nil || snapshot.Installed.ID != releaseA.ID ||
		snapshot.Host.ActiveGenerationID != releaseA.ID {
		t.Fatalf("rolled-back lifecycle snapshot = %#v err=%v", snapshot, err)
	}
	if raw, err := os.ReadFile(filepath.Join(workspace, "preserve.txt")); err != nil || string(raw) != "workspace-data" {
		t.Fatalf("workspace after update/rollback = %q err=%v", raw, err)
	}

	var sawReleaseA, sawReleaseB bool
	for _, call := range runner.snapshot() {
		for _, value := range call.env {
			if value == "LOKI_IMAGE="+runtimeA.CoreImage {
				sawReleaseA = true
			}
			if value == "LOKI_IMAGE="+runtimeB.CoreImage {
				sawReleaseB = true
			}
		}
	}
	if !sawReleaseA || !sawReleaseB {
		t.Fatalf("compose execution did not observe both release images: A=%v B=%v calls=%#v", sawReleaseA, sawReleaseB, runner.snapshot())
	}

	backups, err := store.ListBackups(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	var sawARecovery, sawBRecovery bool
	for _, backup := range backups {
		if backup.Installed == nil {
			continue
		}
		switch backup.Installed.ID {
		case releaseA.ID:
			sawARecovery = true
		case releaseB.ID:
			sawBRecovery = true
		}
	}
	if !sawARecovery || !sawBRecovery {
		t.Fatalf("distinct release recovery evidence missing: A=%v B=%v backups=%#v", sawARecovery, sawBRecovery, backups)
	}
	if strings.TrimSpace(runtimeA.CoreImage) == strings.TrimSpace(runtimeB.CoreImage) {
		t.Fatal("distinct release acceptance used identical image references")
	}
}
