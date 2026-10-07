package main

import (
	"context"
	managedcommand "loki/internal/platform/command"
	"os/exec"
)

func newConnectionCommand(ctx context.Context, binary string, args ...string) *exec.Cmd {
	return managedcommand.New(ctx, binary, args...)
}
