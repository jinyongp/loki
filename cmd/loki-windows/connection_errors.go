package main

import (
	"errors"
	"fmt"
	"io"

	windowshost "loki/internal/host/windows"
	"loki/internal/progress"
)

func printConnectionError(stderr io.Writer, err error, distribution string) {
	// Preserve all operation context and joined cleanup failures. Only the
	// verbose path expands the helper's redacted, per-check diagnostics.
	fmt.Fprintln(stderr, err)
	var failure *windowshost.OpenAIDoctorError
	if !errors.As(err, &failure) {
		return
	}
	if failure.Endpoint != "" {
		fmt.Fprintf(stderr, "  Endpoint: %s\n", failure.Endpoint)
	}
	if failure.LocalEndpointFailed {
		fmt.Fprintf(stderr, "  Check: loki connection show --distribution %s local\n", distribution)
	}
	if progress.Verbose(stderr) {
		fmt.Fprintln(stderr, "  Details:")
		for _, detail := range failure.Details {
			fmt.Fprintf(stderr, "    %s\n", detail)
		}
	} else {
		fmt.Fprintln(stderr, "  Use --verbose before the command for detailed diagnostics.")
	}
}
