package management

import (
	"context"
	"errors"
	"runtime"
	"testing"

	"loki/internal/tools"
)

type deploymentObservation struct{ err error }

func (o deploymentObservation) ConfirmAbsent(context.Context, DeploymentReservation) error {
	return o.err
}

func TestDeploymentReservationSurvivesManagerExitAndFailedStop(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux full deployment")
	}
	store := Store{Root: t.TempDir()}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	state.Config.Mode = tools.Full
	installation := ownedFixtureTarget(t, store, LocalTarget(tools.Full), "workspace")
	state.Installed["workspace"] = installation
	state.Config.Tools = []tools.Selection{{ID: "workspace", Enabled: true}}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	plan, err := store.PlanFull()
	if err != nil {
		t.Fatal(err)
	}
	reservation, err := store.ReserveDeployment(plan)
	if err != nil {
		t.Fatal(err)
	}
	// Reopen the persistent store without retaining a live kernel lease.
	reopened := Store{Root: store.Root}
	if err := reopened.Remove("workspace"); !errors.Is(err, ErrGenerationInUse) {
		t.Fatalf("reserved program removed: %v", err)
	}
	if err := reopened.ReleaseDeployment(context.Background(), reservation.ID, deploymentObservation{errors.New("backend unavailable")}); err == nil {
		t.Fatal("unknown termination released resources")
	}
	if err := reopened.Remove("workspace"); !errors.Is(err, ErrGenerationInUse) {
		t.Fatalf("failed observation lost reservation: %v", err)
	}
	if err := reopened.ReleaseDeployment(context.Background(), reservation.ID, deploymentObservation{}); err != nil {
		t.Fatal(err)
	}
	if err := reopened.Remove("workspace"); err != nil {
		t.Fatal(err)
	}
}

func TestDeploymentRejectsStaleCompositionAndCorruptReservation(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux full deployment")
	}
	store := Store{Root: t.TempDir()}
	state, _ := store.Load()
	state.Config.Mode = tools.Full
	installation := ownedFixtureTarget(t, store, LocalTarget(tools.Full), "workspace")
	state.Installed["workspace"] = installation
	state.Config.Tools = []tools.Selection{{ID: "workspace", Enabled: true}}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	plan, err := store.PlanFull()
	if err != nil {
		t.Fatal(err)
	}
	state.Config.Tools[0].Enabled = false
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReserveDeployment(plan); err == nil {
		t.Fatal("stale enabled composition reserved")
	}
	if err := atomicJSON(store.Root+"/deployments.json", map[string]any{"schema": 1, "deployments": []any{map[string]any{"id": "foreign"}}}); err != nil {
		t.Fatal(err)
	}
	if err := store.Remove("workspace"); err == nil {
		t.Fatal("corrupt reservation allowed destructive removal")
	}
}
