package main

import (
	"bytes"
	"encoding/json"
	"fmt"
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
	t.Setenv("LOKI_REVIEW_CLI_VERSION", management.ManagerRelease)
	script := `#!/bin/sh
printf '%s\n' "$*" >> "$LOKI_REVIEW_SSH_LOG"
if [ "$LOKI_REVIEW_SSH_UNREACHABLE" = 1 ]; then exit 1; fi
case "$*" in
  *"'uname -s;"*) printf 'Linux\nx86_64\n/home/fixture\n' ;;
  *"'version'"*) printf 'loki %s\n' "$LOKI_REVIEW_CLI_VERSION" ;;
  *"'tools' 'status'"*) printf '%s\n' "$LOKI_REVIEW_SSH_REPORT" ;;
  *"'_connection-state'"*) printf '%s\n' "$LOKI_REVIEW_SSH_REPORT" ;;
  *"'_host-authorization'"*) printf '%s\n' '{"administrator_required":false}' ;;
esac
`
	if err := os.WriteFile(filepath.Join(directory, "ssh"), []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))
	return log
}

func TestLocalCodexConnectionStaysLocalAfterDefaultHostChanges(t *testing.T) {
	log := installSetupSSHFixture(t, `{}`)
	store := management.Store{Root: t.TempDir()}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	state.Config.Mode = tools.ProjectHost
	state.Config.Tools = []tools.Selection{{ID: "browser", Enabled: true}}
	artifact := tools.Artifact{Module: "browser", Release: management.Release, Target: management.LocalTarget(tools.ProjectHost), URL: "https://example.com/browser.zip", SHA256: strings.Repeat("a", 64), Bytes: 1, Format: "zip"}
	state.Installed["browser"] = management.Installation{Artifact: artifact, Manifest: tools.Manifest{Schema: 1, ID: "browser", Release: management.Release, Targets: []tools.Target{artifact.Target}}}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(t.TempDir(), "config.toml")
	if err := connectCodex(store, []string{"codex", "--config", config}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(config)
	if err != nil {
		t.Fatal(err)
	}
	var args []string
	for _, line := range strings.Split(string(raw), "\n") {
		if strings.HasPrefix(line, "args = ") {
			if err := json.Unmarshal([]byte(strings.TrimPrefix(line, "args = ")), &args); err != nil {
				t.Fatal(err)
			}
		}
	}
	if len(args) == 0 {
		t.Fatal("connection has no launch arguments")
	}
	selected := management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "ssh", Address: "fixture@host"}, Command: "loki", Root: "/home/fixture/.config/loki"}
	if err := store.SelectExecutionHost(selected); err != nil {
		t.Fatal(err)
	}
	// The deliberately missing local generation proves routing reached this
	// host's browser launcher. The SSH fixture must receive no request.
	if err := run(t.Context(), args, io.Discard, io.Discard); !os.IsNotExist(err) {
		t.Fatal("existing connection did not use its original local browser", err)
	}
	if calls, err := os.ReadFile(log); !os.IsNotExist(err) {
		t.Fatalf("local connection contacted the new default host: %s (%v)", calls, err)
	}
	if remembered, err := store.ExecutionSelection(); err != nil || remembered == nil || *remembered != selected {
		t.Fatal("pinned local launch changed the default host", remembered, err)
	}
}

func TestRemoteCodexConnectionDoesNotRequireProjectDirectory(t *testing.T) {
	installSetupSSHFixture(t, `{"target":{"os":"linux","arch":"amd64","mode":"project-host"},"tools":{"browser":{"installed":true,"enabled":true}}}`)
	config := filepath.Join(t.TempDir(), "config.toml")
	selected := management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "ssh", Address: "fixture@host"}, Command: "/home/fixture/loki", Root: "/home/fixture/.config/loki"}
	if err := connectRemoteCodex(t.Context(), selected, []string{"codex", "--config", config}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(config)
	if err != nil || !strings.Contains(string(raw), "[mcp_servers.loki]") || !strings.Contains(string(raw), `"tools", "serve"`) || strings.Contains(string(raw), `"--workspace"`) || !strings.Contains(string(raw), `"/home/fixture/loki"`) {
		t.Fatalf("incorrect remote connection: %s, %v", raw, err)
	}
}

func TestRememberedRemoteFullInstallationPreparesHostBeforeInstalling(t *testing.T) {
	for _, command := range [][]string{{"setup", "workspace"}, {"tools", "install", "workspace"}} {
		t.Run(strings.Join(command, " "), func(t *testing.T) {
			log := installSetupSSHFixture(t, `{"target":{"os":"linux","arch":"amd64","mode":"full"},"tools":{}}`)
			root := t.TempDir()
			store := management.Store{Root: root}
			selected := management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "ssh", Address: "fixture@host"}, Root: "/fixture-state", Command: "loki"}
			if err := store.SelectExecutionHost(selected); err != nil {
				t.Fatal(err)
			}
			var out, diagnostics bytes.Buffer
			if err := run(t.Context(), append([]string{"--root", root}, command...), &out, &diagnostics); err != nil {
				t.Fatalf("full setup stalled: %v\n%s", err, diagnostics.String())
			}
			raw, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			calls := string(raw)
			authorization := strings.Index(calls, "'_host-authorization'")
			prepare := strings.Index(calls, "'hosts' 'prepare' 'local'")
			operation := "'setup' '--tools' 'workspace'"
			if command[0] == "tools" {
				operation = "'tools' 'install'"
			}
			install := strings.Index(calls, operation)
			if authorization < 0 || prepare <= authorization || install <= prepare {
				t.Fatalf("wrong preparation order: %s", calls)
			}
			if strings.Contains(diagnostics.String(), "choose an execution host first") {
				t.Fatal("asked remote process to select another host")
			}
			actual, err := store.ExecutionSelection()
			if err != nil || actual == nil || actual.Host != selected.Host || actual.Command != selected.Command {
				t.Fatal("remembered host changed", actual, err)
			}
		})
	}
}

func TestInvalidRemoteInstallationDoesNotPrepareHost(t *testing.T) {
	log := installSetupSSHFixture(t, `{}`)
	root := t.TempDir()
	selected := management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "ssh", Address: "fixture@host"}, Command: "loki"}
	if err := (management.Store{Root: root}).SelectExecutionHost(selected); err != nil {
		t.Fatal(err)
	}
	for _, command := range [][]string{{"setup", "unknown-tool"}, {"tools", "install", "unknown-tool"}} {
		if err := run(t.Context(), append([]string{"--root", root}, command...), io.Discard, io.Discard); err == nil {
			t.Fatal("invalid request accepted", command)
		}
	}
	if _, err := os.Stat(log); !os.IsNotExist(err) {
		t.Fatal(fmt.Sprintf("invalid input contacted remote host: %v", err))
	}
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
