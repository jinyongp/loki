package main

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
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
				if strings.Contains(stderr.String(), "[loki]") != (action == "doctor" && name == "github") {
					t.Fatal("GitHub doctor start notice missing or passive inspection produced progress", stderr.String())
				}
				if action == "status" && stderr.Len() != 0 {
					t.Fatalf("passive status produced progress: %s", &stderr)
				}
			})
		}
	}
}

func TestSigningSetupAnnouncesKeyPreparationBeforeValidation(t *testing.T) {
	store, _ := hostIntegrationStoreFixture(t)
	key := filepath.Join(t.TempDir(), "invalid-key")
	if err := os.WriteFile(key, []byte("invalid signing key"), 0600); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := runHostIntegration([]string{
		"setup", "--state-root", store.Root, "--identity-name", "Signing Fixture",
		"--identity-email", "signing@example.test", "--key-file", key, "signing",
	}, &stdout, &stderr)
	if code != 1 || !strings.HasPrefix(stderr.String(), "[loki] Preparing and validating the SSH signing key and Git identity...\n") {
		t.Fatalf("code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stderr.String(), "invalid signing key") || stdout.Len() != 0 {
		t.Fatal("failed signing setup leaked key input or printed a success result")
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
