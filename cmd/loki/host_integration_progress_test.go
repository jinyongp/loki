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
				if strings.Contains(stderr.String(), "[loki]") {
					t.Fatal("fast integration inspection produced internal progress")
				}
				if action == "status" && stderr.Len() != 0 {
					t.Fatalf("passive status produced progress: %s", &stderr)
				}
			})
		}
	}
}

func TestGlobalVerboseKeepsHostIntegrationJSONSeparate(t *testing.T) {
	store, _ := hostIntegrationStoreFixture(t)
	var stdout, stderr bytes.Buffer
	code := run([]string{"--verbose", "host", "integration", "doctor", "--state-root", store.Root, "--json", "github"}, &stdout, &stderr)
	if code != 0 || !json.Valid(stdout.Bytes()) || !strings.Contains(stderr.String(), "[loki]") {
		t.Fatalf("code=%d stdout=%s stderr=%s", code, &stdout, &stderr)
	}
}
