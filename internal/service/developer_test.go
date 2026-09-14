package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"loki/internal/config"
	"loki/internal/gitops"
	"loki/internal/workspace"
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
	if err = os.WriteFile(filepath.Join(c.Root, "repo", "new.txt"), []byte("added\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command("/usr/bin/git", "-C", filepath.Join(c.Root, "repo"), "add", "new.txt").CombinedOutput(); err != nil {
		t.Fatalf("%s %v", out, err)
	}
	if err = os.WriteFile(filepath.Join(c.Root, "report.xml"), []byte(`<testsuite tests="3" failures="1"/>`), 0600); err != nil {
		t.Fatal(err)
	}
	h := DeveloperHandler(f, &gitops.Controller{Paths: paths, Config: c})
	result, err := h(t.Context(), map[string]any{"action": "git_diff", "cwd": "repo", "staged": true})
	if err != nil {
		t.Fatal(err)
	}
	value := result.StructuredContent.(map[string]any)
	stats := value["stats"].(map[string]any)
	if stats["files"] != 1 || stats["additions"] != 1 || value["title"] != "Staged changes" {
		t.Fatal(value)
	}
	result, err = h(t.Context(), map[string]any{"action": "test_report", "path": "report.xml"})
	if err != nil {
		t.Fatal(err)
	}
	value = result.StructuredContent.(map[string]any)
	if value["stats"].(map[string]any)["tests"] != 3 {
		t.Fatal(value)
	}
}
