package githubapp

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type repositoryTokenFunc func(context.Context, string) (string, error)

func (f repositoryTokenFunc) Token(ctx context.Context, target string) (string, error) {
	return f(ctx, target)
}

func commandRunnerFixture(t *testing.T) (*CommandRunner, *atomic.Int32) {
	t.Helper()
	binary := filepath.Join(t.TempDir(), "gh")
	script := "#!/bin/sh\n" +
		"if [ \"$1\" = api ] && [ \"$2\" = sleep ]; then sleep 5; fi\n" +
		"if [ \"$1\" = api ] && [ \"$2\" = loud ]; then head -c 5000 /dev/zero | tr '\\\\0' x; exit 0; fi\n" +
		"if [ \"$1\" = api ] && [ \"$2\" = leak ]; then printf '%s' \"$GH_TOKEN\"; exit 7; fi\n" +
		"[ \"$GH_TOKEN\" = installation-token ] || exit 21\n" +
		"[ \"$GH_REPO\" = connextable/loki ] || exit 22\n" +
		"[ \"$GH_HOST\" = github.com ] || exit 23\n" +
		"[ \"$GH_PROMPT_DISABLED\" = 1 ] || exit 24\n" +
		"[ -z \"$AMBIENT_SECRET\" ] || exit 25\n" +
		"[ \"$HOME\" = \"$GH_CONFIG_DIR\" ] || exit 26\n" +
		"printf 'config=%s\\n' \"$GH_CONFIG_DIR\"\n" +
		"for value in \"$@\"; do printf 'arg=%s\\n' \"$value\"; done\n" +
		"cat\n"
	if err := os.WriteFile(binary, []byte(script), 0700); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	runner := &CommandRunner{
		Config: CommandConfig{
			Binary: binary, CWD: t.TempDir(),
			Environment: []string{"AMBIENT_SECRET=do-not-inherit", "HTTPS_PROXY=http://127.0.0.1:18766"},
			Timeout:     time.Second, MaxInputBytes: 4096, MaxOutputBytes: 4096,
		},
		Tokens: repositoryTokenFunc(func(_ context.Context, target string) (string, error) {
			calls.Add(1)
			if target != "connextable/loki" {
				return "", errors.New("wrong target")
			}
			return "installation-token", nil
		}),
	}
	return runner, &calls
}

func TestCommandRunnerUsesFixedRepositoryAndCleanEnvironment(t *testing.T) {
	runner, calls := commandRunnerFixture(t)
	result, err := runner.Run(t.Context(), CommandRequest{
		Target: " Connextable/Loki ",
		Args:   []string{"issue", "list", "--limit", "1"},
		Input:  []byte("payload"),
	})
	if err != nil || result.ExitCode != 0 || result.Truncated || calls.Load() != 1 {
		t.Fatalf("result=%#v calls=%d err=%v", result, calls.Load(), err)
	}
	for _, want := range []string{"arg=issue", "arg=list", "arg=--limit", "arg=1", "payload"} {
		if !strings.Contains(result.Output, want) {
			t.Fatalf("missing %q in %q", want, result.Output)
		}
	}
	first := strings.SplitN(result.Output, "\n", 2)[0]
	configDir, ok := strings.CutPrefix(first, "config=")
	if !ok {
		t.Fatalf("missing config directory: %q", result.Output)
	}
	if _, statErr := os.Stat(configDir); !errors.Is(statErr, os.ErrNotExist) {
		t.Fatalf("temporary gh config remains: %v", statErr)
	}
	search, err := runner.Run(t.Context(), CommandRequest{
		Target: "connextable/loki", Args: []string{"search", "issues", "is:open"},
	})
	if err != nil || !strings.Contains(search.Output, "arg=--repo") ||
		!strings.Contains(search.Output, "arg=connextable/loki") {
		t.Fatal("search was not fixed to the target repository", search, err)
	}
}

func TestCommandRunnerRejectsCommandAndScopeOverridesBeforeTokenAccess(t *testing.T) {
	runner, calls := commandRunnerFixture(t)
	cases := [][]string{
		{"auth", "status"},
		{"extension", "exec", "x"},
		{"issue", "list", "--repo", "other/repo"},
		{"api", "--hostname=example.com", "/user"},
		{"secret", "set", "X", "--org", "other"},
		{"pr", "create", "--web"},
	}
	for _, args := range cases {
		if _, err := runner.Run(t.Context(), CommandRequest{Target: "connextable/loki", Args: args}); err == nil {
			t.Errorf("accepted %#v", args)
		}
	}
	if _, err := runner.Run(t.Context(), CommandRequest{
		Target: "connextable/loki", Args: []string{"issue", "create", "--", "--repo"},
	}); err != nil {
		t.Fatalf("positional value after -- rejected: %v", err)
	}
	if _, err := runner.Run(t.Context(), CommandRequest{
		Target: "connextable/loki", Args: []string{"search", "repos", "loki"},
	}); err == nil {
		t.Fatal("cross-repository search accepted")
	}
	if calls.Load() != 1 {
		t.Fatalf("credentials accessed for rejected commands: %d", calls.Load())
	}
}

func TestCommandRunnerBoundsInputOutputAndTime(t *testing.T) {
	runner, calls := commandRunnerFixture(t)
	runner.Config.MaxInputBytes = 3
	if _, err := runner.Run(t.Context(), CommandRequest{
		Target: "connextable/loki", Args: []string{"api", "/repos/x/y"}, Input: []byte("four"),
	}); err == nil || calls.Load() != 0 {
		t.Fatalf("oversize input accepted; calls=%d", calls.Load())
	}
	runner.Config.MaxInputBytes = 4096
	runner.Config.MaxOutputBytes = 64
	result, err := runner.Run(t.Context(), CommandRequest{Target: "connextable/loki", Args: []string{"api", "loud"}})
	if err != nil || !result.Truncated || len(result.Output) != 64 {
		t.Fatalf("output bound: %#v %v", result, err)
	}
	runner.Config.Timeout = 20 * time.Millisecond
	result, err = runner.Run(t.Context(), CommandRequest{Target: "connextable/loki", Args: []string{"api", "sleep"}})
	if err != nil || !result.TimedOut || result.ExitCode != 124 {
		t.Fatalf("timeout: %#v %v", result, err)
	}
}

func TestCommandRunnerRedactsTokenAndReturnsStableStartFailure(t *testing.T) {
	runner, _ := commandRunnerFixture(t)
	result, err := runner.Run(t.Context(), CommandRequest{Target: "connextable/loki", Args: []string{"api", "leak"}})
	if err != nil || result.ExitCode != 7 || result.Output != "[REDACTED]" ||
		strings.Contains(string(result.Raw), "installation-token") {
		t.Fatalf("token leak: %#v %v", result, err)
	}
	runner.Config.Binary = filepath.Join(t.TempDir(), "missing-gh")
	result, err = runner.Run(t.Context(), CommandRequest{Target: "connextable/loki", Args: []string{"issue", "list"}})
	if err == nil || err.Error() != "GitHub command could not be started" ||
		strings.Contains(err.Error(), runner.Config.Binary) || result.Output != "" {
		t.Fatalf("unstable start error: %#v %v", result, err)
	}
}
