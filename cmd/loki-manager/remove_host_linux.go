package main

import (
	"context"
	"fmt"
	"io"
	"loki/internal/management"
	"os"
	"os/exec"
	"strconv"
)

func removeManagedHost(ctx context.Context, _ management.Store, selected management.ExecutionSelection, diagnostics io.Writer) error {
	if !selected.System || selected != management.SystemSelection(uint32(os.Getuid())) {
		return fmt.Errorf("only this login user's owned system host can be purged; use hosts remove to detach an external host")
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	command := exec.CommandContext(ctx, "sudo", binary, "_remove-system-host", "--owner-uid", strconv.Itoa(os.Getuid()))
	command.Stdin, command.Stdout, command.Stderr = os.Stdin, diagnostics, diagnostics
	return command.Run()
}
