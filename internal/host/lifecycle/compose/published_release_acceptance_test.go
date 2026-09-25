package compose

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"loki/internal/host/lifecycle"
	"loki/internal/host/releases"
)

const (
	releaseAManifestEnv = "LOKI_ACCEPTANCE_RELEASE_A_MANIFEST"
	releaseBManifestEnv = "LOKI_ACCEPTANCE_RELEASE_B_MANIFEST"
)

type failReleaseRestartOnceRunner struct {
	base        Runner
	targetImage string

	mu    sync.Mutex
	armed bool
	fired bool
}

func (r *failReleaseRestartOnceRunner) arm() {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.armed = true
	r.fired = false
}

func (r *failReleaseRestartOnceRunner) didFire() bool {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.fired
}

func (r *failReleaseRestartOnceRunner) Run(ctx context.Context, env []string, args ...string) ([]byte, error) {
	r.mu.Lock()
	shouldFail := r.armed &&
		!r.fired &&
		slices.Contains(env, "LOKI_IMAGE="+r.targetImage) &&
		containsOrderedArgs(args, "up", "-d", "--remove-orphans")
	if shouldFail {
		r.fired = true
		r.armed = false
	}
	r.mu.Unlock()
	if shouldFail {
		return nil, errors.New("synthetic published-release restart failure")
	}
	return r.base.Run(ctx, env, args...)
}

func containsOrderedArgs(args []string, values ...string) bool {
	if len(values) == 0 {
		return true
	}
	index := 0
	for _, value := range args {
		if value != values[index] {
			continue
		}
		index++
		if index == len(values) {
			return true
		}
	}
	return false
}

func publishedLifecycleGeneration(t *testing.T, path string) lifecycle.Generation {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var manifest releases.ReleaseManifest
	if err = json.Unmarshal(raw, &manifest); err != nil {
		t.Fatal(err)
	}
	manifest, err = releases.NewReleaseManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	encoded, err := json.Marshal(manifest.Generation)
	if err != nil {
		t.Fatal(err)
	}
	var generation lifecycle.Generation
	if err = json.Unmarshal(encoded, &generation); err != nil {
		t.Fatal(err)
	}
	if !generation.Valid() {
		t.Fatal("published release generation is invalid for the lifecycle contract")
	}
	return generation
}

func TestPublishedReleaseTransactionalUpdateFailureRecoveryRollbackAcceptance(t *testing.T) {
	releaseAPath := strings.TrimSpace(os.Getenv(releaseAManifestEnv))
	releaseBPath := strings.TrimSpace(os.Getenv(releaseBManifestEnv))
	if releaseAPath == "" && releaseBPath == "" {
		t.Skip("release-only acceptance requires two published/candidate release manifests")
	}
	if releaseAPath == "" || releaseBPath == "" {
		t.Fatalf("%s and %s must both be configured", releaseAManifestEnv, releaseBManifestEnv)
	}
	if _, err := exec.LookPath("docker"); err != nil {
		t.Fatalf("docker is required for release lifecycle acceptance: %v", err)
	}
	setfacl, err := exec.LookPath("setfacl")
	if err != nil {
		t.Fatalf("setfacl is required for release lifecycle acceptance: %v", err)
	}

	releaseA := publishedLifecycleGeneration(t, releaseAPath)
	releaseB := publishedLifecycleGeneration(t, releaseBPath)
	if releaseA.ID == releaseB.ID ||
		releaseA.Spec.Version == releaseB.Spec.Version ||
		releaseA.Spec.CoreImageDigest == releaseB.Spec.CoreImageDigest {
		t.Fatalf("release fixtures are not distinct: A=%s/%s B=%s/%s",
			releaseA.Spec.Version, releaseA.ID, releaseB.Spec.Version, releaseB.ID)
	}

	workspace := filepath.Join(t.TempDir(), "workspace")
	if err = os.Mkdir(workspace, 0750); err != nil {
		t.Fatal(err)
	}
	preservePath := filepath.Join(workspace, "preserve.txt")
	if err = os.WriteFile(preservePath, []byte("published-release-workspace-data"), 0640); err != nil {
		t.Fatal(err)
	}
	if output, aclErr := exec.Command(setfacl, "-m", "u:10000:rwx,d:u:10000:rwx", workspace).CombinedOutput(); aclErr != nil {
		t.Fatalf("set workspace ACL: %v: %s", aclErr, output)
	}

	backendRoot := filepath.Join(t.TempDir(), "runtime")
	if err = os.Mkdir(backendRoot, 0700); err != nil {
		t.Fatal(err)
	}
	project := fmt.Sprintf("loki-release-accept-%d", os.Getpid())
	runner := &failReleaseRestartOnceRunner{
		base:        ExecRunner{},
		targetImage: defaultCoreRepository + "@" + releaseB.Spec.CoreImageDigest,
	}
	backend, err := New(Config{StateRoot: backendRoot, Project: project, Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanupCtx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		if state, found, loadErr := backend.loadRuntime(); loadErr == nil && found {
			_, _ = backend.compose(cleanupCtx, state, "down", "--remove-orphans", "--volumes")
		}
		for _, name := range persistentVolumes {
			_, _ = runner.base.Run(cleanupCtx, nil, "volume", "rm", "-f", backend.volumeName(name))
		}
	})

	store, err := lifecycle.EnsureFileStore(filepath.Join(t.TempDir(), "state"))
	if err != nil {
		t.Fatal(err)
	}
	installation := lifecycle.InstallationState{
		Scope: "user", Workspace: workspace, DockerAccess: "direct",
	}
	now := time.Now().UTC().Add(time.Second)
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
	if installPlan.CandidateGenerationID != releaseA.ID {
		t.Fatalf("release A install plan = %#v", installPlan)
	}
	if err = backend.Health(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err = store.SaveAvailable(t.Context(), releaseB); err != nil {
		t.Fatal(err)
	}
	failedPlan, err := manager.Prepare(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if failedPlan.ActiveGenerationID != releaseA.ID || failedPlan.CandidateGenerationID != releaseB.ID {
		t.Fatalf("release B failed-apply plan = %#v", failedPlan)
	}
	runner.arm()
	if _, err = manager.Apply(t.Context(), lifecycle.ApplyOptions{}); err == nil ||
		!strings.Contains(err.Error(), "synthetic published-release restart failure") {
		t.Fatalf("release B synthetic failure = %v", err)
	}
	if !runner.didFire() {
		t.Fatal("release B synthetic failure was not injected")
	}
	recovered, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if recovered.Installed == nil || recovered.Installed.ID != releaseA.ID ||
		recovered.Host.ActiveGenerationID != releaseA.ID {
		t.Fatalf("failed release B apply did not recover release A: %#v", recovered)
	}
	recoveredRuntime, found, err := backend.loadRuntime()
	if err != nil || !found ||
		recoveredRuntime.GenerationID != releaseA.ID ||
		recoveredRuntime.CoreImage != defaultCoreRepository+"@"+releaseA.Spec.CoreImageDigest {
		t.Fatalf("runtime after failed release B apply = %#v found=%v err=%v", recoveredRuntime, found, err)
	}
	if err = backend.Health(t.Context()); err != nil {
		t.Fatalf("release A health after failed B apply: %v", err)
	}

	successPlan, err := manager.Prepare(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if successPlan.CandidateGenerationID != releaseB.ID {
		t.Fatalf("release B retry plan = %#v", successPlan)
	}
	if _, err = manager.Apply(t.Context(), lifecycle.ApplyOptions{}); err != nil {
		t.Fatal(err)
	}
	runtimeB, found, err := backend.loadRuntime()
	if err != nil || !found ||
		runtimeB.GenerationID != releaseB.ID ||
		runtimeB.CoreImage != defaultCoreRepository+"@"+releaseB.Spec.CoreImageDigest {
		t.Fatalf("release B runtime = %#v found=%v err=%v", runtimeB, found, err)
	}
	if err = backend.Health(t.Context()); err != nil {
		t.Fatal(err)
	}

	if err = manager.Rollback(t.Context(), lifecycle.MutationOptions{}); err != nil {
		t.Fatal(err)
	}
	rolledBack, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if rolledBack.Installed == nil || rolledBack.Installed.ID != releaseA.ID ||
		rolledBack.Host.ActiveGenerationID != releaseA.ID {
		t.Fatalf("explicit rollback did not restore release A: %#v", rolledBack)
	}
	runtimeA, found, err := backend.loadRuntime()
	if err != nil || !found ||
		runtimeA.GenerationID != releaseA.ID ||
		runtimeA.CoreImage != defaultCoreRepository+"@"+releaseA.Spec.CoreImageDigest {
		t.Fatalf("runtime after explicit rollback = %#v found=%v err=%v", runtimeA, found, err)
	}
	if err = backend.Health(t.Context()); err != nil {
		t.Fatal(err)
	}
	if raw, readErr := os.ReadFile(preservePath); readErr != nil ||
		string(raw) != "published-release-workspace-data" {
		t.Fatalf("workspace after failed apply/update/rollback = %q err=%v", raw, readErr)
	}
}
