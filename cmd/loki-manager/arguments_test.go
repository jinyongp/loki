package main

import (
	"bytes"
	"slices"
	"strings"
	"testing"

	"loki/internal/management"
)

func TestIntegrationOptionsRetainValuesAfterToolName(t *testing.T) {
	actual := toolArguments([]string{"git", "--identity-name", "A Name", "--identity-email", "a@example.org", "--key-file", "/tmp/private key"})
	wanted := []string{"--identity-name", "A Name", "--identity-email", "a@example.org", "--key-file", "/tmp/private key", "--", "git"}
	if !slices.Equal(actual, wanted) {
		t.Fatalf("integration arguments lost their value association: %q", actual)
	}
	actual = toolArguments([]string{"github", "--config-file", "/tmp/public config", "--private-key-file", "/tmp/private key", "--personal-projects"})
	wanted = []string{"--config-file", "/tmp/public config", "--private-key-file", "/tmp/private key", "--personal-projects", "--", "github"}
	if !slices.Equal(actual, wanted) {
		t.Fatalf("GitHub arguments lost their value association: %q", actual)
	}
}

func TestRemoteWizardKeepsImportOnExecutionHost(t *testing.T) {
	if !remoteGitHubWizard([]string{"integrations", "setup", "github", "--personal-projects", "--no-browser"}) {
		t.Fatal("default remote setup must use the frontend callback")
	}
	if !remoteGitHubWizard([]string{"integrations", "setup", "github", "--personal-projects=false", "--no-browser=true"}) {
		t.Fatal("explicit boolean values must retain the frontend callback")
	}
	for _, args := range [][]string{{"integrations", "setup", "github", "--stdin"}, {"integrations", "setup", "github", "--config-file", "/host/config", "--private-key-file", "/host/key"}, {"integrations", "status", "github"}} {
		if remoteGitHubWizard(args) {
			t.Fatal("import/status must remain on the execution host")
		}
	}
}

func TestGitHubRelayRejectsAmbiguousDocumentsBeforeHostMutation(t *testing.T) {
	for _, input := range []string{`{}`, `{"setup":{"action":"begin"},"user":{"action":"status"}}`, `{"setup":{"action":"begin"},"secret":"hidden"}`, `{"setup":{"action":"begin"}} {}`, strings.Repeat("x", (1<<20)+1)} {
		var output bytes.Buffer
		store := management.Store{Root: t.TempDir()}
		if err := runGitHubSetupRelay(t.Context(), store, strings.NewReader(input), &output, &output); err == nil || output.Len() != 0 {
			t.Fatal("ambiguous relay input reached host execution")
		}
	}
}
