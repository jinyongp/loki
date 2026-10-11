package main

import (
	"bytes"
	"encoding/json"
	"io"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"loki/internal/management"
	"loki/internal/tools"
)

func TestUninstallAllOfflineFullToolsNeedsNoRuntime(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("full tools require Linux")
	}
	store := management.Store{Root: t.TempDir()}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	state.Config.Mode = tools.Full
	artifact := tools.Artifact{Module: "browser", Release: management.Release, Target: management.LocalTarget(tools.Full), URL: "https://example.com/tool.zip", SHA256: strings.Repeat("b", 64), Bytes: 1, Format: "zip"}
	directory, err := store.Generation(artifact)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	owner, _ := json.Marshal(management.Owner{Schema: 1, Identity: artifact.Identity()})
	if err := os.WriteFile(filepath.Join(directory, ".loki-owner.json"), owner, 0600); err != nil {
		t.Fatal(err)
	}
	state.Installed["browser"] = management.Installation{Artifact: artifact, Manifest: tools.Manifest{Schema: 1, ID: "browser", Release: management.Release, Targets: []tools.Target{artifact.Target}, Tools: []string{}}}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(store.Root, "retained-data")
	if err := os.WriteFile(data, []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	// No executable, including sudo or Docker, is available to this command.
	t.Setenv("PATH", t.TempDir())
	var out bytes.Buffer
	if err := run(t.Context(), []string{"--host", "local", "--root", store.Root, "tools", "uninstall", "--all", "--json"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if singleJSON(t, out.Bytes())["data_retained"] != true {
		t.Fatal(out.String())
	}
	if _, err := os.Stat(directory); !os.IsNotExist(err) {
		t.Fatal("offline programs remain", err)
	}
	if raw, err := os.ReadFile(data); err != nil || string(raw) != "keep" {
		t.Fatal("offline removal changed retained data", err)
	}
}

func TestUninstallRequiresScopeBeforeHostAccess(t *testing.T) {
	for _, args := range [][]string{{"uninstall"}, {"uninstall", "--purge-data"}, {"uninstall", "unknown"}, {"uninstall", "browser", "--all"}, {"uninstall", "browser", "--purge-data"}} {
		root := filepath.Join(t.TempDir(), "untouched")
		var out bytes.Buffer
		command := append([]string{"--root", root, "--host", "ssh", "--address", "unreachable.invalid"}, args...)
		err := run(t.Context(), command, &out, io.Discard)
		if err == nil || !strings.Contains(err.Error(), "choose what to uninstall") || out.Len() != 0 {
			t.Fatalf("unscoped removal: %v, output %q, error %v", args, out.String(), err)
		}
		if _, err := os.Stat(root); !os.IsNotExist(err) {
			t.Fatal("invalid uninstall changed local state", err)
		}
	}
}

func TestUninstallOneToolPreservesOtherProgramsAndData(t *testing.T) {
	store := management.Store{Root: t.TempDir()}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	dirs := map[tools.ID]string{}
	for _, id := range []tools.ID{"browser", "worker"} {
		artifact := tools.Artifact{Module: id, Release: management.Release, Target: management.LocalTarget(tools.ProjectHost), URL: "https://example.com/tool.zip", SHA256: strings.Repeat("a", 64), Bytes: 1, Format: "zip"}
		manifest := tools.Manifest{Schema: 1, ID: id, Release: management.Release, Targets: []tools.Target{artifact.Target}, Tools: []string{}}
		dir, err := store.Generation(artifact)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.MkdirAll(dir, 0700); err != nil {
			t.Fatal(err)
		}
		owner, _ := json.Marshal(management.Owner{Schema: 1, Identity: artifact.Identity()})
		if err := os.WriteFile(filepath.Join(dir, ".loki-owner.json"), owner, 0600); err != nil {
			t.Fatal(err)
		}
		state.Installed[id] = management.Installation{Artifact: artifact, Manifest: manifest}
		dirs[id] = dir
	}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	data := filepath.Join(store.Root, "profile.txt")
	if err := os.WriteFile(data, []byte("keep user data"), 0600); err != nil {
		t.Fatal(err)
	}
	var out bytes.Buffer
	assertCommandOutput(t, store.Root, []string{"tools", "enable", "browser", "--json"}, true, "enabled")
	assertCommandOutput(t, store.Root, []string{"tools", "disable", "browser", "--json"}, true, "enabled")
	if _, err := os.Stat(dirs["browser"]); err != nil {
		t.Fatal("disable removed installed programs", err)
	}
	if err := run(t.Context(), []string{"--root", store.Root, "uninstall", "browser", "--json"}, &out, io.Discard); err != nil {
		t.Fatal(err)
	}
	if singleJSON(t, out.Bytes())["tool"] != "browser" {
		t.Fatal(out.String())
	}
	if _, err := os.Stat(dirs["browser"]); !os.IsNotExist(err) {
		t.Fatal("browser programs remain", err)
	}
	if _, err := os.Stat(dirs["worker"]); err != nil {
		t.Fatal("other programs were removed", err)
	}
	if raw, err := os.ReadFile(data); err != nil || string(raw) != "keep user data" {
		t.Fatal("user data changed", err)
	}
	assertCommandOutput(t, store.Root, []string{"tools", "uninstall", "--all", "--json"}, true, "tools_removed")
	if _, err := os.Stat(dirs["worker"]); !os.IsNotExist(err) {
		t.Fatal("explicit all did not remove remaining programs", err)
	}
	if _, err := os.Stat(data); err != nil {
		t.Fatal("explicit all removed user data", err)
	}
}
