package skills

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"testing"

	"loki/internal/policy"
)

func fixture(t *testing.T) *Registry {
	t.Helper()
	root, builtin := t.TempDir(), t.TempDir()
	write := func(path, text string) {
		t.Helper()
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, []byte(text), 0600); err != nil {
			t.Fatal(err)
		}
	}
	for _, base := range []string{builtin, filepath.Join(root, ".agents/skills"), filepath.Join(root, "repo/.agents/skills")} {
		write(filepath.Join(base, "example/SKILL.md"), "---\nname: example\ndescription: Example skill\nmetadata:\n  required-tools: [workspace_read, absent, absent]\nallowed-tools: Bash\n---\n\nRead carefully.\n")
	}
	write(filepath.Join(root, "AGENTS.md"), "Root instructions.\r\n")
	write(filepath.Join(root, "repo/AGENTS.md"), "Project instructions.\n")
	write(filepath.Join(root, "repo/.git"), "gitdir: ignored-for-discovery\n")
	write(filepath.Join(root, "repo/.agents/skills/example/references/info.txt"), "Reference.\n")
	write(filepath.Join(root, "repo/.agents/skills/example/assets/raw.bin"), string([]byte{0xff, 0, 1}))
	if err := os.MkdirAll(filepath.Join(root, "repo/sub"), 0700); err != nil {
		t.Fatal(err)
	}
	w, err := policy.New(root)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { w.Close() })
	b, err := policy.New(builtin)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { b.Close() })
	return &Registry{Workspace: w, Builtin: b}
}
func TestRegistryReadAndBoundaries(t *testing.T) {
	r := fixture(t)
	list, err := r.List("repo/sub")
	if err != nil {
		t.Fatal(err)
	}
	items := list["skills"].([]map[string]any)
	if len(items) != 3 || items[0]["selected"] != false || items[2]["scope"] != "project" || items[2]["selected"] != true {
		t.Fatal(list)
	}
	ctx, err := r.AgentContext("repo/sub")
	if err != nil {
		t.Fatal(err)
	}
	if len(ctx["agents"].([]map[string]any)) != 2 || len(ctx["skills"].([]map[string]any)) != 1 {
		t.Fatal(ctx)
	}
	active, err := r.Activate("example", "repo")
	if err != nil {
		t.Fatal(err)
	}
	if active["instructions"] != "\nRead carefully.\n" || len(active["resources"].([]map[string]any)) != 2 {
		t.Fatal(active)
	}
	valid, err := r.Validate("example", "repo", map[string]bool{"workspace_read": true})
	if err != nil {
		t.Fatal(err)
	}
	if valid["valid"] != false || !reflect.DeepEqual(valid["missing_tools"], []string{"absent"}) {
		t.Fatal(valid)
	}
	binary, err := r.ReadResource("example", "assets/raw.bin", "repo")
	if err != nil {
		t.Fatal(err)
	}
	if binary["encoding"] != "base64" || binary["content"] != "/wAB" {
		t.Fatal(binary)
	}
	for _, path := range []string{"../AGENTS.md", "/etc/passwd", "references/../../AGENTS.md", "references\\file"} {
		if _, err := r.ReadResource("example", path, "repo"); err == nil {
			t.Fatalf("accepted %s", path)
		}
	}
	outside := filepath.Join(t.TempDir(), "private")
	if err := os.WriteFile(outside, []byte("outside"), 0600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(r.Workspace.Root(), "repo/.agents/skills/example/references/link")
	if err := os.Symlink(outside, link); err != nil {
		t.Fatal(err)
	}
	if _, err := r.ReadResource("example", "references/link", "repo"); err == nil {
		t.Fatal("symlink read")
	}
	if _, err := r.Activate("example", "repo"); err == nil {
		t.Fatal("symlink listed")
	}
}

func TestPython0471SkillsDifferential(t *testing.T) {
	python := os.Getenv("LOKI_REFERENCE_PYTHON")
	if python == "" {
		t.Skip("Python reference not configured")
	}
	r := fixture(t)
	operations := []func() (map[string]any, error){
		func() (map[string]any, error) { return r.List("repo/sub") },
		func() (map[string]any, error) { return r.AgentContext("repo/sub") },
		func() (map[string]any, error) { return r.Activate("example", "repo/sub") },
		func() (map[string]any, error) {
			return r.Validate("example", "repo/sub", map[string]bool{"workspace_read": true})
		},
		func() (map[string]any, error) { return r.ReadResource("example", "references/info.txt", "repo/sub") },
		func() (map[string]any, error) { return r.ReadResource("example", "assets/raw.bin", "repo/sub") },
	}
	got := []map[string]any{}
	for _, op := range operations {
		result, err := op()
		if err != nil {
			t.Fatal(err)
		}
		got = append(got, result)
	}
	script := `import json,sys
from pathlib import Path
from loki_mcp.policy import WorkspacePolicy
import loki_mcp.skills as skills
skills.BUILTIN_SKILL_ROOT=Path(sys.argv[2])
r=skills.SkillRegistry(WorkspacePolicy(Path(sys.argv[1])))
print(json.dumps([r.list_skills('repo/sub'),r.agent_context('repo/sub'),r.activate_skill('example','repo/sub'),r.validate('example','repo/sub',available_tools={'workspace_read'}),r.read_resource('example','references/info.txt','repo/sub'),r.read_resource('example','assets/raw.bin','repo/sub')]))`
	cmd := exec.CommandContext(t.Context(), python, "-c", script, r.Workspace.Root(), r.Builtin.Root())
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, data)
	}
	var want, normalized any
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	encoded, _ := json.Marshal(got)
	if err := json.Unmarshal(encoded, &normalized); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(normalized, want) {
		t.Fatalf("Go: %s\nPython: %s", encoded, data)
	}
	t.Logf("%d Python skill responses match", len(operations))
}
