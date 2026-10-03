package management

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"loki/internal/tools"
)

type lifecycleBackend struct {
	startFailures int
	stopError     error
	prepared      []string
	running       map[string]bool
}

func (b *lifecycleBackend) Prepare(_ context.Context, reservation DeploymentReservation, _ FullTopology, _ FullLayouts) error {
	b.prepared = append(b.prepared, reservation.ID)
	return nil
}
func (b *lifecycleBackend) Start(_ context.Context, reservation DeploymentReservation) error {
	if b.startFailures > 0 {
		b.startFailures--
		return errors.New("interrupted service creation")
	}
	b.running[reservation.ID] = true
	return nil
}
func (b *lifecycleBackend) Observe(_ context.Context, reservation DeploymentReservation) (FullObservation, error) {
	return FullObservation{State: "observed", Ready: b.running[reservation.ID], Services: map[string]string{}}, nil
}
func (b *lifecycleBackend) Stop(_ context.Context, reservation DeploymentReservation) error {
	if b.stopError != nil {
		return b.stopError
	}
	delete(b.running, reservation.ID)
	return nil
}
func (b *lifecycleBackend) ConfirmAbsent(_ context.Context, reservation DeploymentReservation) error {
	if b.running[reservation.ID] {
		return errors.New("resource remains")
	}
	return nil
}

func lifecycleStore(t *testing.T) Store {
	t.Helper()
	store := Store{Root: t.TempDir()}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	state.Config.Mode = tools.Full
	for _, id := range []tools.ID{"runtime-core", "workspace"} {
		var requires []tools.ID
		if id == "workspace" {
			requires = []tools.ID{"runtime-core"}
		}
		installation := ownedFixtureTarget(t, store, LocalTarget(tools.Full), id, requires...)
		state.Installed[id] = installation
		generation, _ := store.Generation(installation.Artifact)
		if err := os.WriteFile(filepath.Join(generation, "program"), []byte("fixture"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(generation, "contract.json"), []byte("{}"), 0644); err != nil {
			t.Fatal(err)
		}
		if err := os.Mkdir(filepath.Join(generation, "skills"), 0755); err != nil {
			t.Fatal(err)
		}
		payload := FullPayload{Schema: 1, Module: id, Release: Release, Target: installation.Artifact.Target, Images: map[string]string{}, Programs: map[string]string{}, Assets: map[string]string{}}
		if id == "runtime-core" {
			payload.Images["service"] = "example.com/core@sha256:" + strings.Repeat("a", 64)
			payload.Programs["loki"] = "program"
			payload.Assets["execution-contract"] = "contract.json"
		} else {
			payload.Programs["rg"] = "program"
			payload.Assets["skills"] = "skills"
		}
		if err := atomicJSON(filepath.Join(generation, "full-runtime.json"), payload); err != nil {
			t.Fatal(err)
		}
	}
	state.Config.Tools = []tools.Selection{{ID: "workspace", Enabled: true}}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestFullLifecycleResumesOneReservedDeploymentAfterInterruptedStart(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux full lifecycle")
	}
	store := lifecycleStore(t)
	backend := &lifecycleBackend{startFailures: 1, running: map[string]bool{}}
	if _, err := store.ReconcileFull(t.Context(), backend); err == nil {
		t.Fatal("interrupted start reported ready")
	}
	reserved, err := store.Deployments()
	if err != nil || len(reserved) != 1 {
		t.Fatalf("interrupted reservation lost: %v %v", reserved, err)
	}
	reopened := Store{Root: store.Root}
	report, err := reopened.ReconcileFull(t.Context(), backend)
	if err != nil || !report.Observation.Ready || report.Deployment.ID != reserved[0].ID {
		t.Fatalf("retry did not resume same owned resources: %+v %v", report, err)
	}
	if len(backend.prepared) != 2 || backend.prepared[0] != backend.prepared[1] {
		t.Fatal("retry created a second deployment identity")
	}
}

func TestFullLifecycleFailedShutdownKeepsProgramsProtected(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux full lifecycle")
	}
	store := lifecycleStore(t)
	backend := &lifecycleBackend{running: map[string]bool{}}
	if _, err := store.ReconcileFull(t.Context(), backend); err != nil {
		t.Fatal(err)
	}
	if err := store.SetEnabled("workspace", false, nil); err != nil {
		t.Fatal(err)
	}
	backend.stopError = errors.New("termination could not be confirmed")
	if _, err := store.ReconcileFull(t.Context(), backend); err == nil {
		t.Fatal("failed shutdown reported stopped")
	}
	if err := store.Remove("workspace"); !errors.Is(err, ErrGenerationInUse) {
		t.Fatalf("shutdown failure released programs: %v", err)
	}
	backend.stopError = nil
	if err := store.StopFull(t.Context(), backend); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove("workspace"); err != nil {
		t.Fatal(err)
	}
}
