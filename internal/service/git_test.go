package service

import (
	"context"
	"strings"
	"testing"

	"loki/internal/config"
	"loki/internal/gitops"
)

func TestGitStageHandlerRequiresIndexCAS(t *testing.T) {
	handler := GitHandlers(nil)["git_stage"]
	for _, input := range []map[string]any{
		{"action": "paths", "cwd": ".", "paths": []string{"a.txt"}},
		{"action": "unstage", "cwd": ".", "paths": []string{"a.txt"}},
		{"action": "patch", "cwd": ".", "patch": "diff --git a/a.txt b/a.txt\n"},
	} {
		result, err := handler(context.Background(), input)
		if err == nil || result != nil || !strings.Contains(err.Error(), "expected_index_sha256") {
			t.Fatalf("git_stage without index CAS: input=%#v result=%#v err=%v", input, result, err)
		}
	}
}

func TestGitInspectHandlerAppliesDefaultStatusAction(t *testing.T) {
	paths := serviceFixture(t)
	configuration, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	git := &gitops.Controller{
		Paths:  paths,
		Config: configuration,
		Env: []string{
			"PATH=/usr/bin:/bin", "HOME=" + t.TempDir(), "LANG=C.UTF-8", "LC_ALL=C.UTF-8",
			"GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null",
		},
	}
	handler := GitHandlers(git)["git_inspect"]
	result, err := handler(t.Context(), map[string]any{"cwd": "repo"})
	if err != nil || result == nil || result.IsError {
		t.Fatalf("default git_inspect status = %#v, %v", result, err)
	}
	value := result.StructuredContent.(map[string]any)
	if value["exit_code"] != 0 || value["truncated"] != false {
		t.Fatalf("default git_inspect result = %#v", value)
	}
}
