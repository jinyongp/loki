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
	state.Config.Tools = []tools.Selection{{ID: "workspace", Enabled: true}}
	artifact := tools.Artifact{Module: "workspace", Release: management.Release, Target: management.LocalTarget(tools.Full), URL: "https://example.com/tool.zip", SHA256: strings.Repeat("a", 64), Bytes: 1, Format: "zip"}
	state.Installed["workspace"] = management.Installation{Artifact: artifact, Manifest: tools.Manifest{Schema: 1, ID: "workspace", Release: management.Release, Targets: []tools.Target{artifact.Target}, Tools: []string{}}}
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

func TestProjectConnectionUsesSelectedToolsWithoutProjectGrant(t *testing.T) {
	store := management.Store{Root: t.TempDir()}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	state.Config.Mode = tools.ProjectHost
	state.Config.Tools = []tools.Selection{{ID: "browser", Enabled: true}}
	artifact := tools.Artifact{Module: "browser", Release: management.Release, Target: management.LocalTarget(tools.ProjectHost), URL: "https://example.com/tool.zip", SHA256: strings.Repeat("a", 64), Bytes: 1, Format: "zip"}
	state.Installed["browser"] = management.Installation{Artifact: artifact, Manifest: tools.Manifest{Schema: 1, ID: "browser", Release: management.Release, Targets: []tools.Target{artifact.Target}}}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	config := filepath.Join(t.TempDir(), "config.toml")
	var probe bytes.Buffer
	if err := run(t.Context(), []string{"--root", store.Root, "_connection-state"}, &probe, io.Discard); err != nil {
		t.Fatal("execution-host probe requires a project grant", err)
	}
	if singleJSON(t, probe.Bytes())["target"].(map[string]any)["mode"] != "project-host" {
		t.Fatal(probe.String())
	}
	user := "model = 'keep-model'\n[mcp_servers.other]\ncommand = 'keep-other'\n"
	if err := os.WriteFile(config, []byte(user+codexBegin+"[mcp_servers.loki_browser]\ncommand = 'old'\n"+codexEnd), 0600); err != nil {
		t.Fatal(err)
	}
	for _, changed := range []bool{true, false} {
		var out bytes.Buffer
		if err := connectCodex(store, []string{"codex", "--config", config}, &commandOutput{Writer: &out, json: true}, io.Discard); err != nil {
			t.Fatal(err)
		}
		value := singleJSON(t, out.Bytes())
		if value["changed"] != changed || value["server"] != "loki" {
			t.Fatal(out.String())
		}
	}
	raw, err := os.ReadFile(config)
	if err != nil || !strings.HasPrefix(string(raw), user) || strings.Contains(string(raw), "loki_browser") || strings.Contains(string(raw), "--workspace") || !strings.Contains(string(raw), `"tools", "serve"`) || strings.Contains(string(raw), `"serve", "browser"`) || !strings.Contains(string(raw), `"--host", "local"`) {
		t.Fatalf("connection is not a generic Loki entry: %s, %v", raw, err)
	}
	workspace := t.TempDir()
	if err := connectCodex(store, []string{"codex", "--config", config, "--workspace", workspace}, io.Discard, io.Discard); err != nil {
		t.Fatal(err)
	}
	raw, _ = os.ReadFile(config)
	if !strings.Contains(string(raw), `"--workspace"`) {
		t.Fatal("explicit project grant missing", string(raw))
	}
	if err := connectCodex(store, []string{"codex", "--config", config, "--workspace", "relative"}, io.Discard, io.Discard); err == nil {
		t.Fatal("relative project grant accepted")
	}
	temporary := t.TempDir()
	for _, name := range []string{"TMPDIR", "TMP", "TEMP"} {
		t.Setenv(name, temporary)
	}
	if err := serve(t.Context(), store, nil, io.Discard); !os.IsNotExist(err) {
		t.Fatal("generic launcher did not reach the browser generation check", err)
	}
	if entries, err := os.ReadDir(temporary); err != nil || len(entries) != 0 {
		t.Fatal("failed browser startup leaked its temporary workspace", entries, err)
	}
	state.Config.Tools[0].Enabled = false
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	if err := connectCodex(store, []string{"codex", "--config", config}, io.Discard, io.Discard); err == nil {
		t.Fatal("disabled tool accepted")
	}
}

func TestConnectionPreviewIncludesOnlyInstalledEnabledTools(t *testing.T) {
	report := management.Report{Target: management.LocalTarget(tools.ProjectHost), Tools: map[tools.ID]tools.State{
		"browser":   {Installed: true, Enabled: true},
		"git":       {Installed: true},
		"workspace": {Enabled: true},
	}}
	description := setupConnectionDescription(report, "/config.toml")
	if !strings.Contains(description, "Loki tools: browser\n") || !strings.Contains(description, "private temporary directory") || !strings.Contains(description, "--workspace PATH") {
		t.Fatal(description)
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
