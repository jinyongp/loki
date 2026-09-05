package secret

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func TestPrivateActionPlanSelectionAndWorktrees(t *testing.T) {
	c := fixture(t)
	ctx := t.Context()
	if _, err := c.CreateProfile(ctx, "web"); err != nil {
		t.Fatal(err)
	}
	if _, err := c.ImportValues(ctx, "web", map[string]string{"TOKEN": "fixture-private", "EMPTY": "", "PUBLIC_API": "http://127.0.0.1:41280/v1/"}); err != nil {
		t.Fatal(err)
	}
	a := action("project")
	if _, err := c.SetAction(ctx, "web", "dev", encoded(t, a)); err != nil {
		t.Fatal(err)
	}
	plan, err := c.ResolveAction(ctx, "web", "dev", nil)
	if err != nil {
		t.Fatal(err)
	}
	if plan.VisibleCWD != "/workspace/project" || !reflect.DeepEqual(plan.SelectedNames(), []string{"EMPTY", "PUBLIC_API", "TOKEN"}) {
		t.Fatalf("plan metadata: %s %v", plan.VisibleCWD, plan.SelectedNames())
	}
	for _, item := range []any{plan, &plan} {
		if _, err := json.Marshal(item); err == nil {
			t.Fatal("private plan serialized")
		}
		for _, format := range []string{"%v", "%+v", "%#v", "%s"} {
			if strings.Contains(fmt.Sprintf(format, item), "fixture-private") {
				t.Fatal("private plan leaked through formatting")
			}
		}
	}
	for _, cwd := range []string{"project", "feature"} {
		resolved, err := c.ResolveAction(ctx, "web", "dev", &cwd)
		if err != nil || resolved.VisibleCWD != "/workspace/"+cwd {
			t.Fatalf("same repository cwd: %s %v", cwd, err)
		}
	}
	for cwd, message := range map[string]string{
		"other":              "action cwd override must be a worktree of the registered repository",
		".":                  "action cwd override requires a Git worktree",
		"../outside":         "action cwd override must be workspace-relative",
		"/workspace/project": "action cwd override must be workspace-relative",
		"missing":            "action cwd is not a directory",
		"":                   "action cwd override must be workspace-relative",
	} {
		if _, err := c.ResolveAction(ctx, "web", "dev", &cwd); err == nil || err.Error() != message {
			t.Fatalf("cwd %q: %v", cwd, err)
		}
	}
	outside := t.TempDir()
	if err := os.Symlink(outside, filepath.Join(c.Projects.WorkspaceRoot, "escape")); err != nil {
		t.Fatal(err)
	}
	cwd := "escape"
	if _, err := c.ResolveAction(ctx, "web", "dev", &cwd); err == nil || err.Error() != "action cwd escapes workspace" {
		t.Fatalf("escaped cwd: %v", err)
	}
	a["secrets"], a["all_secrets"] = []string{"TOKEN"}, false
	if _, err = c.SetAction(ctx, "web", "dev", encoded(t, a)); err != nil {
		t.Fatal(err)
	}
	selected, err := c.ResolveAction(ctx, "web", "dev", nil)
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(selected.SecretEnvironment(), []string{"TOKEN=fixture-private"}) || len(selected.RedactionValues()) != 1 {
		t.Fatal("selected secret scope changed")
	}
	// Preview requirements include explicitly public dependencies even when they
	// are not part of the action's private injection set.
	bindings, err := selected.PreviewBindings()
	if err != nil || bindings.BackendRoutes["/api"] != 41280 || bindings.EnvironmentSuffixes["PUBLIC_API"] != "/v1" {
		t.Fatalf("preview mapping: %#v %v", bindings, err)
	}
	if _, err = c.SetSecret(ctx, "web", "TOKEN", "", false); err != nil {
		t.Fatal(err)
	}
	if _, err = c.ResolveAction(ctx, "web", "dev", nil); err == nil || err.Error() != "required secrets are not configured: TOKEN" {
		t.Fatalf("missing required: %v", err)
	}
	if selected.SecretEnvironment()[0] != "TOKEN=fixture-private" {
		t.Fatal("in-flight private snapshot changed after state update")
	}
}

func TestActionPreviewEnvironmentValidation(t *testing.T) {
	p := ActionPlan{Policy: ActionPolicy{PublicEnvironment: []string{"API", "REMOTE"}, PreviewEnvironment: map[string]string{"API": "/api"}, DynamicPort: &DynamicPort{OriginEnvironment: "ORIGIN"}}, previewValues: map[string]string{"API": "http://localhost:41280/v1", "REMOTE": "https://example.invalid/api"}}
	bindings, err := p.PreviewBindings()
	if err != nil || !reflect.DeepEqual(bindings.RequiredEnvironment, []string{"API", "ORIGIN"}) {
		t.Fatalf("required mapping: %#v %v", bindings, err)
	}
	base := "https://loki-" + strings.Repeat("a", 32) + ".example.test"
	values := map[string]string{"API": base + "/api/v1", "ORIGIN": base}
	got, err := p.PublicEnvironment(values, "example.test", true)
	if err != nil || !reflect.DeepEqual(got, values) {
		t.Fatalf("mapped environment: %v %v", got, err)
	}
	got["API"] = "changed"
	if values["API"] != base+"/api/v1" {
		t.Fatal("public environment result aliases input")
	}
	if _, err = p.PublicEnvironment(map[string]string{"ORIGIN": base}, "example.test", true); err == nil || !strings.HasPrefix(err.Error(), "PREVIEW_MAPPING_REQUIRED:") {
		t.Fatalf("missing dependency mapping: %v", err)
	}
	for _, raw := range []map[string]string{
		{"API": "http://127.0.0.1:41280"}, {"API": base + "?token=fixture"}, {"API": base + "#fragment"},
		{"API": base + ".attacker.invalid"}, {"TOKEN": base}, {"API": base + ":443"},
	} {
		if _, err = p.PublicEnvironment(raw, "example.test", false); err == nil {
			t.Fatal("invalid public environment accepted")
		}
	}
	p.previewValues["API"] = "http://user:password@localhost:41280"
	if _, err = p.PreviewBindings(); err == nil {
		t.Fatal("preview backend credentials accepted")
	}
	p.previewValues["API"] = "http://localhost:41280"
	p.Policy.PublicEnvironment = append(p.Policy.PublicEnvironment, "OTHER")
	p.Policy.PreviewEnvironment["OTHER"] = "/api"
	p.previewValues["OTHER"] = "http://127.0.0.1:49999"
	if _, err = p.PreviewBindings(); err == nil || err.Error() != "preview route has conflicting backends" {
		t.Fatalf("route collision: %v", err)
	}
}
