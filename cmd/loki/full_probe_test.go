package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestFullSharingReadinessRequiresAnActualPublicOrigin(t *testing.T) {
	path := filepath.Join(t.TempDir(), "loki.toml")
	for _, check := range []struct {
		config string
		ready  bool
	}{
		{"root = \"/workspace\"\n", false},
		{"root = \"/workspace\"\npublic_hosts = [\"mcp.example.com\"]\nartifact_base_url = \"https://mcp.example.com/artifacts\"\n", true},
	} {
		if err := os.WriteFile(path, []byte(check.config), 0600); err != nil {
			t.Fatal(err)
		}
		if err := sharingConfigReady(path); (err == nil) != check.ready {
			t.Fatalf("sharing readiness = %v, want ready %v", err, check.ready)
		}
	}
}
