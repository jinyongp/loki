//go:build windows

package main

import (
	"bytes"
	"testing"

	windowshost "loki/internal/host/windows"
)

func TestWriteNativeProbeFiltersRelayedProgressFromStderr(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := writeNativeProbe(windowshost.NativeProbe{
		ExitCode: 7,
		Stdout:   "machine-output",
		Stderr:   "[loki] Preparing update...\nreal failure\n[loki] Applying update...",
	}, &stdout, &stderr)
	if code != 7 {
		t.Fatalf("code=%d", code)
	}
	if got, want := stdout.String(), "machine-output\n"; got != want {
		t.Fatalf("stdout=%q want=%q", got, want)
	}
	if got, want := stderr.String(), "real failure\n"; got != want {
		t.Fatalf("stderr=%q want=%q", got, want)
	}
}
