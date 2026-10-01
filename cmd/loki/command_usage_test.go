package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestSelectedHostCommandUsage(t *testing.T) {
	for _, test := range []struct {
		name string
		run  func(*bytes.Buffer, *bytes.Buffer) int
		want string
	}{
		{"ingress-allow", func(out, err *bytes.Buffer) int { return runHostIngress([]string{"allow"}, out, err) }, "usage: loki host ingress allow [--system] [--state-root PATH] [--json] [--interrupt-active-jobs] HOST"},
		{"ingress-list", func(out, err *bytes.Buffer) int { return runHostIngress([]string{"list", "extra"}, out, err) }, "usage: loki host ingress list [--system] [--state-root PATH] [--json]"},
		{"checkpoint-show", func(out, err *bytes.Buffer) int { return runCheckpoint([]string{"show"}, out, err) }, "usage: loki checkpoint show ID"},
		{"checkpoint-list", func(out, err *bytes.Buffer) int { return runCheckpoint([]string{"list", "extra"}, out, err) }, "usage: loki checkpoint list"},
	} {
		t.Run(test.name, func(t *testing.T) {
			var stdout, stderr bytes.Buffer
			if code := test.run(&stdout, &stderr); code != 2 || stdout.Len() != 0 || strings.TrimSpace(stderr.String()) != test.want {
				t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
			}
		})
	}
}
