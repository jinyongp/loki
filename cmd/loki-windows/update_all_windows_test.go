//go:build windows

package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestUpdateAllNativeHelpAndInvalidOptions(t *testing.T) {
	for _, args := range [][]string{{"--all", "--help"}, {"--all", "--distribution", "custom", "-h"}} {
		var out, errOut bytes.Buffer
		if code := runUpdate(t.Context(), args, &out, &errOut); code != 0 || !strings.Contains(out.String(), "loki update --all") || errOut.Len() != 0 {
			t.Fatalf("args=%v out=%s stderr=%s", args, out.String(), errOut.String())
		}
	}
	for _, args := range [][]string{{"--all", "--json"}, {"--distribution", "custom"}, {"--all", "--distribution", "../other"}} {
		var out, errOut bytes.Buffer
		if code := runUpdate(t.Context(), args, &out, &errOut); code != 2 || !strings.Contains(errOut.String(), "usage:") {
			t.Fatalf("args=%v code=%d stdout=%s stderr=%s", args, code, out.String(), errOut.String())
		}
	}
}
