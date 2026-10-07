//go:build windows

package main

import (
	"context"
	"os"

	windowshost "loki/internal/host/windows"
)

func protectTestCredentialFile(path string) error {
	data, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return (windowshost.WindowsFrontendPlatform{}).WriteProtectedAtomic(context.Background(), path, data)
}
