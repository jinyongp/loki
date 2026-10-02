package main

import (
	"bufio"
	"bytes"
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWindowsGitHubSetupRejectsAccountAndImportOptions(t *testing.T) {
	for _, option := range []string{"--account", "--account-type", "--manual", "--app-id", "--installation-id", "--repositories", "--config-file", "--private-key-file"} {
		var output, stderr bytes.Buffer
		if code := runWindowsGitHubSetup(context.Background(), "setup", []string{option, "example"}, &output, &stderr); code != 2 || !strings.Contains(stderr.String(), "flag provided but not defined") {
			t.Fatalf("setup accepted %s: code=%d stderr=%s", option, code, stderr.String())
		}
	}
}

func TestWindowsGitHubImportRequiresBothFiles(t *testing.T) {
	for _, args := range [][]string{nil, {"--config-file", "example.toml"}, {"--private-key-file", "example.pem"}} {
		var output, stderr bytes.Buffer
		if code := runWindowsGitHubSetup(context.Background(), "import", args, &output, &stderr); code != 2 || !strings.Contains(stderr.String(), "--config-file and --private-key-file are required") {
			t.Fatalf("import accepted incomplete input: code=%d stderr=%s", code, stderr.String())
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
