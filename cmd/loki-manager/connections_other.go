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
			return connectRemoteCodex(ctx, *selected, args[1:], out, diagnostics)
		}
		return connectCodex(store, args[1:], out, diagnostics)
	}
	if len(args) == 0 || len(args) == 1 && args[0] == "list" {
		return result(out, "Loki connections", map[string]any{"providers": []string{"codex"}})
	}
	return fmt.Errorf("OpenAI managed tunnels use the Windows frontend; configure local Codex with loki connections setup codex")
}

func requireHostConnectionsDetached(management.Store) error { return nil }
