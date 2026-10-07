//go:build !linux && !windows

package main

import (
	"context"
	"fmt"
	"io"
	"loki/internal/management"
)

func removeManagedHost(context.Context, management.Store, management.ExecutionSelection, io.Writer) error {
	return fmt.Errorf("external hosts can be detached with loki hosts remove; purge their owned data using the manager on that host")
}
