package devtools

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func setAgentGuidanceResponses(t *testing.T, client *Client, files map[string]string, skillList, skillInspect, guidance map[string]any) {
	t.Helper()
	write := func(name string, data map[string]any) string {
		path := filepath.Join(client.CWD, name+".json")
		raw, err := json.Marshal(map[string]any{"schema_version": 1, "ok": true, "data": data})
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, append(raw, '\n'), 0600); err != nil {
			t.Fatal(err)
		}
		return path
	}
	listPath := write("skill-list", skillList)
	inspectPath := write("skill-inspect", skillInspect)
	guidancePath := write("guidance", guidance)
	body := "#!/bin/sh\n" +
		"if [ \"$1\" = version ]; then printf '%s\\n' '{\"schema_version\":1,\"ok\":true,\"data\":{\"version\":\"0.17.0\",\"commit\":\"test\",\"protocol_version\":3}}'; exit 0; fi\n" +
		"if [ \"$1 $2\" = \"schema --all\" ]; then cat \"" + files["catalog"] + "\"; exit 0; fi\n" +
		"if [ \"$1 $2\" = \"project inspect\" ]; then cat \"" + files["project"] + "\"; exit 0; fi\n" +
		"printf '%s\\n' \"$@\" > \"" + files["log"] + "\"\n" +
		"case \"$1 $2\" in\n" +
		"  'skill list') cat \"" + listPath + "\" ;;\n" +
		"  'skill inspect') cat \"" + inspectPath + "\" ;;\n" +
		"  'guidance resolve') cat \"" + guidancePath + "\" ;;\n" +
		"  *) exit 2 ;;\n" +
		"esac\n"
	if err := os.WriteFile(client.Binary, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	client.mu.Lock()
	client.verified = false
	client.mu.Unlock()
}

func validAgentGuidanceFixtures(client *Client) (map[string]any, map[string]any, map[string]any) {
	projectRoot := filepath.Join(client.CWD, ".agents", "skills", "review-skill")
	userRoot := "/home/private-user/.agents/skills/review-skill"
	revision := strings.Repeat("a", 64)
	list := map[string]any{
		"items": []any{
			map[string]any{
				"name": "review-skill", "description": "Use for project reviews.", "scope": "project",
				"root": projectRoot, "revision": revision, "resource_count": 1, "total_bytes": 128,
			},
			map[string]any{
				"name": "user-only", "description": "Use for user work.", "scope": "user",
				"root": "/home/private-user/.agents/skills/user-only", "revision": strings.Repeat("b", 64),
				"resource_count": 0, "total_bytes": 64,
			},
		},
		"diagnostics": []any{
			map[string]any{"scope": "user", "path": "/home/private-user/.agents/skills/broken/SKILL.md", "code": "invalid_yaml", "message": "invalid"},
		},
		"shadowed": []any{
			map[string]any{
				"name": "review-skill", "selected_scope": "project", "selected_root": projectRoot,
				"shadowed_scope": "user", "shadowed_root": userRoot,
			},
		},
	}
	inspect := map[string]any{
		"item": map[string]any{
			"name": "review-skill", "description": "Use for project reviews.", "scope": "project",
			"root": projectRoot, "revision": revision, "resource_count": 1, "total_bytes": 128,
			"content": "---\nname: review-skill\ndescription: Use for project reviews.\n---\n# Review\n",
			"resources": []any{
				map[string]any{"path": "references/checks.md", "size": 12, "sha256": strings.Repeat("c", 64), "executable": false},
			},
		},
		"diagnostics": []any{},
		"shadowed": []any{
			map[string]any{
				"name": "review-skill", "selected_scope": "project", "selected_root": projectRoot,
				"shadowed_scope": "user", "shadowed_root": userRoot,
			},
		},
	}
	guidance := map[string]any{
		"target": "packages/api/new.go", "target_dir": "packages/api", "revision": strings.Repeat("d", 64),
		"complete": true, "total_bytes": 18,
		"sources": []any{
			map[string]any{"path": "AGENTS.md", "scope": ".", "revision": strings.Repeat("e", 64), "bytes": 10, "content": "root rules"},
			map[string]any{"path": "packages/api/AGENTS.md", "scope": "packages/api", "revision": strings.Repeat("f", 64), "bytes": 8, "content": "api rule"},
		},
		"diagnostics": []any{},
	}
	return list, inspect, guidance
}

func TestAgentGuidanceAdapterSanitizesSkillRootsAndLoadsProgressively(t *testing.T) {
	client, files := metadataClient(t)
	list, inspect, guidance := validAgentGuidanceFixtures(client)
	setAgentGuidanceResponses(t, client, files, list, inspect, guidance)

	catalog, err := client.ListSkills(t.Context(), ".")
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Items) != 2 || catalog.Items[0].Name != "review-skill" || len(catalog.Shadowed) != 1 {
		t.Fatalf("catalog = %#v", catalog)
	}
	raw, _ := json.Marshal(catalog)
	for _, forbidden := range []string{client.CWD, "/home/private-user", "selected_root", "shadowed_root", "\"content\"", "\"resources\""} {
		if strings.Contains(string(raw), forbidden) {
			t.Fatalf("catalog leaked %q: %s", forbidden, raw)
		}
	}

	detail, err := client.InspectSkill(t.Context(), ".", "review-skill")
	if err != nil {
		t.Fatal(err)
	}
	if detail.Item.Name != "review-skill" || !strings.Contains(detail.Item.Content, "# Review") ||
		len(detail.Item.Resources) != 1 || detail.Item.Resources[0].Path != "references/checks.md" {
		t.Fatalf("detail = %#v", detail)
	}
	raw, _ = json.Marshal(detail)
	if strings.Contains(string(raw), client.CWD) || strings.Contains(string(raw), "/home/private-user") {
		t.Fatalf("detail leaked host path: %s", raw)
	}

	resolved, err := client.ResolveGuidance(t.Context(), ".", "packages/api/new.go")
	if err != nil {
		t.Fatal(err)
	}
	if !resolved.Complete || len(resolved.Sources) != 2 || resolved.Sources[1].Content != "api rule" {
		t.Fatalf("guidance = %#v", resolved)
	}
}

func TestAgentGuidanceAdapterRejectsRawCallsUnsafePathsAndPrivateDrift(t *testing.T) {
	client, files := metadataClient(t)
	list, inspect, guidance := validAgentGuidanceFixtures(client)
	setAgentGuidanceResponses(t, client, files, list, inspect, guidance)

	if _, err := client.Call(t.Context(), "skill list", json.RawMessage([]byte("{\"dir\":\".\"}"))); err == nil || !strings.Contains(err.Error(), "typed adapter") {
		t.Fatalf("raw Skill call error = %v", err)
	}
	if _, err := client.ResolveGuidance(t.Context(), ".", "../outside.go"); err == nil {
		t.Fatal("traversing guidance target accepted")
	}
	if _, err := client.ResolveGuidance(t.Context(), ".", "/tmp/outside.go"); err == nil {
		t.Fatal("absolute guidance target accepted")
	}

	inspect["item"].(map[string]any)["resources"] = []any{
		map[string]any{"path": "../escape.md", "size": 1, "sha256": strings.Repeat("c", 64), "executable": false},
	}
	setAgentGuidanceResponses(t, client, files, list, inspect, guidance)
	if _, err := client.InspectSkill(t.Context(), ".", "review-skill"); err == nil || !strings.Contains(err.Error(), "invalid Skill resource") {
		t.Fatalf("unsafe resource error = %v", err)
	}

	list, inspect, guidance = validAgentGuidanceFixtures(client)
	guidance["sources"].([]any)[0].(map[string]any)["path"] = "../AGENTS.md"
	setAgentGuidanceResponses(t, client, files, list, inspect, guidance)
	if _, err := client.ResolveGuidance(t.Context(), ".", "packages/api/new.go"); err == nil || !strings.Contains(err.Error(), "unsafe guidance source") {
		t.Fatalf("unsafe guidance source error = %v", err)
	}
}
