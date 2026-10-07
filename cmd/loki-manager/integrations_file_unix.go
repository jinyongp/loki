//go:build !windows

package main

import (
	"fmt"
	"os"
)

func privateIntegrationFile(_ string, info os.FileInfo) error {
	if info.Mode().Perm()&0077 != 0 {
		return fmt.Errorf("private keys require owner-only permissions")
	}
	return nil
}
