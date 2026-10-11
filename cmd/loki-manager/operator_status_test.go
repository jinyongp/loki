package main

import (
	"bytes"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestOperatorStatusPreservesTargetAndUnknownRuntime(t *testing.T) {
	root := filepath.Join(t.TempDir(), "untouched")
	t.Setenv("CODEX_HOME", t.TempDir())
	var out bytes.Buffer
	if err := run(t.Context(), []string{"--root", root, "status", "--json"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	value := singleJSON(t, out.Bytes())
	if value["cli_version"] == nil || value["execution_host"] == nil || value["next_actions"] == nil || value["state_root"] != root {
		t.Fatal("status omitted operator context", out.String())
	}
	if value["ready"] != nil {
		t.Fatal("passive status claimed readiness")
	}
	if _, err := os.Stat(root); !os.IsNotExist(err) {
		t.Fatal("status created management state", err)
	}
}

func TestUnreachableHostStatusRetainsTargetAndDoesNotClaimEmptyTools(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("uses an isolated POSIX SSH fixture")
	}
	bin := t.TempDir()
	if err := os.WriteFile(filepath.Join(bin, "ssh"), []byte("#!/bin/sh\nexit 7\n"), 0700); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	t.Setenv("CODEX_HOME", t.TempDir())
	for _, structured := range []bool{false, true} {
		var out bytes.Buffer
		args := []string{"--root", t.TempDir(), "--host", "ssh", "--address", "fixture@host", "status"}
		if structured {
			args = append(args, "--json")
		}
		if err := run(t.Context(), args, &out, io.Discard); err == nil {
			t.Fatal("unreachable host succeeded")
		}
		if structured {
			value := singleJSON(t, out.Bytes())
			if value["tools"] != nil || value["execution_host"].(map[string]any)["host"].(map[string]any)["address"] != "fixture@host" {
				t.Fatal("unreachable host state was lost", out.String())
			}
		} else if !strings.Contains(out.String(), "Tools: unavailable") || !strings.Contains(out.String(), "SSH: fixture@host") || strings.Contains(out.String(), "none installed") {
			t.Fatal("unavailable tools misreported", out.String())
		}
	}
}
