//go:build !linux

package main

import (
	"context"
	"fmt"
	"io"
	"loki/internal/management"
	"strings"
)

func systemCommand(_ context.Context, _ string, args []string, _ io.Reader, _, _ io.Writer) (bool, error) {
	if len(args) > 0 && strings.HasPrefix(args[0], "_system-") {
		return true, fmt.Errorf("system host requires Linux")
	}
	return false, nil
}
func prepareLocalSystemHost(context.Context, management.Store, io.Reader, io.Writer) (*management.ExecutionSelection, error) {
	return nil, fmt.Errorf("select a Linux execution host")
}
