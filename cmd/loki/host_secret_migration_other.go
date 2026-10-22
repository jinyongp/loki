//go:build !linux

package main

import (
	"fmt"
	"io"
)

func runHostSecretMigration(_ []string, _ io.Writer, stderr io.Writer) int {
	fmt.Fprintln(stderr, "secret host migration requires its Linux deployment owner")
	return 1
}
