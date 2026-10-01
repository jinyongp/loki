package compose

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"loki/internal/host/lifecycle"
	"loki/internal/progress"
)

type progressCheckingRunner struct {
	*fakeRunner
	t      *testing.T
	latest *progress.Event
}

func (r progressCheckingRunner) Run(ctx context.Context, env []string, args ...string) ([]byte, error) {
	switch {
	case slices.Contains(args, "stop"):
		if r.latest.Phase != "snapshot-stop" {
			r.t.Error("service shutdown began without progress")
		}
	case len(args) > 0 && args[0] == "run":
		if r.latest.Phase != "snapshot-volume" {
			r.t.Error("volume backup began without progress")
		}
		for _, arg := range args {
			if strings.HasPrefix(arg, "type=bind,src=") && strings.HasSuffix(arg, ",dst=/backup") {
				dir := strings.TrimSuffix(strings.TrimPrefix(arg, "type=bind,src="), ",dst=/backup")
				if err := os.WriteFile(filepath.Join(dir, args[len(args)-3]), []byte("snapshot payload"), 0600); err != nil {
					return nil, err
				}
			}
		}
	case slices.Contains(args, "up"):
		want := "restart"
		if slices.Contains(args, "--wait") {
			want = "health"
		}
		if r.latest.Phase != want {
			r.t.Errorf("service operation began with phase %q, want %q", r.latest.Phase, want)
		}
	}
	return r.fakeRunner.Run(ctx, env, args...)
}

func TestIntegrationProgressPrecedesComposeWorkAndPreservesBackup(t *testing.T) {
	backend, runner, workspace := composeBackendFixture(t)
	generation := composeGeneration(t, "1.2.3", "b")
	if err := backend.Activate(t.Context(), generation, lifecycle.InstallationState{Scope: "user", Workspace: workspace}); err != nil {
		t.Fatal(err)
	}
	var latest progress.Event
	var phases []string
	backend.Progress = progress.ReporterFunc(func(event progress.Event) {
		latest = event
		phases = append(phases, event.Phase)
	})
	backend.runner = progressCheckingRunner{fakeRunner: runner, t: t, latest: &latest}
	volume := backend.volumeName("runtime-state")
	runner.outputs[strings.Join([]string{"volume", "ls", "--quiet", "--filter", "name=^" + volume + "$"}, "\x00")] = []byte(volume + "\n")
	snapshot, err := backend.Snapshot(t.Context(), lifecycle.OperationEnableComponent, lifecycle.Snapshot{})
	if err != nil {
		t.Fatal(err)
	}
	var manifest snapshotManifest
	if err := readPrivateJSON(filepath.Join(snapshot.Ref, "manifest.json"), &manifest); err != nil {
		t.Fatal(err)
	}
	if !manifest.Volumes["runtime-state"].Present || manifest.Volumes["runtime-state"].SHA256 == "" {
		t.Fatal("volume backup or checksum was omitted")
	}
	if err := backend.Restart(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err := backend.Health(t.Context()); err != nil {
		t.Fatal(err)
	}
	if !slices.Contains(phases, "snapshot-hash") {
		t.Fatalf("missing checksum progress: %v", phases)
	}
}
