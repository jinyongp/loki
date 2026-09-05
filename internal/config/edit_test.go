package config

import (
	"bytes"
	"os"
	"path/filepath"
	"strings"
	"sync"
	"testing"
)

func TestProcessLimitEditPreservesTOML(t *testing.T) {
	source := []byte("# limits\n'max_action_processes' = 12 # preserve this\ntext = '''\n[executables]\nmax_action_processes = 1\n'''\n[checks.example]\ncommand = ['true']\n")
	updated, err := RenderProcessLimit(source, "max-action-processes", 16)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Contains(updated, []byte("= 16 # preserve this")) || !bytes.Contains(updated, []byte("max_action_processes = 1")) {
		t.Fatalf("unexpected edit %s", updated)
	}
	if _, err = RenderProcessLimit(source, "max-action-processes", 2); err == nil {
		t.Fatal("per-profile limit exceeds total")
	}
	updated, err = RenderProcessLimit(source, "max-action-processes-per-profile", 8)
	if err != nil {
		t.Fatal(err)
	}
	parsed, err := Parse(updated)
	if err != nil || parsed.MaxActionProcessesPerProfile != 8 {
		t.Fatalf("inserted root setting %v %v", parsed, err)
	}
}

func TestExecutableEditPreservesOtherTables(t *testing.T) {
	for _, table := range []string{"[executables]\nold = '/usr/bin/old'\n", "executables.old = '/usr/bin/old'\n", "executables = {old='/usr/bin/old'}\n", "['executables'] # comment\nold = '/usr/bin/old'\n", ""} {
		source := []byte("# keep\n" + table + "\n[checks.example]\ncommand = ['true']\n")
		updated, err := RenderExecutables(source, map[string]string{"custom": "/opt/a\"b/tool"})
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := Parse(updated)
		if err != nil || parsed.Executables["custom"] != "/opt/a\"b/tool" || len(parsed.Executables) != 1 {
			t.Fatalf("executables %s %v", updated, err)
		}
		if !strings.Contains(string(updated), "[checks.example]\ncommand = ['true']") {
			t.Fatal("other configuration changed")
		}
	}
}

func TestConfigurationEditAtomicOwnershipAndConcurrency(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.toml")
	if err := os.WriteFile(path, []byte("# fixture\n"), 0640); err != nil {
		t.Fatal(err)
	}
	var workers sync.WaitGroup
	for _, setting := range []struct {
		name  string
		value int
	}{{"max-action-processes", 16}, {"max-action-processes-per-profile", 8}} {
		workers.Go(func() {
			if _, err := Edit(t.Context(), path, func(source []byte) ([]byte, error) { return RenderProcessLimit(source, setting.name, setting.value) }); err != nil {
				t.Error(err)
			}
		})
	}
	workers.Wait()
	parsed, err := Load(path)
	if err != nil || parsed.MaxActionProcesses != 16 || parsed.MaxActionProcessesPerProfile != 8 {
		t.Fatalf("lost update %v %v", parsed, err)
	}
	before, _ := os.ReadFile(path)
	if _, err = Edit(t.Context(), path, func([]byte) ([]byte, error) { return []byte("malformed TOML"), nil }); err == nil {
		t.Fatal("invalid configuration published")
	}
	after, _ := os.ReadFile(path)
	if !bytes.Equal(before, after) {
		t.Fatal("failed edit changed configuration")
	}
	info, _ := os.Stat(path)
	if info.Mode().Perm() != 0640 {
		t.Fatal("mode changed")
	}
	os.Symlink(path, path+".link")
	if _, err = Edit(t.Context(), path+".link", func(data []byte) ([]byte, error) { return data, nil }); err == nil {
		t.Fatal("symlink accepted")
	}
}
