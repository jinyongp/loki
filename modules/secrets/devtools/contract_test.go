package devtools

import "testing"

func TestDevtoolsRoleAllowlistCannotExecuteOrReadValues(t *testing.T) {
	for _, authority := range []DevtoolsAuthority{DevtoolsMetadata, DevtoolsAdmin, DevtoolsJobs, "unknown"} {
		for _, command := range []string{"run", "command run", "process start", "process restart", "task claim", "expose", "update", "dashboard", "var get", "secret get", "profile purge"} {
			if AllowsDevtoolsCommand(authority, command) {
				t.Fatalf("%s can %s", authority, command)
			}
		}
	}
	for _, test := range []struct {
		role    DevtoolsAuthority
		command string
	}{{DevtoolsMetadata, "values revision"}, {DevtoolsAdmin, "secret generate"}, {DevtoolsJobs, "values execution deliver"}} {
		if !AllowsDevtoolsCommand(test.role, test.command) {
			t.Fatal("required typed command denied", test)
		}
		for _, other := range []DevtoolsAuthority{DevtoolsMetadata, DevtoolsAdmin, DevtoolsJobs} {
			if other == test.role || test.command == "values revision" && other == DevtoolsAdmin {
				continue
			}
			if AllowsDevtoolsCommand(other, test.command) {
				t.Fatalf("cross-role authority: %s %s", other, test.command)
			}
		}
	}
}
