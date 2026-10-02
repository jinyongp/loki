package githubapp

import (
	"context"
	"os"
	"testing"
	"time"

	"loki/internal/process"
)

func TestRealDelegatedGitHubCLIConfiguration(t *testing.T) {
	if os.Getenv("LOKI_GITHUB_COMMAND_ACCEPTANCE") != "1" {
		t.Skip("requires the packaged CLI and production GitHub tmpfs")
	}
	if os.Geteuid() != 0 {
		t.Fatal("acceptance requires the privileged runtime identity")
	}
	runner := &CommandRunner{
		Config: CommandConfig{
			Binary: "/usr/bin/gh", CWD: "/", TempDir: "/var/tmp/loki/github",
			Identity: &process.Identity{UID: 10000, GID: 10000, Groups: []uint32{10001}},
			Timeout:  5 * time.Second, MaxInputBytes: 4096, MaxOutputBytes: 16384,
		},
		Tokens: repositoryTokenFunc(func(context.Context, string) (string, error) {
			return "synthetic-installation-token", nil
		}),
	}
	if err := runner.Check(t.Context()); err != nil {
		t.Fatal(err)
	}
	result, err := runner.Run(t.Context(), CommandRequest{Target: "example-org/loki", Args: []string{"api", "--help"}})
	if err != nil || result.ExitCode != 0 || result.TimedOut || result.Truncated {
		t.Fatalf("CLI startup: %#v %v", result, err)
	}
	entries, err := os.ReadDir(runner.Config.TempDir)
	if err != nil || len(entries) != 0 {
		t.Fatalf("configuration directories retained: %d %v", len(entries), err)
	}
}
