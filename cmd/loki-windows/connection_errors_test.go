package main

import (
	"bytes"
	"errors"
	"fmt"
	"io"
	"strings"
	"testing"

	windowshost "loki/internal/host/windows"
	"loki/internal/progress"
)

func TestConnectionErrorsKeepRawChecksBehindVerbose(t *testing.T) {
	failure := &windowshost.OpenAIDoctorError{
		Summary:             "Windows cannot reach the local Loki MCP endpoint (connection refused).",
		Endpoint:            "http://127.0.0.1:18765/mcp",
		LocalEndpointFailed: true,
		Details: []string{
			"mcp_server_reachable: dial tcp 127.0.0.1:18765: connectex: connection refused",
			"oauth_metadata: oauth discovery GET failed: connection refused",
		},
	}
	for _, verbose := range []bool{false, true} {
		t.Run(fmt.Sprint(verbose), func(t *testing.T) {
			var output bytes.Buffer
			var writer io.Writer = &output
			if verbose {
				writer = progress.WithVerbose(writer)
			}
			printConnectionError(writer, fmt.Errorf("start managed openai connection: %w", failure), "fixture-distro")
			message := output.String()
			for _, wanted := range []string{failure.Summary, failure.Endpoint, "loki connection show --distribution fixture-distro local"} {
				if !strings.Contains(message, wanted) {
					t.Fatalf("missing cause or recovery guidance %q: %s", wanted, message)
				}
			}
			if strings.Contains(message, "connectex") != verbose || strings.Contains(message, "oauth discovery") != verbose {
				t.Fatalf("detail visibility does not match verbose=%t: %s", verbose, message)
			}
			if !verbose && strings.Count(message, "connection refused") != 1 {
				t.Fatalf("default output repeated the underlying failure: %s", message)
			}
		})
	}
}

func TestConnectionErrorsPreserveOtherFailures(t *testing.T) {
	for _, err := range []error{
		errors.New("credential unavailable"),
		errors.Join(&windowshost.OpenAIDoctorError{Summary: "Verification failed."}, errors.New("cleanup failed")),
	} {
		var output bytes.Buffer
		printConnectionError(&output, err, "fixture-distro")
		if !strings.Contains(output.String(), err.Error()) || strings.Contains(output.String(), "connection show") {
			t.Fatalf("unrelated failure was hidden or received local endpoint guidance: %s", &output)
		}
	}
}
