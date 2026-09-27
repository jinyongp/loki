package windows

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"

	"loki/internal/host/connect"
)

type fakeConnectionHelpers struct {
	helper ManagedHelper
	calls  []string
	err    error
}

func (helpers *fakeConnectionHelpers) Ensure(_ context.Context, id, platform string) (ManagedHelper, error) {
	helpers.calls = append(helpers.calls, id+"|"+platform)
	if helpers.err != nil {
		return ManagedHelper{}, helpers.err
	}
	return helpers.helper, nil
}

type fakeConnectionStore struct {
	states map[string]ConnectionState
	roots  map[string]string
}

func newFakeConnectionStore() *fakeConnectionStore {
	return &fakeConnectionStore{states: map[string]ConnectionState{}, roots: map[string]string{}}
}

func connectionKey(distribution, provider string) string {
	return distribution + "|" + provider
}

func (store *fakeConnectionStore) ProviderRoot(distribution, provider string) (string, error) {
	key := connectionKey(distribution, provider)
	if root := store.roots[key]; root != "" {
		return root, nil
	}
	root := `C:\Users\alice\AppData\Local\Programs\Loki\connections\` + distribution + `\` + provider
	store.roots[key] = root
	return root, nil
}

func (store *fakeConnectionStore) Read(distribution, provider string) (ConnectionState, bool, error) {
	state, ok := store.states[connectionKey(distribution, provider)]
	return state, ok, nil
}

func (store *fakeConnectionStore) Write(_ context.Context, state ConnectionState) error {
	if err := validateConnectionState(state); err != nil {
		return err
	}
	store.states[connectionKey(state.Distribution, state.Provider)] = state
	return nil
}

func (store *fakeConnectionStore) List(distribution string) ([]ConnectionState, error) {
	var states []ConnectionState
	for _, state := range store.states {
		if state.Distribution == distribution {
			states = append(states, state)
		}
	}
	slices.SortFunc(states, func(left, right ConnectionState) int {
		return strings.Compare(left.Provider, right.Provider)
	})
	return states, nil
}

func (store *fakeConnectionStore) Remove(_ context.Context, distribution, provider string) error {
	delete(store.states, connectionKey(distribution, provider))
	return nil
}

type fakeConnectionTasks struct {
	calls []bool
	err   error
}

func (tasks *fakeConnectionTasks) Reconcile(_ context.Context, _ string, enabled bool) error {
	tasks.calls = append(tasks.calls, enabled)
	return tasks.err
}

type fakeConnectionAdapter struct {
	provider string
	helperID string
	calls    []string
	healthy  bool
	errAt    string
	last     ConnectionRuntimeContext
}

func (adapter *fakeConnectionAdapter) Provider() string { return adapter.provider }
func (adapter *fakeConnectionAdapter) HelperID() string { return adapter.helperID }

func (adapter *fakeConnectionAdapter) record(name string, runtime ConnectionRuntimeContext) error {
	adapter.calls = append(adapter.calls, name)
	adapter.last = runtime
	if adapter.errAt == name {
		return errors.New(name + " failed")
	}
	return nil
}

func (adapter *fakeConnectionAdapter) Setup(_ context.Context, runtime ConnectionRuntimeContext) error {
	return adapter.record("setup", runtime)
}

func (adapter *fakeConnectionAdapter) Start(_ context.Context, runtime ConnectionRuntimeContext) error {
	return adapter.record("start", runtime)
}

func (adapter *fakeConnectionAdapter) Status(_ context.Context, runtime ConnectionRuntimeContext) (ConnectionRuntimeStatus, error) {
	if err := adapter.record("status", runtime); err != nil {
		return ConnectionRuntimeStatus{}, err
	}
	return ConnectionRuntimeStatus{State: "running", Healthy: adapter.healthy}, nil
}

func (adapter *fakeConnectionAdapter) Stop(_ context.Context, runtime ConnectionRuntimeContext) error {
	return adapter.record("stop", runtime)
}

func (adapter *fakeConnectionAdapter) Remove(_ context.Context, runtime ConnectionRuntimeContext) error {
	return adapter.record("remove", runtime)
}

func connectionManagerFixture() (ConnectionManager, *fakeConnectionAdapter, *fakeConnectionHelpers, *fakeConnectionStore, *fakeConnectionTasks) {
	helper := connect.Helper{
		ID: "helper-one", Provider: "provider-one", Version: "1.2.3",
		Platform: "windows-amd64", Executable: "helper.exe",
	}
	helpers := &fakeConnectionHelpers{helper: ManagedHelper{
		Helper: helper, Root: `C:\helpers\helper-one`, ExecutablePath: `C:\helpers\helper-one\helper.exe`, Reused: true,
	}}
	store := newFakeConnectionStore()
	tasks := &fakeConnectionTasks{}
	adapter := &fakeConnectionAdapter{provider: "provider-one", helperID: "helper-one", healthy: true}
	manager := ConnectionManager{
		Helpers: helpers, Store: store, Tasks: tasks, Adapters: []RemoteConnectionAdapter{adapter},
		Platform: "windows-amd64",
	}
	return manager, adapter, helpers, store, tasks
}

func TestConnectionManagerSetupStopStartRemovePersistsEnablement(t *testing.T) {
	manager, adapter, helpers, store, tasks := connectionManagerFixture()
	distribution := "loki-mcp"

	if err := manager.Setup(t.Context(), distribution, "provider-one"); err != nil {
		t.Fatal(err)
	}
	state, ok, _ := store.Read(distribution, "provider-one")
	if !ok || !state.Enabled || state.HelperID != "helper-one" {
		t.Fatalf("state after setup=%+v present=%v", state, ok)
	}
	if got := tasks.calls; !reflect.DeepEqual(got, []bool{true}) {
		t.Fatalf("task calls after setup=%v", got)
	}
	if adapter.last.Root != `C:\Users\alice\AppData\Local\Programs\Loki\connections\loki-mcp\provider-one` ||
		adapter.last.Helper.ExecutablePath != `C:\helpers\helper-one\helper.exe` {
		t.Fatalf("runtime context=%+v", adapter.last)
	}

	if err := manager.Stop(t.Context(), distribution, "provider-one"); err != nil {
		t.Fatal(err)
	}
	state, _, _ = store.Read(distribution, "provider-one")
	if state.Enabled {
		t.Fatal("stop did not persist disabled state")
	}
	if got := tasks.calls; !reflect.DeepEqual(got, []bool{true, false}) {
		t.Fatalf("task calls after stop=%v", got)
	}

	if err := manager.Start(t.Context(), distribution, "provider-one"); err != nil {
		t.Fatal(err)
	}
	state, _, _ = store.Read(distribution, "provider-one")
	if !state.Enabled {
		t.Fatal("start did not persist enabled state")
	}
	if got := tasks.calls; !reflect.DeepEqual(got, []bool{true, false, true}) {
		t.Fatalf("task calls after start=%v", got)
	}

	if err := manager.Remove(t.Context(), distribution, "provider-one"); err != nil {
		t.Fatal(err)
	}
	if _, present, _ := store.Read(distribution, "provider-one"); present {
		t.Fatal("remove left managed connection state")
	}
	if got := tasks.calls; !reflect.DeepEqual(got, []bool{true, false, true, false}) {
		t.Fatalf("task calls after remove=%v", got)
	}
	if len(helpers.calls) != 4 {
		t.Fatalf("helper revalidation calls=%v", helpers.calls)
	}
	if got := adapter.calls; !reflect.DeepEqual(got, []string{
		"setup", "status", "stop", "start", "status", "remove",
	}) {
		t.Fatalf("adapter calls=%v", got)
	}
}

func TestConnectionManagerStartupRestoresOnlyEnabled(t *testing.T) {
	manager, adapter, helpers, store, _ := connectionManagerFixture()
	runtime, err := manager.runtime(t.Context(), "loki-mcp", adapter)
	if err != nil {
		t.Fatal(err)
	}
	enabled := stateFromRuntime(runtime, true)
	if err = store.Write(t.Context(), enabled); err != nil {
		t.Fatal(err)
	}
	adapter.calls = nil
	helpers.calls = nil

	if err = manager.ReconcileEnabled(t.Context(), "loki-mcp"); err != nil {
		t.Fatal(err)
	}
	if got := adapter.calls; !reflect.DeepEqual(got, []string{"start", "status"}) {
		t.Fatalf("enabled adapter calls=%v", got)
	}
	if len(helpers.calls) != 1 {
		t.Fatalf("enabled startup did not revalidate helper: %v", helpers.calls)
	}

	enabled.Enabled = false
	if err = store.Write(t.Context(), enabled); err != nil {
		t.Fatal(err)
	}
	adapter.calls = nil
	helpers.calls = nil
	if err = manager.ReconcileEnabled(t.Context(), "loki-mcp"); err != nil {
		t.Fatal(err)
	}
	if len(adapter.calls) != 0 || len(helpers.calls) != 0 {
		t.Fatalf("disabled connection was touched: adapter=%v helpers=%v", adapter.calls, helpers.calls)
	}
}

func TestConnectionManagerActivationRollsBackWhenStartupTaskFails(t *testing.T) {
	manager, adapter, _, store, tasks := connectionManagerFixture()
	tasks.err = errors.New("task failed")
	err := manager.Setup(t.Context(), "loki-mcp", "provider-one")
	if err == nil || !strings.Contains(err.Error(), "startup-task reconciliation failed") {
		t.Fatalf("err=%v", err)
	}
	if _, present, _ := store.Read("loki-mcp", "provider-one"); present {
		t.Fatal("failed activation left connection state present")
	}
	if got := adapter.calls; !reflect.DeepEqual(got, []string{"setup", "status", "stop"}) {
		t.Fatalf("adapter rollback calls=%v", got)
	}
}

func TestConnectionManagerRejectsStateBoundToDifferentHelper(t *testing.T) {
	manager, _, _, store, _ := connectionManagerFixture()
	store.states[connectionKey("loki-mcp", "provider-one")] = ConnectionState{
		SchemaVersion: 1, Distribution: "loki-mcp", Provider: "provider-one", Enabled: true,
		HelperID: "other-helper", HelperVersion: "1.2.3", HelperPlatform: "windows-amd64",
	}
	if err := manager.Start(t.Context(), "loki-mcp", "provider-one"); err == nil ||
		!strings.Contains(err.Error(), "does not match") {
		t.Fatalf("mismatched helper state accepted: %v", err)
	}
}

func TestConnectionManagerListsOnlyCompiledAdapters(t *testing.T) {
	manager, _, _, _, _ := connectionManagerFixture()
	manager.Adapters = append(manager.Adapters,
		&fakeConnectionAdapter{provider: "provider-two", helperID: "helper-two"},
	)
	providers, err := manager.Providers()
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(providers, []string{"provider-one", "provider-two"}) {
		t.Fatalf("providers=%v", providers)
	}
}

func TestConnectionManagerRemoveAllUsesOnlyLocalAdapterRemove(t *testing.T) {
	manager, adapter, _, store, tasks := connectionManagerFixture()
	runtime, err := manager.runtime(t.Context(), "loki-mcp", adapter)
	if err != nil {
		t.Fatal(err)
	}
	if err = store.Write(t.Context(), stateFromRuntime(runtime, true)); err != nil {
		t.Fatal(err)
	}
	adapter.calls = nil
	tasks.calls = nil
	if err = manager.RemoveAllForDistribution(t.Context(), "loki-mcp"); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(adapter.calls, []string{"remove"}) {
		t.Fatalf("remove all adapter calls=%v", adapter.calls)
	}
	if _, present, _ := store.Read("loki-mcp", "provider-one"); present {
		t.Fatal("remove all left provider state")
	}
	if !reflect.DeepEqual(tasks.calls, []bool{false}) {
		t.Fatalf("remove all task calls=%v", tasks.calls)
	}
}
