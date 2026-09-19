package service

import (
	"context"
	"strings"
	"testing"
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
