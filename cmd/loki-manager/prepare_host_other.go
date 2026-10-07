//go:build !windows

package main

import (
	"context"
	"fmt"
	"io"
	"loki/internal/management"
	"loki/internal/tools"
	"os"
	"strings"
)

func prepareManagedHost(ctx context.Context, frontend management.Store, diagnostics io.Writer) (*management.ExecutionSelection, error) {
	fmt.Fprintln(diagnostics, "Selected tools need a Linux execution host.")
	fmt.Fprint(diagnostics, "SSH destination (user@host; Enter cancels): ")
	address, err := readSetupLine(os.Stdin, 4096)
	if err != nil && err != io.EOF {
		return nil, err
	}
	address = strings.TrimSpace(address)
	if address == "" {
		return nil, fmt.Errorf("choose a Linux host with loki hosts prepare ssh --address USER@HOST, then retry setup")
	}
	selected := management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "ssh", Address: address}, Command: "loki"}
	if err := selected.Validate(); err != nil {
		return nil, err
	}
	if err := prepareSSHHost(ctx, frontend, selected, diagnostics, diagnostics); err != nil {
		return nil, err
	}
	return frontend.ExecutionSelection()
}
