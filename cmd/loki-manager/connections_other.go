//go:build !windows

package main

import (
	"context"
	"fmt"
	"io"
	"loki/internal/management"
)

func runConnections(ctx context.Context, store management.Store, args []string, input io.Reader, out, diagnostics io.Writer) error {
	if len(args) > 1 && args[0] == "setup" && args[1] == "codex" {
		selected, err := store.ExecutionSelection()
		if err != nil {
			return err
		}
		if selected != nil && selected.Remote() {
			return connectCodexOnHost(ctx, store, selected, args[1:], out, diagnostics)
		}
		return connectCodex(store, args[1:], out, diagnostics)
	}
	if len(args) == 0 || len(args) == 1 && args[0] == "list" {
		return showClientConnections(store, out)
	}
	if len(args) == 2 && args[1] == "codex" && (args[0] == "status" || args[0] == "show" || args[0] == "remove") {
		if args[0] != "remove" {
			return showClientConnections(store, out)
		}
		config, err := defaultCodexConfig()
		if err != nil {
			return err
		}
		return disconnectCodex(config, out)
	}
	return fmt.Errorf("OpenAI managed tunnels use the Windows frontend; configure local Codex with loki connections setup codex")
}

func inspectManagedConnections(management.Store) ([]clientConnection, error) { return nil, nil }

func requireHostConnectionsDetached(management.Store) error { return nil }

func requireManagedHostDetached(management.Store, management.HostRecord) error { return nil }
