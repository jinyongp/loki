package main

import (
	"context"
	"io"
	"os"
	"os/exec"

	"loki/internal/management"
)

func privilegedMaintenance(ctx context.Context, store management.Store, args []string, input io.Reader, out, diagnostics io.Writer) (bool, error) {
	if os.Geteuid() == 0 {
		return false, nil
	}
	binary, err := os.Executable()
	if err != nil {
		return true, err
	}
	auth := exec.CommandContext(ctx, "sudo", "-v")
	auth.Stdin, auth.Stdout, auth.Stderr = input, diagnostics, diagnostics
	if err := auth.Run(); err != nil {
		return true, err
	}
	argv := []string{"-n", binary, "--root", store.Root}
	if jsonOutput(out) {
		argv = append(argv, "--json")
	}
	command := exec.CommandContext(ctx, "sudo", append(argv, args...)...)
	command.Stdin, command.Stdout, command.Stderr = input, out, diagnostics
	return true, command.Run()
}
