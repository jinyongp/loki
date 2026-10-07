//go:build !linux

package main

import (
	"context"
	"io"
	"loki/internal/management"
)

func privilegedMaintenance(context.Context, management.Store, []string, io.Reader, io.Writer, io.Writer) (bool, error) {
	return false, nil
}
