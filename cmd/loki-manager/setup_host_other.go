//go:build !windows

package main

import (
	"context"
	"fmt"
)

func setupWSLDistributions(context.Context) ([]string, error) {
	return nil, fmt.Errorf("WSL selection requires the Windows CLI")
}
