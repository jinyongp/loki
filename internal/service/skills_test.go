package service

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/contract"
	"loki/internal/mcpserver"
	"loki/internal/skills"
)

func TestSkillsMCP(t *testing.T) {
	paths := serviceFixture(t)
	registry := &skills.Registry{Workspace: paths}
	handlers := SkillReadHandlers(registry, map[string]bool{"workspace_read": true})
	handlers["skill_write"] = SkillWriteHandler(registry)
	baseline, _ := contract.Baseline()
	definitions, _ := baseline.Definitions()
	for _, definition := range definitions {
		if handlers[definition.Name] == nil {
			handlers[definition.Name] = func(context.Context, map[string]any) (*mcp.CallToolResult, error) {
				return nil, errors.New("outside skills test")
			}
		}
	}
	server, err := mcpserver.New(handlers)
	if err != nil {
		t.Fatal(err)
	}
	a, b := mcp.NewInMemoryTransports()
	ss, err := server.Connect(t.Context(), a, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer ss.Close()
	cs, err := mcp.NewClient(&mcp.Implementation{Name: "skills-test", Version: "1"}, nil).Connect(t.Context(), b, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer cs.Close()
	call := func(name string, args map[string]any) map[string]any {
		t.Helper()
		result, err := cs.CallTool(t.Context(), &mcp.CallToolParams{Name: name, Arguments: args})
		if err != nil || result.IsError {
			t.Fatalf("%s: %v %#v", name, err, result)
		}
		data, _ := json.Marshal(result.StructuredContent)
		var value map[string]any
		if err := json.Unmarshal(data, &value); err != nil {
			t.Fatal(err)
		}
		return value
	}
	created := call("skill_write", map[string]any{"action": "create", "name": "example", "scope": "project", "cwd": "repo", "description": "An example", "instructions": "Read project files.", "metadata": map[string]any{"metadata": map[string]any{"required-tools": []string{"workspace_read"}}}})
	if created["scope"] != "project" {
		t.Fatal(created)
	}
	listed := call("skill_read", map[string]any{"action": "list", "cwd": "repo"})
	if len(listed["skills"].([]any)) != 1 {
		t.Fatal(listed)
	}
	active := call("skill_read", map[string]any{"action": "activate", "name": "example", "cwd": "repo"})
	if active["instructions"] != "\nRead project files.\n" {
		t.Fatal(active)
	}
	valid := call("skill_read", map[string]any{"action": "validate", "name": "example", "cwd": "repo"})
	if valid["valid"] != true {
		t.Fatal(valid)
	}
	call("skill_write", map[string]any{"action": "resource", "name": "example", "cwd": "repo", "path": "references/guide.txt", "content": "Guide"})
	resource := call("skill_read", map[string]any{"action": "resource", "name": "example", "cwd": "repo", "path": "references/guide.txt"})
	if resource["content"] != "Guide" {
		t.Fatal(resource)
	}
	ctx := call("agent_context", map[string]any{"cwd": "repo"})
	if len(ctx["skills"].([]any)) != 1 {
		t.Fatal(ctx)
	}
}
