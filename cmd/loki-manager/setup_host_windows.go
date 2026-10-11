//go:build windows

package main

import (
	"context"

	windowshost "loki/internal/host/windows"
)

func setupWSLDistributions(ctx context.Context) ([]string, error) {
	return (windowshost.WSLClient{Runner: windowshost.ExecNativeRunner{}}).ListDistributions(ctx)
}
