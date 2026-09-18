package devtools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func metadataClient(t *testing.T) (*Client, map[string]string) {
	t.Helper()
	dir := t.TempDir()
	files := map[string]string{
		"catalog": filepath.Join(dir, "catalog.json"),
		"project": filepath.Join(dir, "project.json"),
		"list":    filepath.Join(dir, "list.json"),
		"inspect": filepath.Join(dir, "inspect.json"),
		"log":     filepath.Join(dir, "args.log"),
	}
	if err := os.WriteFile(files["catalog"], embeddedCatalog, 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "devtools.toml"), []byte("profile = \"fixture\"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	project := map[string]any{
		"schema_version": 1, "ok": true,
		"data": map[string]any{
			"item":  map[string]any{"profile": "fixture", "source": "file", "config_path": filepath.Join(dir, "devtools.toml"), "root": dir},
			"paths": map[string]any{"config": "/private/devtools/config", "data": "/private/devtools/data", "cache": "/private/devtools/cache"},
		},
	}
	list := map[string]any{
		"schema_version": 1, "ok": true,
		"data": map[string]any{"profile": "fixture", "items": []any{
			map[string]any{"name": "web", "exec": []string{"npm", "run", "dev"}, "inject": true, "env": "local", "serve": []string{"http"}},
		}},
	}
	inspect := map[string]any{
		"schema_version": 1, "ok": true,
		"data": map[string]any{"profile": "fixture", "item": map[string]any{
			"name": "web", "exec": []string{"npm", "run", "dev"}, "inject": true, "env": "local", "serve": []string{"http"},
			"bind": map[string]any{"API_PORT": map[string]any{"port": "http", "profile": "", "instance": "", "template": "http://127.0.0.1:PORT"}},
			"requirements": map[string]any{
				"tools": map[string]any{"node": map[string]any{"executable": "node", "version": ">=22", "version_args": []string{"--version"}}},
				"vars":  []string{"PUBLIC_URL"}, "secs": []string{"API_TOKEN"},
			},
			"ready": map[string]any{"exec": []string{"node", "scripts/ready.mjs"}, "timeout": "30s"},
		}},
	}
	for name, value := range map[string]any{"project": project, "list": list, "inspect": inspect} {
		raw, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(files[name], append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
	}
	script := filepath.Join(dir, "devtools")
	body := "#!/bin/sh\n" +
		"if [ \"$1\" = version ]; then printf '%s\\n' '{\"schema_version\":1,\"ok\":true,\"data\":{\"version\":\"0.17.0\",\"commit\":\"test\",\"protocol_version\":3}}'; exit 0; fi\n" +
		"if [ \"$1 $2\" = \"schema --all\" ]; then cat \"" + files["catalog"] + "\"; exit 0; fi\n" +
		"printf '%s\\n' \"$@\" > \"" + files["log"] + "\"\n" +
		"case \"$1 $2\" in\n" +
		"  'project inspect') cat \"" + files["project"] + "\" ;;\n" +
		"  'command list') cat \"" + files["list"] + "\" ;;\n" +
		"  'command inspect') cat \"" + files["inspect"] + "\" ;;\n" +
		"  *) exit 2 ;;\n" +
		"esac\n"
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	client, err := NewClient(script, dir, []string{"PATH=/usr/bin:/bin", "HOME=" + dir})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { client.Close() })
	return client, files
}

func TestMetadataReaderReturnsSanitizedProjectAndCommandDefinitions(t *testing.T) {
	client, _ := metadataClient(t)
	if err := os.Mkdir(filepath.Join(client.CWD, "nested"), 0700); err != nil {
		t.Fatal(err)
	}
	project, err := client.InspectProject(t.Context(), "nested")
	if err != nil {
		t.Fatal(err)
	}
	if project.Profile != "fixture" || project.Source != "file" || project.Root != "." || project.ConfigPath != "devtools.toml" {
		t.Fatalf("project = %#v", project)
	}
	raw, err := json.Marshal(project)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(raw), "/private/devtools/") || strings.Contains(string(raw), client.CWD) {
		t.Fatalf("project leaked host path: %s", raw)
	}

	catalog, err := client.ListCommands(t.Context(), ".")
	if err != nil {
		t.Fatal(err)
	}
	if catalog.Profile != "fixture" || len(catalog.Items) != 1 || catalog.Items[0].Name != "web" || catalog.Items[0].Exec[0] != "npm" {
		t.Fatalf("catalog = %#v", catalog)
	}

	detail, err := client.InspectCommand(t.Context(), ".", "web")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Profile != "fixture" || detail.Item.Name != "web" || detail.Item.Ready == nil || detail.Item.Ready.Timeout != "30s" {
		t.Fatalf("detail = %#v", detail)
	}
	if got := detail.Item.Requirements.Secs; len(got) != 1 || got[0] != "API_TOKEN" {
		t.Fatalf("secret requirements = %#v", got)
	}
	if detail.Item.Bind["API_PORT"].Template == nil || *detail.Item.Bind["API_PORT"].Template == "" {
		t.Fatalf("binding = %#v", detail.Item.Bind)
	}
}

func TestMetadataReaderConfinesDirectoriesAndRawAccess(t *testing.T) {
	client, _ := metadataClient(t)
	if _, err := client.Call(t.Context(), "project inspect", json.RawMessage([]byte("{\"dir\":\".\"}"))); err == nil || !strings.Contains(err.Error(), "typed adapter") {
		t.Fatalf("raw metadata call error = %v", err)
	}
	if _, err := client.InspectProject(t.Context(), "/tmp"); err == nil {
		t.Fatal("outside directory accepted")
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(client.CWD, "linked")); err != nil {
		t.Fatal(err)
	}
	if _, err := client.ListCommands(t.Context(), "linked"); err == nil {
		t.Fatal("symlinked directory accepted")
	}

	client, files := metadataClient(t)
	if err := os.Remove(filepath.Join(client.CWD, "devtools.toml")); err != nil {
		t.Fatal(err)
	}
	_ = os.Remove(files["log"])
	if _, err := client.ListCommands(t.Context(), "."); err == nil || !strings.Contains(err.Error(), "not present") {
		t.Fatalf("missing in-workspace project error = %v", err)
	}
	if _, err := os.Stat(files["log"]); !os.IsNotExist(err) {
		t.Fatal("project command ran while no in-workspace configuration existed")
	}
}

func TestMetadataReaderRejectsEscapingAndMismatchedResponses(t *testing.T) {
	client, files := metadataClient(t)
	var response map[string]any
	raw, err := os.ReadFile(files["project"])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	response["data"].(map[string]any)["item"].(map[string]any)["root"] = "/tmp"
	raw, _ = json.Marshal(response)
	if err := os.WriteFile(files["project"], raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.InspectProject(context.Background(), "."); err == nil {
		t.Fatal("escaping project root accepted")
	}

	client, files = metadataClient(t)
	raw, err = os.ReadFile(files["inspect"])
	if err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(raw, &response); err != nil {
		t.Fatal(err)
	}
	response["data"].(map[string]any)["item"].(map[string]any)["name"] = "other"
	raw, _ = json.Marshal(response)
	if err := os.WriteFile(files["inspect"], raw, 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := client.InspectCommand(context.Background(), ".", "web"); err == nil {
		t.Fatal("mismatched command metadata accepted")
	}
}
