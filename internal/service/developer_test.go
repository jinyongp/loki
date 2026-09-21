package service

import (
	"os"
	"path/filepath"
	"testing"

	"loki/internal/config"
	"loki/internal/work/workspace"
)

func TestDeveloperViews(t *testing.T) {
	paths := serviceFixture(t)
	c, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Root = paths.Root()
	f, err := workspace.New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	if err = os.WriteFile(filepath.Join(c.Root, "report.xml"), []byte(`<testsuite tests="3" failures="1"/>`), 0600); err != nil {
		t.Fatal(err)
	}
	h := DeveloperHandler(f)
	diff := "diff --git a/new.txt b/new.txt\n--- /dev/null\n+++ b/new.txt\n@@ -0,0 +1 @@\n+added\n"
	truncated := false
	result, err := h(t.Context(), map[string]any{
		"action": "git_diff", "cwd": "repo", "staged": true,
		"path": "new.txt", "content": diff, "truncated": truncated,
	})
	if err != nil {
		t.Fatal(err)
	}
	value := result.StructuredContent.(map[string]any)
	stats := value["stats"].(map[string]any)
	if stats["files"] != 1 || stats["additions"] != 1 || stats["deletions"] != 0 ||
		value["title"] != "Staged changes" || value["subtitle"] != "repo · new.txt" ||
		value["content"] != diff || value["truncated"] != false {
		t.Fatal(value)
	}
	result, err = h(t.Context(), map[string]any{"action": "test_report", "report_path": "report.xml"})
	if err != nil {
		t.Fatal(err)
	}
	value = result.StructuredContent.(map[string]any)
	if value["stats"].(map[string]any)["tests"] != 3 || value["truncated"] != false ||
		value["subtitle"] != "report.xml" {
		t.Fatal(value)
	}
}

func TestDeveloperDiffRequiresCapturedResult(t *testing.T) {
	paths := serviceFixture(t)
	c, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	c.Root = paths.Root()
	f, err := workspace.New(c)
	if err != nil {
		t.Fatal(err)
	}
	defer f.Close()
	h := DeveloperHandler(f)
	if _, err = h(t.Context(), map[string]any{"action": "git_diff", "cwd": "repo"}); err == nil {
		t.Fatal("git_diff view accepted missing captured content")
	}
}
