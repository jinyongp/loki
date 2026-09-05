package project

import (
	"os"
	"strings"
	"testing"
)

func TestRawTaskArgumentRestrictions(t *testing.T) {
	for _, args := range [][]string{nil, make([]string, 129), {"exe", "/usr/bin/true"}, {"--", "conf"}, {"rc:/dev/null", "list"}, {"add", "rc.hooks:on"}, {"context", "define", "escape"}, {"ed"}, {"synchron"}, {"IM"}, {"un"}, {"add", "\x00"}, {strings.Repeat("x", 8193)}} {
		if err := ValidateTaskArguments(args); err == nil {
			t.Fatalf("accepted unsafe arguments %q", args)
		}
	}
	for _, args := range [][]string{{}, {"list"}, {"project:go", "add", "description:execute migration"}, {"1", "modify", "+ready"}, {"1", "annotate", "--", "execute migration"}, {"export"}, {"_get", "1.uuid"}} {
		if err := ValidateTaskArguments(args); err != nil {
			t.Fatalf("rejected normal arguments %q: %v", args, err)
		}
	}
}

func TestRawTaskRealSharedStore(t *testing.T) {
	binary := "/home/linuxbrew/.linuxbrew/bin/task"
	if _, err := os.Stat(binary); err != nil {
		t.Skip("Taskwarrior unavailable")
	}
	store, repo, secondary := fixture(t)
	tasks := Tasks{Store: store, Binary: binary, Home: t.TempDir()}
	if _, err := tasks.Raw(t.Context(), RawTaskRequest{CWD: repo, Arguments: []string{"list"}}); err == nil {
		t.Fatal("uninitialized datastore accepted")
	}
	slug := initialize(t, store, repo)
	for _, cwd := range []string{repo, secondary} {
		result, err := tasks.Raw(t.Context(), RawTaskRequest{CWD: cwd, Arguments: []string{"add", "project:" + slug, "description:raw wrapper 테스트"}})
		if err != nil || result["exit_code"] != 0 {
			t.Fatalf("add: %v %v", result, err)
		}
	}
	out, err := tasks.Do(t.Context(), TaskRequest{CWD: repo, Action: "count"})
	if err != nil || out["count"] != 2 {
		t.Fatalf("shared typed readback: %v %v", out, err)
	}
	for _, args := range [][]string{{"exec", "/usr/bin/true"}, {"rc:/dev/null", "list"}, {"conf", "hooks", "on"}} {
		if _, err = tasks.Raw(t.Context(), RawTaskRequest{CWD: repo, Arguments: args}); err == nil {
			t.Fatalf("unsafe command executed: %q", args)
		}
	}
	result, err := tasks.Raw(t.Context(), RawTaskRequest{CWD: repo, Arguments: []string{"show", "hooks"}})
	if err != nil || result["exit_code"] != 0 || !strings.Contains(result["output"].(string), "off") {
		t.Fatalf("hooks must remain off: %v %v", result, err)
	}
	for _, seconds := range []int{0, 1801} {
		if _, err := tasks.Raw(t.Context(), RawTaskRequest{CWD: repo, Arguments: []string{}, Timeout: &seconds}); err == nil {
			t.Fatal("invalid timeout accepted")
		}
	}
}
