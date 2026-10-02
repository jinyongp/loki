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

func TestHostPersonalProjectsSelectionIsExplicitAndActionBound(t *testing.T) {
	for _, action := range []string{"status", "doctor"} {
		var stderr bytes.Buffer
		defaults, _, err := parseHostIntegrationOptions(action, []string{"github"}, &stderr)
		if err != nil || defaults.PersonalProjects {
			t.Fatal("default inspection selected personal authorization", defaults, err)
		}
		selected, _, err := parseHostIntegrationOptions(action, []string{"--personal-projects", "github"}, &stderr)
		if err != nil || !selected.PersonalProjects {
			t.Fatal("personal authorization selection lost", selected, err)
		}
	}
	for _, action := range []string{"enable", "disable", "remove", "list"} {
		if _, _, err := parseHostIntegrationOptions(action, []string{"--personal-projects", "github"}, &bytes.Buffer{}); err == nil {
			t.Fatal("personal inspection accepted outside status/doctor", action)
		}
	}
	var stdout, stderr bytes.Buffer
	if code := runHostGitHubSetup("setup", []string{"--personal-projects", "--help"}, &stdout, &stderr); code != 0 || !strings.Contains(stderr.String(), "personal-projects") {
		t.Fatal("setup does not expose optional personal Projects", code, stderr.String())
	}
	stderr.Reset()
	if code := runHostGitHubSetup("setup", []string{"--personal-projects", "--stdin", "github"}, &stdout, &stderr); code != 2 {
		t.Fatal("personal authorization accepted with import transport", code)
	}
}
