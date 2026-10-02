package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestGitHubSetupRejectsAccountAndImportOptions(t *testing.T) {
	for _, option := range []string{"--account", "--account-type", "--config-file", "--private-key-file", "--manual", "--app-id", "--installation-id", "--repositories"} {
		var output, stderr bytes.Buffer
		if code := runHostGitHubSetup("setup", []string{option, "example", "github"}, &output, &stderr); code != 2 || !strings.Contains(stderr.String(), "flag provided but not defined") {
			t.Fatalf("setup accepted %s: code=%d stderr=%s", option, code, stderr.String())
		}
	}
}

func TestGitHubImportRequiresConfigurationAndKey(t *testing.T) {
	for _, args := range [][]string{{"github"}, {"--config-file", "example.toml", "github"}, {"--private-key-file", "example.pem", "github"}} {
		var output, stderr bytes.Buffer
		if code := runHostIntegration(append([]string{"import"}, args...), &output, &stderr); code != 2 || !strings.Contains(stderr.String(), "--config-file and --private-key-file are required") {
			t.Fatalf("invalid import accepted: code=%d stderr=%s", code, stderr.String())
		}
	}
}
