//go:build !windows

package main

import (
	"fmt"
	"io"
)

func runWindowsCommand(_ []string, _ io.Writer, stderr io.Writer) int {
	fmt.Fprintln(stderr, "Windows Loki frontend commands are available only in the Windows build.")
	return 2
}
