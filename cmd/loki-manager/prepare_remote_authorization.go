package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"golang.org/x/term"
	"io"
	"loki/internal/management"
	"os"
)

func prepareRemoteAuthorization(ctx context.Context, selected management.ExecutionSelection, input io.Reader, diagnostics io.Writer, run remoteHostRunner) (bool, error) {
	call := func(action string, material []byte, response *githubRelayOutput) error {
		args := []string{action}
		if selected.Root != "" {
			args = append([]string{"--root", selected.Root}, args...)
		}
		return run(ctx, selected, args, bytes.NewReader(material), response, diagnostics)
	}
	var response githubRelayOutput
	if err := call("_host-authorization", nil, &response); err != nil {
		return false, fmt.Errorf("checking execution-host administrator authorization: %w", err)
	}
	var authority struct {
		Required *bool `json:"administrator_required"`
		Prepared bool  `json:"prepared"`
	}
	if response.overflow || json.Unmarshal(response.Bytes(), &authority) != nil || authority.Required == nil || *authority.Required && authority.Prepared {
		return false, fmt.Errorf("invalid execution-host authorization response")
	}
	if !*authority.Required {
		return authority.Prepared, nil
	}
	file, ok := input.(*os.File)
	if !ok || !term.IsTerminal(int(file.Fd())) {
		return false, fmt.Errorf("execution host needs administrator authentication; rerun loki setup in an interactive terminal")
	}
	password, err := setupPassword(ctx, file, diagnostics, "[INPUT REQUIRED] Execution host administrator password (input hidden; used once for host preparation): ")
	if err != nil {
		return false, err
	}
	defer clear(password)
	response.Reset()
	if err := call("_prepare-host-relay", password, &response); err != nil {
		return false, fmt.Errorf("execution-host administrator preparation failed; retry loki setup: %w", err)
	}
	var prepared management.ExecutionSelection
	if response.overflow || json.Unmarshal(response.Bytes(), &prepared) != nil || prepared.Validate() != nil || !prepared.System {
		return false, fmt.Errorf("execution host returned invalid system preparation metadata")
	}
	return true, nil
}

// The remote CLI remembers its protected local service. Following operations use
// that selection instead of asking a non-interactive relay to select a host.
func prepareRemoteFullHost(ctx context.Context, selected management.ExecutionSelection, input io.Reader, diagnostics io.Writer) error {
	return prepareRemoteFullHostWithRunner(ctx, selected, input, diagnostics, defaultRemotePreparationDependencies().run)
}

func prepareRemoteFullHostWithRunner(ctx context.Context, selected management.ExecutionSelection, input io.Reader, diagnostics io.Writer, run remoteHostRunner) error {
	if selected.Host.Kind != "wsl" && selected.Host.Kind != "ssh" {
		return nil
	}
	prepared, err := prepareRemoteAuthorization(ctx, selected, input, diagnostics, run)
	if err != nil {
		return err
	}
	if prepared {
		return nil
	}
	args := []string{"hosts", "prepare", "local"}
	if selected.Root != "" {
		args = append([]string{"--root", selected.Root}, args...)
	}
	return run(ctx, selected, args, input, diagnostics, diagnostics)
}
