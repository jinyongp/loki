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

func TestCodexDisconnectPreservesUnrelatedAndUserManagedEntries(t *testing.T) {
	config := filepath.Join(t.TempDir(), "config.toml")
	user := "model = 'chosen-model'\n[mcp_servers.other]\ncommand = 'other'\n"
	owned := "# BEGIN Loki tools\n[mcp_servers.loki]\ncommand = 'loki'\n# END Loki tools\n"
	if err := os.WriteFile(config, []byte(user+owned), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	if err := disconnectCodex(config, &commandOutput{Writer: &out, json: true}); err != nil {
		t.Fatal(err)
	}
	if singleJSON(t, out.Bytes())["changed"] != true {
		t.Fatal(out.String())
	}
	if raw, err := os.ReadFile(config); err != nil || string(raw) != user {
		t.Fatalf("unrelated configuration changed: %q, %v", raw, err)
	}
	manual := user + "[mcp_servers.loki]\ncommand = 'my-own-loki'\n"
	if err := os.WriteFile(config, []byte(manual), 0600); err != nil {
		t.Fatal(err)
	}
	if err := disconnectCodex(config, io.Discard); err != nil {
		t.Fatal(err)
	}
	if raw, _ := os.ReadFile(config); string(raw) != manual {
		t.Fatal("user-managed connection was removed")
	}
}

func TestConnectCodexReplacesPreviousOwnedComposition(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("full tool composition requires Linux")
	}
	store := management.Store{Root: t.TempDir()}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	state.Config.Mode = tools.Full
	state.Config.Tools = []tools.Selection{{ID: "browser", Enabled: true}}
	artifact := tools.Artifact{Module: "browser", Release: management.Release, Target: management.LocalTarget(tools.Full), URL: "https://example.com/tool.zip", SHA256: strings.Repeat("a", 64), Bytes: 1, Format: "zip"}
	state.Installed["browser"] = management.Installation{Artifact: artifact, Manifest: tools.Manifest{Schema: 1, ID: "browser", Release: management.Release, Targets: []tools.Target{artifact.Target}, Tools: []string{}}}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	directory := t.TempDir()
	t.Setenv("CODEX_HOME", directory)
	config := filepath.Join(directory, "config.toml")
	user := "model = 'keep-model'\n[mcp_servers.other]\ncommand = 'keep-other'\n"
	old := user + codexBegin + "[mcp_servers.loki_browser]\ncommand = 'previous-loki'\n" + codexEnd
	if err := os.WriteFile(config, []byte(old), 0600); err != nil {
		t.Fatal(err)
	}
	for _, changed := range []bool{true, false} {
		var out bytes.Buffer
		if err := run(t.Context(), []string{"--root", store.Root, "connections", "setup", "codex", "--json"}, &out, io.Discard); err != nil {
			t.Fatal(err)
		}
		if singleJSON(t, out.Bytes())["changed"] != changed {
			t.Fatal("repeat connection was not idempotent", out.String())
		}
	}
	raw, err := os.ReadFile(config)
	if err != nil || !strings.HasPrefix(string(raw), user) || strings.Contains(string(raw), "[mcp_servers.loki_browser]") || strings.Count(string(raw), "[mcp_servers.loki]") != 1 {
		t.Fatalf("composition switch retained duplicate tools or changed user settings: %s, %v", raw, err)
	}
}

func TestConnectionsListInspectsConfigurationWithoutExposingSecrets(t *testing.T) {
	directory := t.TempDir()
	t.Setenv("CODEX_HOME", directory)
	raw := "api_key = 'private-not-for-output'\n[mcp_servers.loki]\ncommand = 'loki'\n"
	if err := os.WriteFile(filepath.Join(directory, "config.toml"), []byte(raw), 0600); err != nil {
		t.Fatal(err)
	}
	for _, command := range [][]string{{"connections", "list", "--json"}, {"status", "--json"}} {
		var out bytes.Buffer
		if err := run(t.Context(), append([]string{"--root", t.TempDir()}, command...), &out, io.Discard); err != nil {
			t.Fatal(err)
		}
		value := singleJSON(t, out.Bytes())
		connections := value["connections"].([]any)
		if connections[0].(map[string]any)["configured"] != true || strings.Contains(out.String(), "private-not-for-output") {
			t.Fatal("connection configuration was misreported or secret leaked", out.String())
		}
	}
}
