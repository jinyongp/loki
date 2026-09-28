package windows

import (
	"context"
	"errors"
	"fmt"
	"regexp"
	"slices"
	"strings"
)

const ConnectionStateSchemaVersion = 1

var connectionProviderPattern = regexp.MustCompile(`^[a-z0-9]+(?:-[a-z0-9]+)*$`)

type ConnectionState struct {
	SchemaVersion  int    `json:"schema_version"`
	Distribution   string `json:"distribution"`
	Provider       string `json:"provider"`
	Enabled        bool   `json:"enabled"`
	HelperID       string `json:"helper_id"`
	HelperVersion  string `json:"helper_version"`
	HelperPlatform string `json:"helper_platform"`
}

type ConnectionRuntimeContext struct {
	Distribution string
	Provider     string
	Root         string
	Helper       ManagedHelper
}

type ConnectionRuntimeStatus struct {
	State   string `json:"state"`
	Healthy bool   `json:"healthy"`
	Ready   bool   `json:"ready"`
	Detail  string `json:"detail,omitempty"`
}

type RemoteConnectionAdapter interface {
	Provider() string
	HelperID() string
	Setup(context.Context, ConnectionRuntimeContext) error
	Start(context.Context, ConnectionRuntimeContext) error
	Status(context.Context, ConnectionRuntimeContext) (ConnectionRuntimeStatus, error)
	Stop(context.Context, ConnectionRuntimeContext) error
	Remove(context.Context, ConnectionRuntimeContext) error
}

type ManagedHelperEnsurer interface {
	Ensure(context.Context, string, string) (ManagedHelper, error)
}

type ConnectionStateStore interface {
	ProviderRoot(string, string) (string, error)
	EnsureProviderRoot(context.Context, string, string) error
	Read(string, string) (ConnectionState, bool, error)
	Write(context.Context, ConnectionState) error
	List(string) ([]ConnectionState, error)
	Remove(context.Context, string, string) error
}

type ConnectionStartupTaskReconciler interface {
	Reconcile(context.Context, string, bool) error
}

type ManagedConnectionStatus struct {
	Configured bool                    `json:"configured"`
	State      ConnectionState         `json:"connection,omitempty"`
	Runtime    ConnectionRuntimeStatus `json:"runtime,omitempty"`
}

type ConnectionManager struct {
	Helpers  ManagedHelperEnsurer
	Store    ConnectionStateStore
	Tasks    ConnectionStartupTaskReconciler
	Adapters []RemoteConnectionAdapter
	Platform string
}

func (manager ConnectionManager) Providers() ([]string, error) {
	registry, err := manager.registry()
	if err != nil {
		return nil, err
	}
	providers := make([]string, 0, len(registry))
	for provider := range registry {
		providers = append(providers, provider)
	}
	slices.Sort(providers)
	return providers, nil
}

func (manager ConnectionManager) Setup(ctx context.Context, distribution, provider string) error {
	adapter, runtime, previous, hadPrevious, err := manager.prepareSetup(ctx, distribution, provider)
	if err != nil {
		return err
	}
	if err = adapter.Setup(ctx, runtime); err != nil {
		return manager.rollbackSetup(ctx, adapter, runtime, previous, hadPrevious,
			fmt.Errorf("setup managed %s connection: %w", provider, err))
	}
	status, err := adapter.Status(ctx, runtime)
	if err != nil {
		return manager.rollbackSetup(ctx, adapter, runtime, previous, hadPrevious,
			fmt.Errorf("verify managed %s connection after setup: %w", provider, err))
	}
	if !status.Healthy {
		return manager.rollbackSetup(ctx, adapter, runtime, previous, hadPrevious,
			fmt.Errorf("managed %s connection is not healthy after setup", provider))
	}
	state := stateFromRuntime(runtime, true)
	if err = manager.Store.Write(ctx, state); err != nil {
		return manager.rollbackSetup(ctx, adapter, runtime, previous, hadPrevious,
			fmt.Errorf("persist managed %s connection: %w", provider, err))
	}
	if err = manager.reconcileTask(ctx, distribution); err != nil {
		return manager.rollbackActivation(ctx, adapter, runtime, previous, hadPrevious, err)
	}
	return nil
}

func (manager ConnectionManager) Start(ctx context.Context, distribution, provider string) error {
	adapter, runtime, previous, hadPrevious, err := manager.prepare(ctx, distribution, provider)
	if err != nil {
		return err
	}
	if !hadPrevious {
		return fmt.Errorf("managed %s connection is not configured; run connect setup first", provider)
	}
	if err = validateStateAgainstRuntime(previous, runtime); err != nil {
		return err
	}
	if err = adapter.Start(ctx, runtime); err != nil {
		return fmt.Errorf("start managed %s connection: %w", provider, err)
	}
	status, err := adapter.Status(ctx, runtime)
	if err != nil {
		_ = adapter.Stop(ctx, runtime)
		return fmt.Errorf("verify managed %s connection after start: %w", provider, err)
	}
	if !status.Healthy {
		_ = adapter.Stop(ctx, runtime)
		return fmt.Errorf("managed %s connection is not healthy after start", provider)
	}
	next := previous
	next.Enabled = true
	if err = manager.Store.Write(ctx, next); err != nil {
		_ = adapter.Stop(ctx, runtime)
		return fmt.Errorf("persist enabled managed %s connection: %w", provider, err)
	}
	if err = manager.reconcileTask(ctx, distribution); err != nil {
		return manager.rollbackActivation(ctx, adapter, runtime, previous, true, err)
	}
	return nil
}

func (manager ConnectionManager) Stop(ctx context.Context, distribution, provider string) error {
	adapter, runtime, previous, hadPrevious, err := manager.prepare(ctx, distribution, provider)
	if err != nil {
		return err
	}
	if !hadPrevious {
		return fmt.Errorf("managed %s connection is not configured", provider)
	}
	if err = validateStateAgainstRuntime(previous, runtime); err != nil {
		return err
	}
	if err = adapter.Stop(ctx, runtime); err != nil {
		return fmt.Errorf("stop managed %s connection: %w", provider, err)
	}
	next := previous
	next.Enabled = false
	if err = manager.Store.Write(ctx, next); err != nil {
		return fmt.Errorf("persist disabled managed %s connection: %w", provider, err)
	}
	if err = manager.reconcileTask(ctx, distribution); err != nil {
		return fmt.Errorf("managed %s connection stopped but startup-task reconciliation failed: %w", provider, err)
	}
	return nil
}

func (manager ConnectionManager) Remove(ctx context.Context, distribution, provider string) error {
	adapter, runtime, previous, hadPrevious, err := manager.prepare(ctx, distribution, provider)
	if err != nil {
		return err
	}
	if !hadPrevious {
		return nil
	}
	if err = validateStateAgainstRuntime(previous, runtime); err != nil {
		return err
	}
	if err = adapter.Remove(ctx, runtime); err != nil {
		return fmt.Errorf("remove local managed %s connection: %w", provider, err)
	}
	if err = manager.Store.Remove(ctx, distribution, provider); err != nil {
		return fmt.Errorf("remove managed %s connection state: %w", provider, err)
	}
	if err = manager.reconcileTask(ctx, distribution); err != nil {
		return fmt.Errorf("managed %s connection removed but startup-task reconciliation failed: %w", provider, err)
	}
	return nil
}

func (manager ConnectionManager) Status(ctx context.Context, distribution, provider string) (ManagedConnectionStatus, error) {
	adapter, runtime, state, configured, err := manager.prepare(ctx, distribution, provider)
	if err != nil {
		return ManagedConnectionStatus{}, err
	}
	if !configured {
		return ManagedConnectionStatus{Configured: false}, nil
	}
	if err = validateStateAgainstRuntime(state, runtime); err != nil {
		return ManagedConnectionStatus{}, err
	}
	status, err := adapter.Status(ctx, runtime)
	if err != nil {
		return ManagedConnectionStatus{}, err
	}
	return ManagedConnectionStatus{Configured: true, State: state, Runtime: status}, nil
}

func (manager ConnectionManager) ValidateEnabled(ctx context.Context, distribution string) (int, error) {
	states, err := manager.Store.List(distribution)
	if err != nil {
		return 0, err
	}
	registry, err := manager.registry()
	if err != nil {
		return 0, err
	}
	enabled := 0
	for _, state := range states {
		if !state.Enabled {
			continue
		}
		adapter, ok := registry[state.Provider]
		if !ok {
			return 0, fmt.Errorf("enabled managed connection uses unsupported provider %q", state.Provider)
		}
		runtime, runtimeErr := manager.runtime(ctx, distribution, adapter)
		if runtimeErr != nil {
			return 0, runtimeErr
		}
		if err = validateStateAgainstRuntime(state, runtime); err != nil {
			return 0, err
		}
		enabled++
	}
	return enabled, nil
}

func (manager ConnectionManager) ReconcileEnabled(ctx context.Context, distribution string) error {
	states, err := manager.Store.List(distribution)
	if err != nil {
		return err
	}
	registry, err := manager.registry()
	if err != nil {
		return err
	}
	for _, state := range states {
		if !state.Enabled {
			continue
		}
		adapter, ok := registry[state.Provider]
		if !ok {
			return fmt.Errorf("enabled managed connection uses unsupported provider %q", state.Provider)
		}
		runtime, err := manager.runtime(ctx, distribution, adapter)
		if err != nil {
			return err
		}
		if err = validateStateAgainstRuntime(state, runtime); err != nil {
			return err
		}
		if err = adapter.Start(ctx, runtime); err != nil {
			return fmt.Errorf("restore enabled managed %s connection: %w", state.Provider, err)
		}
		status, statusErr := adapter.Status(ctx, runtime)
		if statusErr != nil || !status.Healthy {
			return errors.Join(
				fmt.Errorf("enabled managed %s connection did not become healthy", state.Provider),
				statusErr,
			)
		}
	}
	return nil
}

func (manager ConnectionManager) RemoveAllForDistribution(ctx context.Context, distribution string) error {
	states, err := manager.Store.List(distribution)
	if err != nil {
		return err
	}
	registry, err := manager.registry()
	if err != nil {
		return err
	}
	for _, state := range states {
		adapter, ok := registry[state.Provider]
		if !ok {
			return fmt.Errorf("cannot remove managed connection for unsupported provider %q", state.Provider)
		}
		runtime, runtimeErr := manager.runtime(ctx, distribution, adapter)
		if runtimeErr != nil {
			return runtimeErr
		}
		if err = validateStateAgainstRuntime(state, runtime); err != nil {
			return err
		}
		if err = adapter.Remove(ctx, runtime); err != nil {
			return fmt.Errorf("remove local managed %s connection during uninstall: %w", state.Provider, err)
		}
		if err = manager.Store.Remove(ctx, distribution, state.Provider); err != nil {
			return err
		}
	}
	if manager.Tasks != nil {
		if err = manager.Tasks.Reconcile(ctx, distribution, false); err != nil {
			return err
		}
	}
	return nil
}

func (manager ConnectionManager) prepare(
	ctx context.Context,
	distribution, provider string,
) (RemoteConnectionAdapter, ConnectionRuntimeContext, ConnectionState, bool, error) {
	adapter, err := manager.adapterFor(distribution, provider)
	if err != nil {
		return nil, ConnectionRuntimeContext{}, ConnectionState{}, false, err
	}
	state, present, err := manager.Store.Read(distribution, provider)
	if err != nil {
		return nil, ConnectionRuntimeContext{}, ConnectionState{}, false, err
	}
	if !present {
		return adapter, ConnectionRuntimeContext{}, ConnectionState{}, false, nil
	}
	runtime, err := manager.runtime(ctx, distribution, adapter)
	if err != nil {
		return nil, ConnectionRuntimeContext{}, ConnectionState{}, false, err
	}
	return adapter, runtime, state, true, nil
}

func (manager ConnectionManager) prepareSetup(
	ctx context.Context,
	distribution, provider string,
) (RemoteConnectionAdapter, ConnectionRuntimeContext, ConnectionState, bool, error) {
	adapter, err := manager.adapterFor(distribution, provider)
	if err != nil {
		return nil, ConnectionRuntimeContext{}, ConnectionState{}, false, err
	}
	previous, present, err := manager.Store.Read(distribution, provider)
	if err != nil {
		return nil, ConnectionRuntimeContext{}, ConnectionState{}, false, err
	}
	if err = manager.Store.EnsureProviderRoot(ctx, distribution, provider); err != nil {
		return nil, ConnectionRuntimeContext{}, ConnectionState{}, false, err
	}
	runtime, err := manager.runtime(ctx, distribution, adapter)
	if err != nil {
		if present {
			return nil, ConnectionRuntimeContext{}, ConnectionState{}, false, err
		}
		cleanupErr := manager.Store.Remove(ctx, distribution, provider)
		return nil, ConnectionRuntimeContext{}, ConnectionState{}, false, errors.Join(err, cleanupErr)
	}
	return adapter, runtime, previous, present, nil
}

func (manager ConnectionManager) adapterFor(distribution, provider string) (RemoteConnectionAdapter, error) {
	if manager.Helpers == nil || manager.Store == nil {
		return nil, errors.New("managed connection core is incomplete")
	}
	if err := ValidateDistributionName(distribution); err != nil {
		return nil, err
	}
	registry, err := manager.registry()
	if err != nil {
		return nil, err
	}
	adapter, ok := registry[provider]
	if !ok {
		return nil, fmt.Errorf("unsupported managed connection provider %q", provider)
	}
	return adapter, nil
}

func (manager ConnectionManager) runtime(
	ctx context.Context,
	distribution string,
	adapter RemoteConnectionAdapter,
) (ConnectionRuntimeContext, error) {
	provider := adapter.Provider()
	root, err := manager.Store.ProviderRoot(distribution, provider)
	if err != nil {
		return ConnectionRuntimeContext{}, err
	}
	helper, err := manager.Helpers.Ensure(ctx, adapter.HelperID(), manager.platform())
	if err != nil {
		return ConnectionRuntimeContext{}, err
	}
	return ConnectionRuntimeContext{
		Distribution: distribution,
		Provider:     provider,
		Root:         root,
		Helper:       helper,
	}, nil
}

func (manager ConnectionManager) registry() (map[string]RemoteConnectionAdapter, error) {
	registry := make(map[string]RemoteConnectionAdapter, len(manager.Adapters))
	for _, adapter := range manager.Adapters {
		if adapter == nil {
			return nil, errors.New("managed connection registry contains a nil adapter")
		}
		provider := strings.TrimSpace(adapter.Provider())
		helperID := strings.TrimSpace(adapter.HelperID())
		if !connectionProviderPattern.MatchString(provider) || helperID == "" {
			return nil, errors.New("managed connection adapter identity is invalid")
		}
		if _, duplicate := registry[provider]; duplicate {
			return nil, fmt.Errorf("managed connection provider %q is registered more than once", provider)
		}
		registry[provider] = adapter
	}
	return registry, nil
}

func (manager ConnectionManager) platform() string {
	value := strings.TrimSpace(manager.Platform)
	if value == "" {
		return FrontendArchitecture
	}
	return value
}

func (manager ConnectionManager) reconcileTask(ctx context.Context, distribution string) error {
	if manager.Tasks == nil {
		return nil
	}
	states, err := manager.Store.List(distribution)
	if err != nil {
		return err
	}
	enabled := false
	for _, state := range states {
		if state.Enabled {
			enabled = true
			break
		}
	}
	return manager.Tasks.Reconcile(ctx, distribution, enabled)
}

func (manager ConnectionManager) rollbackSetup(
	ctx context.Context,
	adapter RemoteConnectionAdapter,
	runtime ConnectionRuntimeContext,
	previous ConnectionState,
	hadPrevious bool,
	cause error,
) error {
	if hadPrevious {
		restoreRuntimeErr := adapter.Start(ctx, runtime)
		restoreStateErr := manager.Store.Write(ctx, previous)
		return errors.Join(cause, restoreRuntimeErr, restoreStateErr)
	}
	removeErr := adapter.Remove(ctx, runtime)
	stateErr := manager.Store.Remove(ctx, runtime.Distribution, runtime.Provider)
	return errors.Join(cause, removeErr, stateErr)
}

func (manager ConnectionManager) rollbackActivation(
	ctx context.Context,
	adapter RemoteConnectionAdapter,
	runtime ConnectionRuntimeContext,
	previous ConnectionState,
	hadPrevious bool,
	cause error,
) error {
	stopErr := adapter.Stop(ctx, runtime)
	var stateErr error
	if hadPrevious {
		stateErr = manager.Store.Write(ctx, previous)
	} else {
		stateErr = manager.Store.Remove(ctx, runtime.Distribution, runtime.Provider)
	}
	return errors.Join(fmt.Errorf("startup-task reconciliation failed: %w", cause), stopErr, stateErr)
}

func stateFromRuntime(runtime ConnectionRuntimeContext, enabled bool) ConnectionState {
	return ConnectionState{
		SchemaVersion:  ConnectionStateSchemaVersion,
		Distribution:   runtime.Distribution,
		Provider:       runtime.Provider,
		Enabled:        enabled,
		HelperID:       runtime.Helper.Helper.ID,
		HelperVersion:  runtime.Helper.Helper.Version,
		HelperPlatform: runtime.Helper.Helper.Platform,
	}
}

func validateConnectionState(state ConnectionState) error {
	if state.SchemaVersion != ConnectionStateSchemaVersion ||
		ValidateDistributionName(state.Distribution) != nil ||
		!connectionProviderPattern.MatchString(state.Provider) ||
		strings.TrimSpace(state.HelperID) == "" ||
		strings.TrimSpace(state.HelperVersion) == "" ||
		strings.TrimSpace(state.HelperPlatform) == "" {
		return errors.New("managed connection state is invalid")
	}
	return nil
}

func validateStateAgainstRuntime(state ConnectionState, runtime ConnectionRuntimeContext) error {
	if err := validateConnectionState(state); err != nil {
		return err
	}
	if state.Distribution != runtime.Distribution || state.Provider != runtime.Provider ||
		state.HelperID != runtime.Helper.Helper.ID ||
		state.HelperVersion != runtime.Helper.Helper.Version ||
		state.HelperPlatform != runtime.Helper.Helper.Platform {
		return errors.New("managed connection state does not match the release-bound adapter/helper")
	}
	return nil
}
