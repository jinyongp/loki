package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"loki/internal/management"
	"loki/internal/tools"
)

// Exercise the actual SSH command boundary without a network or host mutation.
func installSetupSSHFixture(t *testing.T, report string) string {
	t.Helper()
	if runtime.GOOS == "windows" {
		t.Skip("uses an isolated POSIX SSH fixture")
	}
	directory := t.TempDir()
	log := filepath.Join(directory, "calls")
	t.Setenv("LOKI_REVIEW_SSH_LOG", log)
	t.Setenv("LOKI_REVIEW_SSH_REPORT", report)
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$LOKI_REVIEW_SSH_LOG"
case "$*" in
  *"'tools' 'status'"*) printf '%s\n' "$LOKI_REVIEW_SSH_REPORT" ;;
  *"'_host-authorization'"*) printf '%s\n' '{"administrator_required":false}' ;;
esac
`
	if err := os.WriteFile(filepath.Join(directory, "ssh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func TestRemoteBrowserSetupRetainsFullHostAndStartsServices(t *testing.T) {
	log := installSetupSSHFixture(t, `{"target":{"os":"linux","arch":"amd64","mode":"full"},"tools":{}}`)
	selected := management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "ssh", Address: "fixture@host"}, Root: "/fixture-state", Command: "loki"}
	var out bytes.Buffer
	err := runRemoteSetup(t.Context(), management.Store{}, selected, setupOptions{selected: []tools.ID{"browser"}, mode: tools.ProjectHost}, strings.NewReader(""), &commandOutput{Writer: &out, json: true}, io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	result := singleJSON(t, out.Bytes())
	if result["mode"] != "full" || result["started"] != true {
		t.Fatal("frontend did not retain full mode", out.String())
	}
	raw, err := os.ReadFile(log)
	if err != nil || !strings.Contains(string(raw), "'--mode' 'full'") || !strings.Contains(string(raw), "'_prepare-environment'") || !strings.Contains(string(raw), "'tools' 'start'") {
		t.Fatalf("full browser setup skipped service startup: %s, %v", raw, err)
	}
}

func TestRememberedHostStatusHonorsRootOverride(t *testing.T) {
	log := installSetupSSHFixture(t, `{"target":{"os":"linux","arch":"amd64","mode":"full"},"tools":{}}`)
	root := t.TempDir()
	store := management.Store{Root: root}
	selected := management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "ssh", Address: "fixture@host"}, Root: "/saved-state", Command: "loki"}
	if err := store.SelectExecutionHost(selected); err != nil {
		t.Fatal(err)
	}
	t.Setenv("CODEX_HOME", t.TempDir())
	var out bytes.Buffer
	if err := run(t.Context(), []string{"--root", root, "status", "--json"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if singleJSON(t, out.Bytes())["state_root"] != root {
		t.Fatal("status displayed stale root", out.String())
	}
	raw, err := os.ReadFile(log)
	if err != nil || strings.Contains(string(raw), "/saved-state") || !strings.Contains(string(raw), root) {
		t.Fatalf("status probed stale root: %s, %v", raw, err)
	}
}

func TestMalformedRemoteStatusIsUnavailable(t *testing.T) {
	installSetupSSHFixture(t, `{}`)
	t.Setenv("CODEX_HOME", t.TempDir())
	var out bytes.Buffer
	err := run(t.Context(), []string{"--host", "ssh", "--address", "fixture@host", "status", "--json"}, &out, io.Discard)
	if err == nil || !strings.Contains(err.Error(), "missing tool state") || singleJSON(t, out.Bytes())["tools"] != nil {
		t.Fatalf("malformed response became valid state: %s, %v", out.String(), err)
	}
}
