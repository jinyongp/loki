package commands

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestNodeCommands(t *testing.T) {
	home, err := os.UserHomeDir()
	if err != nil {
		t.Fatal(err)
	}
	fnm := filepath.Join(home, ".local", "share", "fnm")
	entries, err := os.ReadDir(filepath.Join(fnm, "node-versions"))
	if err != nil || len(entries) == 0 {
		if os.Getenv("LOKI_REQUIRE_NODE_COMMAND_TESTS") == "1" {
			t.Fatal("required development FNM installation unavailable")
		}
		t.Skip("optional development FNM installation unavailable")
	}
	version := ""
	for _, entry := range entries {
		if entry.IsDir() && nodePattern.MatchString(entry.Name()) {
			version = entry.Name()
		}
	}
	if version == "" {
		t.Fatal("no development Node version")
	}
	c := fixture(t)
	c.Environment = map[string]string{"FNM_DIR": fnm, "COREPACK_ENABLE_NETWORK": "0"}
	for _, r := range []Request{
		{Action: "exec", Executable: stringPointer("node"), Arguments: []string{"--version"}, CWD: ".", NodeVersion: &version, Timeout: 10},
		{Action: "npm", Arguments: []string{"--version"}, CWD: ".", NodeVersion: &version, Timeout: 10},
		{Action: "fnm", Arguments: []string{"--version"}, Timeout: 10},
	} {
		result, err := c.Run(t.Context(), r)
		if err != nil || result["exit_code"] != 0 || strings.TrimSpace(result["output"].(string)) == "" {
			t.Fatal(r.Action, result, err)
		}
	}
}
func stringPointer(value string) *string { return &value }
