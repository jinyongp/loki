package skills

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"loki/internal/policy"
)

func TestBundledCatalogContainsPinnedDevtoolsSkill(t *testing.T) {
	_, source, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("cannot locate bundled skills")
	}
	bundled := filepath.Join(filepath.Dir(source), "..", "..", "bundled_skills")
	raw, err := os.ReadFile(filepath.Join(bundled, "devtools", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := digest(raw), "55a260c71fff25e7a731244bbf7043f055cadde26f5bc79fe4430ff23a0ea3bd"; got != want {
		t.Fatalf("devtools 0.8.2 skill digest = %s, want %s", got, want)
	}
	workspace, err := policy.New(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { workspace.Close() })
	builtin, err := policy.New(bundled)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { builtin.Close() })
	registry := &Registry{Workspace: workspace, Builtin: builtin}
	list, err := registry.List(".")
	if err != nil {
		t.Fatal(err)
	}
	items := list["skills"].([]map[string]any)
	if len(items) != 1 || items[0]["name"] != "devtools" || items[0]["selected"] != true {
		t.Fatalf("bundled skills = %#v", list)
	}
	active, err := registry.Activate("devtools", ".")
	if err != nil || !strings.Contains(active["instructions"].(string), "devtools schema task claim") {
		t.Fatalf("activated devtools skill = %#v, error = %v", active, err)
	}
}

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

func TestPython0471SkillWritesDifferential(t *testing.T) {
	python := os.Getenv("LOKI_REFERENCE_PYTHON")
	if python == "" {
		t.Skip("Python reference not configured")
	}
	r := fixture(t)
	created, err := r.Create("project", "repo", "created", "A skill", "Original.", nil)
	if err != nil {
		t.Fatal(err)
	}
	active, err := r.Activate("created", "repo")
	if err != nil {
		t.Fatal(err)
	}
	written, err := r.WriteResource("created", "assets/data.bin", "/wAB", "repo", false, nil, "base64")
	if err != nil {
		t.Fatal(err)
	}
	patch := "--- a/SKILL.md\n+++ b/SKILL.md\n@@ -3,4 +3,4 @@\n description: A skill\n ---\n \n-Original.\n+Updated.\n"
	edited, err := r.Edit(t.Context(), "created", "repo", patch, created["sha256"].(string))
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	script := `import json,sys
from pathlib import Path
from loki_mcp.policy import WorkspacePolicy
import loki_mcp.skills as skills
root=Path(sys.argv[1]); (root/'repo/.git').mkdir(parents=True)
skills.BUILTIN_SKILL_ROOT=root/'missing-builtin'
r=skills.SkillRegistry(WorkspacePolicy(root))
c=r.create('project','repo','created','A skill','Original.')
a=r.activate_skill('created','repo')
w=r.write_resource('created','assets/data.bin','/wAB','repo',encoding='base64')
e=r.edit('created','repo',sys.argv[2],c['sha256'])
print(json.dumps([c,a,w,e]))`
	cmd := exec.CommandContext(t.Context(), python, "-c", script, root, patch)
	data, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("%v %s", err, data)
	}
	encoded, _ := json.Marshal([]map[string]any{created, active, written, edited})
	var want, got any
	if err := json.Unmarshal(data, &want); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal(encoded, &got); err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Go: %s\nPython: %s", encoded, data)
	}
	t.Log("4 Python skill write responses match")
}
