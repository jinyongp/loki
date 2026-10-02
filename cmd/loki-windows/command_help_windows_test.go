package main

import (
	"bytes"
	"context"
	"io"
	"strings"
	"testing"
)

func TestWindowsSelectedCommandHelpDoesNotAccessAppliance(t *testing.T) {
	for _, test := range []struct {
		name  string
		run   func(context.Context, []string, io.Writer, io.Writer) int
		args  []string
		code  int
		usage string
	}{
		{"integration-missing-name", runIntegration, []string{"status"}, 2, "usage: loki integration status"},
		{"integration-help", runIntegration, []string{"status", "--help"}, 0, "usage: loki integration status"},
		{"integration-flags-help", runIntegration, []string{"status", "--distribution", "test", "--help"}, 0, "usage: loki integration status"},
		{"integration-setup-help", runIntegration, []string{"setup", "signing", "--help"}, 0, "usage: loki integration setup signing"},
		{"integration-refresh-help", runIntegration, []string{"refresh", "--help"}, 0, "usage: loki integration refresh github"},
		{"integration-refresh-github-help", runIntegration, []string{"refresh", "github", "--help"}, 0, "usage: loki integration refresh github"},
		{"integration-refresh-missing-name", runIntegration, []string{"refresh"}, 2, "usage: loki integration refresh github"},
		{"integration-refresh-invalid-name", runIntegration, []string{"refresh", "signing"}, 2, "usage: loki integration refresh github"},
		{"update-help", runUpdate, []string{"apply", "--help"}, 0, "usage: loki update apply"},
		{"update-flags-help", runUpdate, []string{"apply", "--distribution", "test", "--help"}, 0, "usage: loki update apply"},
		{"connection-help", runConnection, []string{"show", "--help"}, 0, "usage: loki connection show"},
		{"connection-flags-help", runConnection, []string{"show", "--distribution", "test", "--help"}, 0, "usage: loki connection show"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := test.run(t.Context(), test.args, &stdout, &stderr); code != test.code {
				t.Fatalf("code=%d stdout=%s stderr=%s", code, &stdout, &stderr)
			}
			output, other := &stdout, &stderr
			if test.code != 0 {
				output, other = &stderr, &stdout
			}
			if !strings.Contains(output.String(), test.usage) || other.Len() != 0 || strings.Count(output.String(), "usage:") != 1 {
				t.Fatalf("stdout=%s stderr=%s", &stdout, &stderr)
			}
		})
	}
}
