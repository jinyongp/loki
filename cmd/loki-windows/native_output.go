package main

import (
	"fmt"
	"io"

	windowshost "loki/internal/host/windows"
	"loki/internal/progress"
)

func writeNativeProbe(probe windowshost.NativeProbe, stdout, stderr io.Writer) int {
	if probe.Stdout != "" {
		fmt.Fprintln(stdout, probe.Stdout)
	}
	if detail := progress.NonProgressText(probe.Stderr); detail != "" {
		fmt.Fprintln(stderr, detail)
	}
	if probe.ExitCode < 0 {
		return 1
	}
	return probe.ExitCode
}
