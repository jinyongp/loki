package main

import (
	"bytes"
	"encoding/json"
	"strings"
	"testing"
)

func TestIntegrationInspectionsKeepJSONSeparateFromProgress(t *testing.T) {
	for _, name := range []string{"browser", "signing", "github"} {
		for _, action := range []string{"status", "doctor"} {
			t.Run(name+"/"+action, func(t *testing.T) {
				store, _ := hostIntegrationStoreFixture(t)
				var stdout, stderr bytes.Buffer
				code := runHostIntegration([]string{action, "--state-root", store.Root, "--json", name}, &stdout, &stderr)
				if code != 0 || !json.Valid(stdout.Bytes()) {
					t.Fatalf("code=%d stdout=%s stderr=%s", code, &stdout, &stderr)
				}
				if action == "doctor" && !strings.Contains(stderr.String(), "[loki]") {
					t.Fatal("doctor ran without progress")
				}
				if action == "status" && stderr.Len() != 0 {
					t.Fatalf("passive status produced progress: %s", &stderr)
				}
			})
		}
	}
}
