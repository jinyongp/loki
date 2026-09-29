package main

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildWindowsGitHubConfigProducesRestrictedFragment(t *testing.T) {
	raw, err := buildWindowsGitHubConfig(
		123, "Example-Org", "organization", 456,
		"Repo,repo-two,repo",
	)
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, want := range []string{
		"github_app_id = 123",
		`github_api_version = "2026-03-10"`,
		`account = "example-org"`,
		`account_type = "organization"`,
		"installation_id = 456",
		`repositories = ["repo", "repo-two"]`,
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("config missing %q: %s", want, text)
		}
	}
	for _, invalid := range []struct {
		account, accountType, repositories string
	}{
		{"bad/name", "organization", "repo"},
		{"example-org", "owner", "repo"},
		{"example-org", "organization", "repo,../escape"},
		{"example-org", "organization", ""},
	} {
		if _, err = buildWindowsGitHubConfig(123, invalid.account, invalid.accountType, 456, invalid.repositories); err == nil {
			t.Fatalf("invalid GitHub config accepted: %#v", invalid)
		}
	}
}

func TestReadWindowsIntegrationFileRejectsDirectoryAndOversize(t *testing.T) {
	root := t.TempDir()
	if _, err := readWindowsIntegrationFile(root, 1024); err == nil {
		t.Fatal("directory accepted as integration file")
	}
	path := filepath.Join(root, "input.pem")
	if err := os.WriteFile(path, bytes.Repeat([]byte("x"), 65), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := readWindowsIntegrationFile(path, 64); err == nil {
		t.Fatal("oversized integration file accepted")
	}
	if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	raw, err := readWindowsIntegrationFile(path, 64)
	if err != nil || string(raw) != "fixture" {
		t.Fatalf("integration file=%q err=%v", raw, err)
	}
}

func TestReadWindowsIntegrationFileRejectsSymlinkWhenSupported(t *testing.T) {
	root := t.TempDir()
	target := filepath.Join(root, "target.pem")
	link := filepath.Join(root, "link.pem")
	if err := os.WriteFile(target, []byte("secret"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, link); err != nil {
		t.Skipf("Windows symlink creation unavailable: %v", err)
	}
	if _, err := readWindowsIntegrationFile(link, 64); err == nil {
		t.Fatal("reparse/symlink integration file accepted")
	}
}

func TestPromptWindowsValueTrimsAndRejectsEmpty(t *testing.T) {
	var output bytes.Buffer
	value, err := promptWindowsValue(bufio.NewReader(strings.NewReader("  fixture  \r\n")), &output, "Value")
	if err != nil || value != "fixture" || output.String() != "Value: " {
		t.Fatalf("value=%q output=%q err=%v", value, output.String(), err)
	}
	if _, err = promptWindowsValue(bufio.NewReader(strings.NewReader("\r\n")), &output, "Value"); err == nil {
		t.Fatal("empty prompt value accepted")
	}
}
