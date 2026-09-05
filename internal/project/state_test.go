package project

import (
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
)

func ptr[T any](value T) *T { return &value }
func fixture(t *testing.T) (*Store, string, string) {
	t.Helper()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	repo := filepath.Join(workspace, "project")
	secondary := filepath.Join(workspace, "feature")
	if err := os.MkdirAll(repo, 0700); err != nil {
		t.Fatal(err)
	}
	git := func(args ...string) {
		t.Helper()
		cmd := exec.Command("/usr/bin/git", args...)
		cmd.Env = []string{"PATH=/usr/bin:/bin", "HOME=" + root, "GIT_CONFIG_NOSYSTEM=1", "GIT_CONFIG_GLOBAL=/dev/null", "GIT_AUTHOR_NAME=Fixture", "GIT_AUTHOR_EMAIL=fixture@example.invalid", "GIT_COMMITTER_NAME=Fixture", "GIT_COMMITTER_EMAIL=fixture@example.invalid"}
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v: %s %v", args, output, err)
		}
	}
	git("init", "-q", repo)
	git("-C", repo, "-c", "commit.gpgsign=false", "commit", "--allow-empty", "-qm", "fixture")
	git("-C", repo, "worktree", "add", "-q", "-b", "feature", secondary)
	store, err := New(workspace, filepath.Join(root, "state"))
	if err != nil {
		t.Fatal(err)
	}
	return store, repo, secondary
}
func initialize(t *testing.T, s *Store, cwd string) string {
	t.Helper()
	r, err := s.Initialize(t.Context(), cwd, InitRequest{Goal: "Share project state safely across worktrees", SlugBase: ptr("share-project-state"), Depth: "standard", IntentKind: "plan-local"})
	if err != nil {
		t.Fatal(err)
	}
	return r["slug"].(string)
}
func TestWorktreesShareStateWithIndependentBindings(t *testing.T) {
	s, repo, secondary := fixture(t)
	before, err := s.Status(t.Context(), repo)
	if err != nil || before["initialized"] != false {
		t.Fatalf("uninitialized: %v %v", before, err)
	}
	if _, err = os.Stat(s.StateRoot); !os.IsNotExist(err) {
		t.Fatal("status created central state")
	}
	slug := initialize(t, s, repo)
	a, err := s.Status(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	b, err := s.Status(t.Context(), secondary)
	if err != nil {
		t.Fatal(err)
	}
	if a["project_id"] != b["project_id"] || a["worktree_id"] == b["worktree_id"] || a["active_workstream"] != slug || b["active_workstream"] != nil || b["task_store"] != "central" {
		t.Fatalf("identities: %v %v", a, b)
	}
	if _, err = s.Bind(t.Context(), secondary, slug); err != nil {
		t.Fatal(err)
	}
	b, err = s.Status(t.Context(), secondary)
	if err != nil || b["active_workstream"] != slug {
		t.Fatalf("binding: %v %v", b, err)
	}
	if again := initialize(t, s, repo); again != slug {
		t.Fatal("initialization is not idempotent")
	}
	list, err := s.List(t.Context(), repo)
	if err != nil || len(list["workstreams"].([]map[string]any)) != 1 {
		t.Fatalf("list %v %v", list, err)
	}
	id, err := s.Resolve(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	logical := "/workspace/project/.git"
	if id.ProjectID != hash([]byte(logical))[:32] || id.WorktreeID != hash([]byte("project"))[:24] {
		t.Fatal("Python identity hash changed")
	}
	if data, err := os.ReadFile(filepath.Join(id.StateDirectory, "taskwarrior", "taskrc")); err != nil || !strings.Contains(string(data), "hooks=0\n") {
		t.Fatalf("taskrc %s %v", data, err)
	}
}
func TestLogicalGitAliasAndEscape(t *testing.T) {
	s, repo, secondary := fixture(t)
	expected, err := s.Resolve(t.Context(), repo)
	if err != nil {
		t.Fatal(err)
	}
	real := *s
	s.GitRead = func(ctx context.Context, cwd string, args ...string) (string, error) {
		value, err := real.gitRead(ctx, cwd, args...)
		return strings.ReplaceAll(value, s.WorkspaceRoot, "/workspace"), err
	}
	actual, err := s.Resolve(t.Context(), secondary)
	if err != nil || actual.ProjectID != expected.ProjectID || actual.WorktreeRoot != secondary {
		t.Fatalf("alias %v %v", actual, err)
	}
	outside := t.TempDir()
	if err = os.Symlink(outside, filepath.Join(s.WorkspaceRoot, "escape")); err != nil {
		t.Fatal(err)
	}
	s.GitRead = func(context.Context, string, ...string) (string, error) { return "/workspace/escape", nil }
	if _, err = s.Resolve(t.Context(), repo); err == nil || !strings.Contains(err.Error(), "escapes workspace") {
		t.Fatalf("alias escape: %v", err)
	}
	if _, err = s.Resolve(t.Context(), outside); err == nil {
		t.Fatal("outside cwd accepted")
	}
}
func TestArtifactOptimisticConcurrencyAcrossStoreInstances(t *testing.T) {
	s, repo, _ := fixture(t)
	initialize(t, s, repo)
	created, err := s.WriteArtifact(t.Context(), repo, nil, "plan.md", "first\n", nil)
	if err != nil {
		t.Fatal(err)
	}
	expected := created["sha256"].(string)
	var successes atomic.Int32
	var wg sync.WaitGroup
	for range 12 {
		wg.Go(func() {
			other, err := New(s.WorkspaceRoot, s.StateRoot)
			if err != nil {
				t.Error(err)
				return
			}
			_, err = other.WriteArtifact(t.Context(), repo, nil, "plan.md", "second\n", &expected)
			if err == nil {
				successes.Add(1)
			} else if !strings.Contains(err.Error(), "changed since") {
				t.Error(err)
			}
		})
	}
	wg.Wait()
	if successes.Load() != 1 {
		t.Fatalf("concurrent winners %d", successes.Load())
	}
	read, err := s.ReadArtifact(t.Context(), repo, nil, "plan.md")
	if err != nil || read["content"] != "second\n" {
		t.Fatalf("read %v %v", read, err)
	}
	if _, err = s.WriteArtifact(t.Context(), repo, nil, "plan.md", "third", nil); err == nil {
		t.Fatal("missing expected revision accepted")
	}
	if _, err = s.WriteArtifact(t.Context(), repo, nil, "spec.md", "new", &expected); err == nil {
		t.Fatal("nonexistent revision accepted")
	}
	for _, name := range []string{"../plan.md", "manifest.json", "taskrc"} {
		if _, err = s.WriteArtifact(t.Context(), repo, nil, name, "new", nil); err == nil {
			t.Fatalf("unsafe artifact %s", name)
		}
	}
	if _, err = s.WriteArtifact(t.Context(), repo, nil, "spec.md", strings.Repeat("x", MaxArtifactBytes+1), nil); err == nil {
		t.Fatal("oversized artifact accepted")
	}
}
func TestInitUnicodeAndValidation(t *testing.T) {
	s, repo, _ := fixture(t)
	r := InitRequest{Goal: "  Ｓtraße\u001c  테스트\n", SlugBase: ptr("unicode-project-state"), Depth: "standard", IntentKind: "plan-local"}
	out, err := s.Initialize(t.Context(), repo, r)
	if err != nil {
		t.Fatal(err)
	}
	id, _ := s.Resolve(t.Context(), repo)
	data, err := os.ReadFile(filepath.Join(id.StateDirectory, "workstreams", out["slug"].(string), "manifest.json"))
	if err != nil {
		t.Fatal(err)
	}
	var manifest map[string]any
	if err = json.Unmarshal(data, &manifest); err != nil {
		t.Fatal(err)
	}
	if manifest["goal"] != "Straße 테스트" || manifest["goal_hash"] != hash([]byte("strasse 테스트")) || manifest["intent_source"] != nil {
		t.Fatalf("canonical manifest %s", data)
	}
	for _, bad := range []InitRequest{{Goal: " ", Depth: "standard", IntentKind: "plan-local"}, {Goal: "valid", Slug: ptr("short"), Depth: "standard", IntentKind: "plan-local"}, {Goal: "valid", Slug: ptr("valid-three-words"), Depth: "invalid", IntentKind: "plan-local"}, {Goal: "valid", Slug: ptr("valid-three-words"), Depth: "standard", IntentKind: "authoritative"}, {Goal: "valid", Slug: ptr("valid-three-words"), Depth: "standard", IntentKind: "plan-local", IntentSource: ptr("")}} {
		if _, err = s.Initialize(t.Context(), repo, bad); err == nil {
			t.Fatalf("bad init accepted %+v", bad)
		}
	}
	r.Slug = ptr(out["slug"].(string))
	r.Goal = "conflicting goal"
	if _, err = s.Initialize(t.Context(), repo, r); err == nil || !strings.Contains(err.Error(), "identity does not match") {
		t.Fatalf("conflicting manifest %v", err)
	}
}
func TestCentralSymlinkProtection(t *testing.T) {
	s, repo, _ := fixture(t)
	outside := t.TempDir()
	if err := os.Symlink(outside, s.StateRoot); err != nil {
		t.Fatal(err)
	}
	if _, err := s.Initialize(t.Context(), repo, InitRequest{Goal: "test", Slug: ptr("safe-test-state"), Depth: "standard", IntentKind: "plan-local"}); err == nil {
		t.Fatal("state-root symlink accepted")
	}
	entries, err := os.ReadDir(outside)
	if err != nil || len(entries) != 0 {
		t.Fatal("outside state mutated")
	}
}
