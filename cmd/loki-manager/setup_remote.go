package main

import (
	"context"
	"fmt"
	"io"
	"slices"
	"strings"

	"loki/internal/management"
	"loki/internal/tools"
)

func runRemoteSetup(ctx context.Context, frontend management.Store, selected management.ExecutionSelection, options setupOptions, input io.Reader, out, diagnostics io.Writer) error {
	if options.catalog != "" {
		return fmt.Errorf("offline setup catalogs must be installed directly on the execution host; use loki tools install --catalog there")
	}
	if selected.Host.Kind == "ssh" && options.mode == tools.Full {
		if err := prepareSSHAuthorization(ctx, selected, input, diagnostics); err != nil {
			return err
		}
	}
	call := func(args ...string) error {
		if selected.Root != "" {
			args = append([]string{"--root", selected.Root}, args...)
		}
		relay, err := management.RelaySelection(ctx, selected, args)
		if err != nil {
			return err
		}
		relay.Stdin, relay.Stdout, relay.Stderr = input, diagnostics, diagnostics
		return relay.Run()
	}
	names := make([]string, len(options.selected))
	for i, id := range options.selected {
		names[i] = string(id)
	}
	args := []string{"setup", "--tools", strings.Join(names, ","), "--mode", string(options.mode), "--no-start"}
	if options.version != "" {
		args = append(args, "--version", options.version)
	} else {
		args = append(args, "--version", management.ManagerRelease)
	}
	if err := call(args...); err != nil {
		return fmt.Errorf("execution-host setup failed; retry loki setup to resume: %w", err)
	}
	if options.mode == tools.Full && !options.noStart {
		if err := call("_prepare-environment"); err != nil {
			return err
		}
		if slices.Contains(options.selected, tools.ID("github")) {
			if err := runSelectedGitHubWizard(ctx, selected, []string{"integrations", "setup", "github"}, input, diagnostics, diagnostics); err != nil {
				return err
			}
		} else if err := call("tools", "start"); err != nil {
			return err
		}
	}
	if options.client != "" {
		if err := connectRemoteCodex(ctx, selected, []string{options.client, "--workspace", options.workspace}, diagnostics, diagnostics); err != nil {
			return err
		}
	}
	return success(out, "Loki setup completed on the selected execution host.", map[string]any{"tools": options.selected, "mode": options.mode, "host": selected.Host, "started": options.mode == tools.Full && !options.noStart})
}
