package service

import (
	"context"
	"encoding/json"
	"strings"
	"testing"

	"loki/internal/agentcontext"
)

type recordingAgentProvider struct {
	contextCalls int
	skillCalls   int
	cwd          string
	target       string
	name         string
}

func (p *recordingAgentProvider) Context(_ context.Context, cwd, target string) (agentcontext.ContextResult, error) {
	p.contextCalls++
	p.cwd, p.target = cwd, target
	return agentcontext.ContextResult{
		Guidance: agentcontext.GuidanceResult{
			Target: "src/new.go", TargetDir: "src",
			Revision: strings.Repeat("a", 64), Complete: true,
			Sources: []agentcontext.GuidanceSource{
				{Path: "AGENTS.md", Scope: ".", Revision: strings.Repeat("b", 64), Bytes: 10, Content: "root rules"},
			},
			Diagnostics: []agentcontext.GuidanceDiagnostic{}, TotalBytes: 10,
		},
		Skills: agentcontext.SkillCatalog{
			Complete: true,
			Items: []agentcontext.SkillSummary{
				{Name: "review-skill", Description: "Use for reviews.", Scope: "project", Revision: strings.Repeat("c", 64), ResourceCount: 1, TotalBytes: 128},
			},
			Diagnostics: []agentcontext.SkillDiagnostic{}, Shadowed: []agentcontext.SkillShadow{},
		},
	}, nil
}

func (p *recordingAgentProvider) Skill(_ context.Context, cwd, target, name string) (agentcontext.SkillInspection, error) {
	p.skillCalls++
	p.cwd, p.target, p.name = cwd, target, name
	return agentcontext.SkillInspection{
		Item: agentcontext.SkillDetail{
			SkillSummary: agentcontext.SkillSummary{
				Name: name, Description: "Use for reviews.", Scope: "project",
				Revision: strings.Repeat("c", 64), ResourceCount: 1, TotalBytes: 128,
			},
			Content: "# Review",
			Resources: []agentcontext.SkillResource{
				{Path: "references/checks.md", Size: 12, SHA256: strings.Repeat("d", 64)},
			},
		},
		Diagnostics: []agentcontext.SkillDiagnostic{}, Shadowed: []agentcontext.SkillShadow{},
	}, nil
}

func TestAgentGuidanceContextIsMetadataFirstAndSkillIsOnDemand(t *testing.T) {
	provider := &recordingAgentProvider{}
	handlers := AgentGuidanceHandlers(provider)

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
	if provider.contextCalls != 1 || provider.skillCalls != 0 || provider.target != "src/new.go" {
		t.Fatalf("provider state = %#v", provider)
	}

	skillResult, err := handlers["agent_guidance"](t.Context(), map[string]any{
		"action": "skill", "cwd": ".", "target": "src/new.go", "name": "review-skill",
	})
	if err != nil || skillResult == nil || skillResult.IsError {
		t.Fatalf("skill result=%#v err=%v", skillResult, err)
	}
	skillJSON, _ := json.Marshal(skillResult.StructuredContent)
	if !strings.Contains(string(skillJSON), "# Review") || !strings.Contains(string(skillJSON), "references/checks.md") {
		t.Fatalf("skill detail missing: %s", skillJSON)
	}
	if provider.skillCalls != 1 || provider.target != "src/new.go" || provider.name != "review-skill" {
		t.Fatalf("provider state = %#v", provider)
	}
}

func TestAgentGuidanceRejectsIrrelevantFields(t *testing.T) {
	provider := &recordingAgentProvider{}
	handler := AgentGuidanceHandlers(provider)["agent_guidance"]
	if _, err := handler(t.Context(), map[string]any{"action": "context", "name": "review-skill"}); err == nil {
		t.Fatal("context accepted Skill name")
	}
	if _, err := handler(t.Context(), map[string]any{"action": "skill", "target": "src"}); err == nil {
		t.Fatal("skill accepted a missing name")
	}
	if provider.contextCalls != 0 || provider.skillCalls != 0 {
		t.Fatalf("provider was called for rejected request: %#v", provider)
	}
}
