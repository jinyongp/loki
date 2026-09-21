package agentcontext

import (
	"bytes"
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"loki/internal/config"
	"loki/internal/policy"
	"loki/internal/process"
	"loki/internal/work/workspace/git"
)

type providerTestGitRunner struct {
	root string
	env  []string
}

func (r providerTestGitRunner) Run(ctx context.Context, request gitops.CommandRequest) (gitops.CommandResult, error) {
	cwd := r.root
	if request.CWD != "." {
		cwd = filepath.Join(r.root, filepath.FromSlash(request.CWD))
	}
	result, err := process.Run(ctx, process.Spec{
		Argv: request.Argv, CWD: cwd, Env: r.env, Input: request.Input,
		Timeout: request.Timeout, MaxOutput: request.MaxOutput,
	})
	root := filepath.Clean(r.root)
	return gitops.CommandResult{
		ExitCode:  result.ExitCode,
		Output:    strings.ReplaceAll(result.Output, root, "/workspace"),
		Raw:       bytes.ReplaceAll(result.Raw, []byte(root), []byte("/workspace")),
		Truncated: result.Truncated, TimedOut: result.TimedOut, Canceled: result.Canceled,
	}, err
}

func TestProviderResolvesRepositoryFromCWD(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(filepath.Join(repo, "packages", "api", "src"), 0755); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"PATH=/usr/bin:/bin",
		"HOME=" + t.TempDir(),
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
	}
	cmd := exec.CommandContext(t.Context(), "/usr/bin/git", "init", "-q", "--initial-branch=main")
	cmd.Dir = repo
	cmd.Env = env
	if output, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("git init: %v %s", err, output)
	}

	writeGuidance(t, repo, "root\n")
	writeGuidance(t, filepath.Join(repo, "packages", "api"), "api\n")
	writeSkill(t, repo, "project-skill", "Project skill.", "project resource")
	userHome := t.TempDir()
	writeSkill(t, userHome, "user-skill", "User skill.", "")

	paths, err := policy.New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer paths.Close()
	configuration, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	git := &gitops.Controller{Paths: paths, Config: configuration, Env: env, Runner: providerTestGitRunner{root: paths.Root(), env: env}}
	provider := &Provider{Paths: paths, Git: git, UserHome: userHome}

	result, err := provider.Context(t.Context(), "repo/packages/api", "src/new.go")
	if err != nil {
		t.Fatal(err)
	}
	if result.Guidance.Target != "packages/api/src/new.go" || len(result.Guidance.Sources) != 2 {
		t.Fatalf("guidance = %#v", result.Guidance)
	}
	if len(result.Skills.Items) != 2 ||
		result.Skills.Items[0].Name != "project-skill" ||
		result.Skills.Items[1].Name != "user-skill" {
		t.Fatalf("skills = %#v", result.Skills)
	}

	skill, err := provider.Skill(t.Context(), "repo/packages/api", ".", "project-skill")
	if err != nil {
		t.Fatal(err)
	}
	if skill.Item.Scope != "project" || skill.Item.Content == "" || len(skill.Item.Resources) != 1 {
		t.Fatalf("skill = %#v", skill)
	}
}

func TestProviderContextUsesTargetOwningNestedRepository(t *testing.T) {
	root := t.TempDir()
	outer := filepath.Join(root, "outer")
	nested := filepath.Join(outer, "nested")
	if err := os.MkdirAll(filepath.Join(nested, "src"), 0755); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"PATH=/usr/bin:/bin",
		"HOME=" + t.TempDir(),
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
	}
	initRepository := func(path string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "/usr/bin/git", "init", "-q", "--initial-branch=main")
		cmd.Dir = path
		cmd.Env = env
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git init %s: %v %s", path, err, output)
		}
	}
	initRepository(outer)
	initRepository(nested)

	writeGuidance(t, outer, "outer rules\n")
	writeGuidance(t, nested, "nested rules\n")
	writeSkill(t, outer, "outer-skill", "Outer skill.", "")
	writeSkill(t, nested, "nested-skill", "Nested skill.", "")
	userHome := t.TempDir()
	writeSkill(t, userHome, "user-skill", "User skill.", "")
	if err := os.WriteFile(filepath.Join(nested, "src", "existing.go"), []byte("package nested\n"), 0644); err != nil {
		t.Fatal(err)
	}

	paths, err := policy.New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer paths.Close()
	configuration, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	git := &gitops.Controller{Paths: paths, Config: configuration, Env: env, Runner: providerTestGitRunner{root: paths.Root(), env: env}}
	provider := &Provider{Paths: paths, Git: git, UserHome: userHome}

	for _, test := range []struct {
		target string
		want   string
	}{
		{target: "nested/src/new.go", want: "src/new.go"},
		{target: "nested/src/existing.go", want: "src/existing.go"},
		{target: "nested/src", want: "src"},
	} {
		result, err := provider.Context(t.Context(), "outer", test.target)
		if err != nil {
			t.Fatalf("Context(%q): %v", test.target, err)
		}
		if result.Guidance.Target != test.want || len(result.Guidance.Sources) != 1 ||
			result.Guidance.Sources[0].Content != "nested rules\n" {
			t.Fatalf("Context(%q) guidance = %#v", test.target, result.Guidance)
		}
		if len(result.Skills.Items) != 2 ||
			result.Skills.Items[0].Name != "nested-skill" ||
			result.Skills.Items[1].Name != "user-skill" {
			t.Fatalf("Context(%q) skills = %#v", test.target, result.Skills)
		}
		for _, item := range result.Skills.Items {
			if item.Name == "outer-skill" {
				t.Fatalf("Context(%q) leaked outer project Skill: %#v", test.target, result.Skills)
			}
		}
	}
	nestedSkill, err := provider.Skill(t.Context(), "outer", "nested/src/new.go", "nested-skill")
	if err != nil {
		t.Fatal(err)
	}
	if nestedSkill.Item.Scope != "project" || nestedSkill.Item.Name != "nested-skill" {
		t.Fatalf("nested Skill = %#v", nestedSkill)
	}
	if _, err := provider.Skill(t.Context(), "outer", "nested/src/new.go", "outer-skill"); err == nil {
		t.Fatal("nested target loaded an outer-repository project Skill")
	}
}

func TestProviderContextSupportsInWorkspaceLinkedWorktree(t *testing.T) {
	root := t.TempDir()
	repo := filepath.Join(root, "repo")
	if err := os.MkdirAll(repo, 0755); err != nil {
		t.Fatal(err)
	}
	env := []string{
		"PATH=/usr/bin:/bin",
		"HOME=" + t.TempDir(),
		"LANG=C.UTF-8",
		"LC_ALL=C.UTF-8",
		"GIT_CONFIG_NOSYSTEM=1",
		"GIT_CONFIG_GLOBAL=/dev/null",
	}
	run := func(directory string, args ...string) {
		t.Helper()
		cmd := exec.CommandContext(t.Context(), "/usr/bin/git", args...)
		cmd.Dir = directory
		cmd.Env = env
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("git %v at %s: %v %s", args, directory, err, output)
		}
	}
	run(repo, "init", "-q", "--initial-branch=main")
	if err := os.WriteFile(filepath.Join(repo, "README.md"), []byte("base\n"), 0644); err != nil {
		t.Fatal(err)
	}
	run(repo, "add", "--", "README.md")
	run(repo,
		"-c", "user.name=Loki Test",
		"-c", "user.email=loki@example.test",
		"-c", "commit.gpgsign=false",
		"-c", "core.hooksPath=/dev/null",
		"commit", "-qm", "base",
	)
	feature := filepath.Join(root, "feature")
	run(repo, "worktree", "add", "-q", "-b", "feature", feature)
	writeGuidance(t, feature, "feature rules\n")
	writeSkill(t, feature, "feature-skill", "Feature skill.", "")

	paths, err := policy.New(root)
	if err != nil {
		t.Fatal(err)
	}
	defer paths.Close()
	configuration, err := config.Parse(nil)
	if err != nil {
		t.Fatal(err)
	}
	git := &gitops.Controller{Paths: paths, Config: configuration, Env: env, Runner: providerTestGitRunner{root: paths.Root(), env: env}}
	provider := &Provider{Paths: paths, Git: git}

	result, err := provider.Context(t.Context(), ".", "feature/new.go")
	if err != nil {
		t.Fatal(err)
	}
	if result.Guidance.Target != "new.go" || len(result.Guidance.Sources) != 1 ||
		result.Guidance.Sources[0].Content != "feature rules\n" {
		t.Fatalf("guidance = %#v", result.Guidance)
	}
	if len(result.Skills.Items) != 1 || result.Skills.Items[0].Name != "feature-skill" {
		t.Fatalf("skills = %#v", result.Skills)
	}
}
