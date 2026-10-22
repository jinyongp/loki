package management

import (
	"errors"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	controlpolicy "loki/internal/control/policy"
	hostpolicy "loki/internal/host/policy"
)

func TestFullResourcePolicyBusyPublicationAndLostStart(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("native Linux full lifecycle")
	}
	store := lifecycleStore(t)
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	generation, err := store.Generation(state.Installed["runtime-core"].Artifact)
	if err != nil {
		t.Fatal(err)
	}
	contract, err := os.ReadFile("../../packaging/images/config/execution-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(generation, "contract.json"), contract, 0600); err != nil {
		t.Fatal(err)
	}
	backend := &lifecycleBackend{running: map[string]bool{}}
	store.MaintenanceBackend = backend
	initial, err := store.ReconcileFull(t.Context(), backend)
	if err != nil {
		t.Fatal(err)
	}
	resource := controlpolicy.ApplicationResource{ID: "9af6c2e6-c1d4-41a3-a6e9-9fb3c07c67d2", Alias: "example", Repository: "repo:example", Principals: []controlpolicy.ApplicationGrant{{Principal: "agent", Metadata: true}}, Environments: []string{"common"}, Keys: map[string]string{"TOKEN": "secret"}, Network: "none"}
	plan, err := store.PrepareApplicationResources(hostpolicy.ApplicationChange{Action: "set", Alias: resource.Alias, Resource: &resource})
	if err != nil {
		t.Fatal(err)
	}
	before, _ := os.ReadFile(store.ActivationPath())
	backend.stopError = errors.New("active Job refuses mutation")
	if err := store.ApplyApplicationResources(t.Context(), plan); err == nil {
		t.Fatal("busy policy publication succeeded")
	}
	after, _ := os.ReadFile(store.ActivationPath())
	if string(before) != string(after) || !backend.running[initial.Deployment.ID] {
		t.Fatal("busy rejection changed activation or stopped the current deployment")
	}
	backend.stopError = nil
	if err := store.ApplyApplicationResources(t.Context(), plan); err != nil {
		t.Fatal(err)
	}
	backend.startFailures = 1
	if _, err := store.ReconcileFull(t.Context(), backend); err == nil {
		t.Fatal("start interruption was hidden")
	}
	if err := store.ApplyApplicationResources(t.Context(), plan); err != nil {
		t.Fatal("same plan could not replay committed publication", err)
	}
	ready, err := store.ReconcileFull(t.Context(), backend)
	if err != nil || !ready.Observation.Ready || ready.Deployment.ID == initial.Deployment.ID {
		t.Fatalf("new policy deployment: %+v %v", ready, err)
	}
	state, err = store.Load()
	if err != nil || len(state.ApplicationResources) != 1 || state.ApplicationResources[0].ID != resource.ID {
		t.Fatal("atomic activation lost application grants", err)
	}
}
