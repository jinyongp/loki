package workspace

import (
	"context"
	"strings"
	"testing"
)

type fakeRepository struct {
	statusCalls int
}

func (r *fakeRepository) Status(context.Context, string) (map[string]any, error) {
	r.statusCalls++
	return map[string]any{"exit_code": 0, "output": "", "truncated": false}, nil
}
func (*fakeRepository) Diff(context.Context, string, bool, *string) (map[string]any, error) {
	return map[string]any{"exit_code": 0, "output": "", "truncated": false}, nil
}
func (*fakeRepository) Index(context.Context, string) (map[string]any, error) {
	return map[string]any{"index_sha256": strings.Repeat("a", 64)}, nil
}
func (*fakeRepository) CommitContext(context.Context, string) (map[string]any, error) {
	return map[string]any{"configured": false}, nil
}
func (*fakeRepository) MutatePaths(context.Context, string, string, []string, *string) (map[string]any, error) {
	return map[string]any{}, nil
}
func (*fakeRepository) StagePatch(context.Context, string, string, bool, *string) (map[string]any, error) {
	return map[string]any{}, nil
}

func TestGitStageHandlerRequiresIndexCAS(t *testing.T) {
	handler := GitHandlers(&fakeRepository{})["git_stage"]
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
	repository := &fakeRepository{}
	handler := GitHandlers(repository)["git_inspect"]
	result, err := handler(t.Context(), map[string]any{"cwd": "repo"})
	if err != nil || result == nil || result.IsError {
		t.Fatalf("default git_inspect status = %#v, %v", result, err)
	}
	value := result.StructuredContent.(map[string]any)
	if value["exit_code"] != 0 || value["truncated"] != false || repository.statusCalls != 1 {
		t.Fatalf("default git_inspect result = %#v calls=%d", value, repository.statusCalls)
	}
}
