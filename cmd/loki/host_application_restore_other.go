//go:build !linux

package main

import (
	"fmt"
	"io"
)

func runHostApplicationRestore(_ []string, _ io.Writer, stderr io.Writer) int {
	fmt.Fprintln(stderr, "application restore requires its Linux deployment owner")
	return 1
}
