package secret

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"loki/internal/policy"
)

func TestPython0471SecretDifferential(t *testing.T) {
	python := os.Getenv("LOKI_REFERENCE_PYTHON")
	if python == "" {
		python = "../../.tmp/python-baseline/bin/python"
	}
	python, err := filepath.Abs(python)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = os.Stat(python); err != nil {
		t.Skip("isolated Python reference unavailable")
	}
	c := fixture(t)
	reference, err := os.MkdirTemp(t.TempDir(), "secret-reference-")
	if err != nil {
		t.Fatal(err)
	}
	cases := referenceCases(t)
	cmd := exec.CommandContext(t.Context(), python, "testdata/python_reference.py", c.Projects.WorkspaceRoot, reference)
	cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "LANG=C.UTF-8", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_CONFIG_NOSYSTEM=1"}
	cmd.Stdin = bytes.NewReader(encoded(t, cases))
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	output, err := cmd.Output()
	if err != nil {
		t.Fatalf("Python reference %v: %s", err, stderr.String())
	}
	var expected []map[string]any
	if err = json.Unmarshal(output, &expected); err != nil {
		t.Fatalf("reference response %v", err)
	}
	if len(expected) != len(cases) {
		t.Fatal("missing reference cases")
	}
	for i, tc := range cases {
		value, err := runReference(t, c, tc)
		actual := map[string]any{"result": value}
		if err != nil {
			actual = map[string]any{"error": err.Error()}
		}
		var normalized map[string]any
		if err = json.Unmarshal(encoded(t, actual), &normalized); err != nil {
			t.Fatal(err)
		}
		if !reflect.DeepEqual(normalized, expected[i]) {
			t.Errorf("case %d %v\nGo: %v\nPython: %v", i, tc, normalized, expected[i])
		}
	}
	t.Logf("Compared %d secret/action/workflow/command cases with Python 0.47.1", len(cases))
}
func referenceCases(t *testing.T) []map[string]any {
	t.Helper()
	cases := []map[string]any{}
	for _, overrides := range []map[string]any{
		{}, {"secrets": []string{"TOKEN"}, "all_secrets": false}, {"command": []string{}}, {"command": []string{"sh", "script.sh"}},
		{"cwd": "../outside"}, {"cwd": "/workspace/project"}, {"secrets": nil}, {"all_secrets": "yes"}, {"secrets": []string{"TOKEN"}}, {"all_secrets": false},
		{"secrets": []string{"UNKNOWN"}, "all_secrets": false}, {"secrets": []string{"TOKEN", "TOKEN"}, "all_secrets": false},
		{"required_secrets": "TOKEN"}, {"required_secrets": []string{"MISSING"}}, {"required_secrets": []string{"TOKEN", "TOKEN"}},
		{"secrets": []string{"PUBLIC_API"}, "all_secrets": false}, {"timeout_seconds": 0}, {"timeout_seconds": true}, {"timeout_seconds": 1.5},
		{"max_output_bytes": 4095}, {"materialize_env_file": "bad name"}, {"materialize_env_path": "secret.env"},
		{"materialize_env_file": "ENV_FILE", "materialize_env_path": "../bad"}, {"docker_access": 1},
		{"dynamic_port": nil}, {"dynamic_port": map[string]any{"preferred": 8765, "environment": "PORT"}},
		{"dynamic_port": map[string]any{"preferred": true, "environment": "PORT"}},
		{"dynamic_port": map[string]any{"preferred": 41280, "environment": "bad name"}},
		{"dynamic_port": map[string]any{"preferred": 41280, "environment": "PORT", "extra": true}},
		{"command": []string{"pnpm", "dev", "{LOKI_PORT}", "{LOKI_PORT}"}},
		{"command": []string{"pnpm", "dev"}, "dynamic_port": nil, "local_callback": true}, {"local_callback": "yes"},
		{"public_environment": []string{"PUBLIC_API", "PUBLIC_API"}}, {"public_environment": []string{"bad name"}},
		{"preview_environment": []string{}}, {"preview_environment": map[string]string{"OTHER": "/api"}}, {"preview_environment": map[string]string{"PUBLIC_API": "/api/../"}},
		{"singleton": 1}, {"lock_probe": ""}, {"lock_probe": "../lock"}, {"lock_probe": "/tmp/lock"},
	} {
		a := action("project")
		for key, value := range overrides {
			a[key] = value
		}
		cases = append(cases, map[string]any{"kind": "action", "value": a, "available": []string{"TOKEN", "PUBLIC_API"}})
	}
	for _, source := range []string{"# fixture\nexport TOKEN='synthetic'\nEMPTY=\n", "VALUE=\"line\\n한글\"\n", "A=one # literal\nA=two\n", "A=x\r\nB=y\u2028C=z", "# empty", "INVALID", "A=\"\\q\"", "BAD NAME=value"} {
		cases = append(cases, map[string]any{"kind": "dotenv", "value": source})
	}
	for _, argv := range [][]string{
		{"node", "script.js"}, {"node", "--eval", "fixture"}, {"python", "script.py"}, {"python3", "-m", "module"}, {"go", "env", "GOPATH"}, {"go", "env", "-w", "GOPATH=fixture"},
		{"rustup", "run", "stable", "cargo"}, {"find", ".", "-exec", "pwd", ";"}, {"fd", "--exec=cat"}, {"actionlint", "-shellcheck=other"},
		{"git", "status", "--short"}, {"git", "-c", "core.hooksPath=fixture", "status"}, {"git", "commit", "--no-gpg-sign"}, {"git", "commit-tree", "HEAD^{tree}"}, {"git", "reset", "HEAD"},
		{"git", "checkout", "--", "file"}, {"git", "config", "--get", "user.name"}, {"git", "config", "--show-origin", "--get", "commit.template"}, {"git", "config", "--get", "core.hooksPath"}, {"git", "config", "--get", "--get-all", "user.name"},
		{"git", "push", "--force"}, {"git", "submodule", "foreach", "pwd"}, {"gh", "api", "user"}, {"gh", "auth", "status"}, {"gh", "auth", "token"}, {"gh", "repo", "delete"}, {"gh", "secret", "list"},
		{"task", "list"}, {"task", "rc.hooks=on", "list"}, {"task", "purge"},
		{"hyperfine", "--version"}, {"hyperfine", "--shell=none", "go test ./..."}, {"hyperfine", "-N", "git config --get user.name"}, {"hyperfine", "-N", "rg 'quoted value'"},
		{"hyperfine", "go test ./..."}, {"hyperfine", "-N", "--prepare", "unsafe", "go test"}, {"hyperfine", "-N", "--warmup"}, {"hyperfine", "-N", "sh fixture.sh"}, {"hyperfine", "-N", "go env -w GOPATH=fixture"}, {"hyperfine", "-N", "rg 'unclosed"},
	} {
		cases = append(cases, map[string]any{"kind": "exec", "executable": argv[0], "arguments": argv[1:]})
	}
	for _, doc := range []any{
		map[string]any{"version": 1, "profiles": map[string]any{}},
		map[string]any{"version": 1, "profiles": map[string]any{}, "future": map[string]any{"keep": true}},
		map[string]any{"version": 2, "profiles": map[string]any{}}, map[string]any{"version": 1, "profiles": nil},
		map[string]any{"version": 1, "profiles": map[string]any{"bad name": map[string]any{}}},
		map[string]any{"version": 1, "profiles": map[string]any{}, "projects": []any{}},
	} {
		cases = append(cases, map[string]any{"kind": "document", "value": doc})
	}
	operations := []map[string]any{
		{"operation": "init"}, {"operation": "list_profiles"}, {"operation": "get_profile", "profile": "missing"},
		{"operation": "profile_create", "profile": "web"}, {"operation": "profile_create", "profile": "web"},
		{"operation": "import_env", "profile": "web", "values": map[string]string{"TOKEN": "", "PUBLIC_API": "http://127.0.0.1:41280"}},
		{"operation": "action_set", "profile": "web", "action_name": "dev", "action": action("project")},
		{"operation": "get_profile", "profile": "web"}, {"operation": "secret_generate", "profile": "web", "secret": "TOKEN", "bytes": 32},
		{"operation": "secret_generate", "profile": "web", "secret": "TOKEN", "bytes": 32}, {"operation": "get_profile", "profile": "web"},
		{"operation": "secret_remove", "profile": "web", "secret": "TOKEN"},
		{"operation": "project_status", "cwd": "project"}, {"operation": "project_register", "cwd": "project"}, {"operation": "project_register", "cwd": "feature", "name": "renamed"},
		{"operation": "project_set_workflow", "cwd": "project", "workflow": "development", "steps": [][]string{{"web", "dev"}}, "required_secrets": map[string][]string{"web": {"TOKEN"}}, "timeout_seconds": 3600},
		{"operation": "project_status", "cwd": "feature"}, {"operation": "project_workflow", "cwd": "feature", "workflow": "development"},
		{"operation": "profile_remove", "profile": "web"}, {"operation": "action_remove", "profile": "web", "action_name": "dev"},
		{"operation": "project_remove_workflow", "cwd": "project", "workflow": "development"},
		{"operation": "action_remove", "profile": "web", "action_name": "dev"}, {"operation": "secret_remove", "profile": "web", "secret": "TOKEN"},
		{"operation": "public_value_set", "profile": "web", "secret": "PUBLIC_API", "value": "https://example.invalid"},
		{"operation": "list_profiles"}, {"operation": "profile_remove", "profile": "web"}, {"operation": "project_unregister", "cwd": "feature"}, {"operation": "project_status", "cwd": "project"},
	}
	for _, r := range operations {
		cases = append(cases, map[string]any{"kind": "operation", "request": r})
	}
	for _, override := range []any{nil, "project", "feature", "other", ".", "missing", "", "../outside", "/workspace/project", "project\\subdir"} {
		cases = append(cases, map[string]any{"kind": "action_cwd", "registered": "project", "override": override})
	}
	for _, value := range []string{"", "http://127.0.0.1:41280/v1///", "http://LOCALHOST:41280/api", "http://[::1]:41280/v1%20test", "http://0.0.0.0", "http://localhost:0/", "https://example.invalid/api", "https://localhost:41280", "http://name:password@localhost:41280", "http://@localhost:41280", "http://localhost:41280/?query=value", "http://localhost:41280/#fragment"} {
		cases = append(cases, map[string]any{"kind": "preview_bindings", "policy": action("project"), "values": map[string]string{"PUBLIC_API": value}})
	}
	for _, routes := range []map[string]string{{}, {"PUBLIC_API": "/api", "OTHER": "/api"}} {
		a := action("project")
		a["public_environment"], a["preview_environment"] = []string{"PUBLIC_API", "OTHER"}, routes
		cases = append(cases, map[string]any{"kind": "preview_bindings", "policy": a, "values": map[string]string{"PUBLIC_API": "http://127.0.0.1:41280/v1", "OTHER": "http://localhost:49999"}})
	}
	base := "https://loki-00000000000000000000000000000000.streamliner.im"
	for _, values := range []map[string]string{{}, {"PUBLIC_API": base + "/api"}, {"PUBLIC_ORIGIN": base}, {"UNKNOWN": base}, {"PUBLIC_API": "http://127.0.0.1:41280"}, {"PUBLIC_API": base + ".example.invalid"}, {"PUBLIC_API": base + "?q=fixture"}, {"PUBLIC_API": base + "#fragment"}, {"PUBLIC_API": base + ":443"}} {
		cases = append(cases, map[string]any{"kind": "public_environment", "policy": action("project"), "values": values})
	}
	return cases
}
func runReference(t *testing.T, c Controller, tc map[string]any) (any, error) {
	t.Helper()
	switch tc["kind"] {
	case "action":
		a, err := decodeObject(encoded(t, tc["value"]))
		if err != nil {
			return nil, err
		}
		available := map[string]any{}
		for _, name := range tc["available"].([]string) {
			available[name] = "fixture"
		}
		return true, validateAction(a, available)
	case "document":
		return true, Validate(encoded(t, tc["value"]))
	case "dotenv":
		return ParseDotenv(tc["value"].(string))
	case "exec":
		return true, policy.ValidateExec(tc["executable"].(string), tc["arguments"].([]string))
	case "action_cwd":
		var override *string
		if v, ok := tc["override"].(string); ok {
			override = &v
		}
		cwd, err := actionCWD(t.Context(), c.Projects, tc["registered"].(string), override)
		if err != nil {
			return nil, err
		}
		relative, err := filepath.Rel(c.Projects.WorkspaceRoot, cwd)
		return filepath.ToSlash(filepath.Join("/workspace", relative)), err
	case "preview_bindings", "public_environment":
		var a ActionPolicy
		if err := json.Unmarshal(encoded(t, tc["policy"]), &a); err != nil {
			t.Fatal(err)
		}
		p := ActionPlan{Policy: a, previewValues: tc["values"].(map[string]string)}
		if tc["kind"] == "preview_bindings" {
			return p.PreviewBindings()
		}
		return p.PublicEnvironment(tc["values"].(map[string]string), "streamliner.im", false)
	case "operation":
		r := tc["request"].(map[string]any)
		str := func(key string) string { v, _ := r[key].(string); return v }
		ctx := t.Context()
		switch r["operation"] {
		case "init":
			return c.Initialize(ctx)
		case "list_profiles":
			return c.Profiles(ctx)
		case "get_profile":
			return c.Profile(ctx, str("profile"))
		case "profile_create":
			return c.CreateProfile(ctx, str("profile"))
		case "profile_remove":
			return c.RemoveProfile(ctx, str("profile"))
		case "import_env":
			return c.ImportValues(ctx, str("profile"), r["values"].(map[string]string))
		case "secret_set", "public_value_set":
			return c.SetSecret(ctx, str("profile"), str("secret"), str("value"), r["operation"] == "public_value_set")
		case "secret_generate":
			return c.Generate(ctx, str("profile"), str("secret"), r["bytes"].(int))
		case "secret_remove":
			return c.RemoveSecret(ctx, str("profile"), str("secret"))
		case "action_set":
			return c.SetAction(ctx, str("profile"), str("action_name"), encoded(t, r["action"]))
		case "action_remove":
			return c.RemoveAction(ctx, str("profile"), str("action_name"))
		case "project_register":
			var name *string
			if v, ok := r["name"].(string); ok {
				name = &v
			}
			return c.Register(ctx, str("cwd"), name)
		case "project_unregister":
			return c.Unregister(ctx, str("cwd"))
		case "project_status":
			return c.Registration(ctx, str("cwd"))
		case "project_set_workflow":
			return c.SetWorkflow(ctx, str("cwd"), str("workflow"), encoded(t, map[string]any{"steps": r["steps"], "required_secrets": r["required_secrets"], "timeout_seconds": r["timeout_seconds"]}))
		case "project_remove_workflow":
			return c.RemoveWorkflow(ctx, str("cwd"), str("workflow"))
		case "project_workflow":
			return c.Workflow(ctx, str("cwd"), str("workflow"))
		}
	}
	t.Fatalf("unhandled reference %v", tc)
	return nil, nil
}
