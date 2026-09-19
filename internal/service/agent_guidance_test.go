package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
)

func TestAgentGuidanceContextIsMetadataFirstAndSkillIsOnDemand(t *testing.T) {
	calls := []string{}
	runtime := runtimeFixture(func(_ context.Context, request any) (json.RawMessage, error) {
		row := request.(map[string]any)
		operation := row["operation"].(string)
		calls = append(calls, operation)
		switch operation {
		case "devtools_guidance_resolve":
			return json.RawMessage(`{
				"target":"src/new.go","target_dir":"src","revision":"aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
				"complete":true,
				"sources":[{"path":"AGENTS.md","scope":".","revision":"bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb","bytes":10,"content":"root rules"}],
				"diagnostics":[],"total_bytes":10
			}`), nil
		case "devtools_skill_list":
			return json.RawMessage(`{
				"items":[{"name":"review-skill","description":"Use for reviews.","scope":"project","revision":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","resource_count":1,"total_bytes":128}],
				"diagnostics":[],"shadowed":[]
			}`), nil
		case "devtools_skill_inspect":
			return json.RawMessage(`{
				"item":{"name":"review-skill","description":"Use for reviews.","scope":"project","revision":"cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc","resource_count":1,"total_bytes":128,"content":"# Review","resources":[{"path":"references/checks.md","size":12,"sha256":"dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd","executable":false}]},
				"diagnostics":[],"shadowed":[]
			}`), nil
		default:
			t.Fatalf("unexpected operation %q", operation)
			return nil, nil
		}
	})
	handlers := AgentGuidanceHandlers(runtime)

	contextResult, err := handlers["agent_guidance"](t.Context(), map[string]any{
		"action": "context", "cwd": ".", "target": "src/new.go",
	})
	if err != nil || contextResult == nil || contextResult.IsError {
		t.Fatalf("context result=%#v err=%v", contextResult, err)
	}
	contextJSON, _ := json.Marshal(contextResult.StructuredContent)
	if strings.Contains(string(contextJSON), "# Review") || strings.Contains(string(contextJSON), "references/checks.md") {
		t.Fatalf("context eagerly loaded Skill body/resources: %s", contextJSON)
	}
	if !strings.Contains(string(contextJSON), "root rules") || !strings.Contains(string(contextJSON), "review-skill") {
		t.Fatalf("context missing guidance/inventory: %s", contextJSON)
	}
	if len(calls) != 2 || calls[0] != "devtools_guidance_resolve" || calls[1] != "devtools_skill_list" {
		t.Fatalf("context calls = %#v", calls)
	}

	calls = nil
	skillResult, err := handlers["agent_guidance"](t.Context(), map[string]any{
		"action": "skill", "cwd": ".", "name": "review-skill",
	})
	if err != nil || skillResult == nil || skillResult.IsError {
		t.Fatalf("skill result=%#v err=%v", skillResult, err)
	}
	skillJSON, _ := json.Marshal(skillResult.StructuredContent)
	if !strings.Contains(string(skillJSON), "# Review") || !strings.Contains(string(skillJSON), "references/checks.md") {
		t.Fatalf("skill detail missing: %s", skillJSON)
	}
	if len(calls) != 1 || calls[0] != "devtools_skill_inspect" {
		t.Fatalf("skill calls = %#v", calls)
	}
}

func TestAgentGuidanceRejectsIrrelevantFields(t *testing.T) {
	calls := 0
	runtime := runtimeFixture(func(context.Context, any) (json.RawMessage, error) {
		calls++
		return json.RawMessage(`{"items":[],"diagnostics":[],"shadowed":[]}`), nil
	})
	handler := AgentGuidanceHandlers(runtime)["agent_guidance"]
	if _, err := handler(t.Context(), map[string]any{"action": "context", "name": "review-skill"}); err == nil {
		t.Fatal("context accepted Skill name")
	}
	if _, err := handler(t.Context(), map[string]any{"action": "skill", "name": "review-skill", "target": "src"}); err == nil {
		t.Fatal("skill accepted target")
	}
	if calls != 0 {
		t.Fatalf("runtime called %d times for rejected requests", calls)
	}
}
