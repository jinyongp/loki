package service

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"loki/internal/config"
	"loki/internal/gitops"
	"loki/internal/process"
	"loki/internal/workspace"
)

func TestDeveloperViews(t *testing.T) {
	_, paths := serviceFixture(t)
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
	manager, err := process.NewManager(process.ManagerOptions{MaxProcesses: 1, MaxOutputBytes: 4096, Retention: time.Minute})
	if err != nil {
		t.Fatal(err)
	}
	defer manager.Close()
	h := DeveloperHandler(f, &gitops.Controller{Paths: paths, Config: c}, manager)
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
	session, err := manager.Start(process.StartSpec{Name: "fixture", Spec: process.Spec{Argv: []string{"/usr/bin/printf", "\x1b[31mred\x1b[0m"}, CWD: c.Root, Timeout: time.Second, MaxOutput: 4096}})
	if err != nil {
		t.Fatal(err)
	}
	id := session["session_id"].(string)
	deadline := time.Now().Add(2 * time.Second)
	for {
		snapshot, err := manager.Read(id, nil, 4096)
		if err != nil {
			t.Fatal(err)
		}
		if snapshot["status"] == "exited" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("process timeout")
		}
		time.Sleep(10 * time.Millisecond)
	}
	result, err = h(t.Context(), map[string]any{"action": "process_log", "session_id": id, "offset": 0, "limit": 4096})
	if err != nil {
		t.Fatal(err)
	}
	value = result.StructuredContent.(map[string]any)
	if value["content"] != "red" || value["kind"] != "log" {
		t.Fatal(value)
	}
}
