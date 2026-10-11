package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"

	"loki/internal/management"
	"loki/internal/tools"
)

func reconcileToolActivation(ctx context.Context, store management.Store, diagnostics io.Writer) error {
	state, err := store.Load()
	if err != nil || state.Config.Mode != tools.Full {
		return err
	}
	enabled := false
	for _, selected := range state.Config.Tools {
		if selected.Enabled {
			enabled = true
			break
		}
	}
	if enabled {
		if err := management.PrepareEnvironment(ctx, os.Stdin, diagnostics); err != nil {
			return err
		}
	} else {
		deployments, err := store.Deployments()
		if err != nil || len(deployments) == 0 {
			return err
		}
	}
	backend, err := management.NewFullBackend(store, diagnostics)
	if err != nil {
		return err
	}
	if idle, ok := backend.(idleMaintenanceBackend); ok {
		if err := idle.RequireIdle(ctx); err != nil {
			return err
		}
	}
	if enabled {
		fmt.Fprintln(diagnostics, "Applying enabled tools to the running services...")
		_, err := store.ReconcileFull(ctx, backend)
		return err
	}
	fmt.Fprintln(diagnostics, "No tools enabled; stopping owned services...")
	return store.StopFull(ctx, backend)
}

func uninstallTool(ctx context.Context, store management.Store, id tools.ID, diagnostics io.Writer) (retErr error) {
	state, err := store.Load()
	if err != nil {
		return err
	}
	if _, exists := state.Installed[id]; !exists {
		return fmt.Errorf("tool %s is not installed on this host; run 'loki status'", id)
	}
	// Check dependencies before stopping services. Remove repeats this check
	// under the store lock, so a concurrent selection cannot bypass it.
	for other, installed := range state.Installed {
		for _, required := range installed.Manifest.Requires {
			if required == id {
				return fmt.Errorf("tool %s is required by %s; uninstall dependent tools first", id, other)
			}
		}
	}
	if state.Config.Mode == tools.Full {
		deployments, err := store.Deployments()
		if err != nil {
			return err
		}
		if len(deployments) == 0 {
			return store.Remove(id)
		}
		backend, err := management.NewFullBackend(store, diagnostics)
		if err != nil {
			return err
		}
		if idle, ok := backend.(idleMaintenanceBackend); ok {
			if err := idle.RequireIdle(ctx); err != nil {
				return err
			}
		}
		fmt.Fprintln(diagnostics, "Stopping owned services before removing tool programs...")
		if err := store.StopFull(ctx, backend); err != nil {
			return err
		}
		defer func() {
			if err := reconcileToolActivation(ctx, store, diagnostics); err != nil {
				retErr = errors.Join(retErr, fmt.Errorf("remaining tools need service recovery; run 'loki tools start': %w", err))
			}
		}()
	}
	return store.Remove(id)
}
