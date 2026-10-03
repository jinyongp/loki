package management

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"loki/internal/tools"
)

func TestFullPayloadRejectsMutableImagesAndEscapingPrograms(t *testing.T) {
	artifact := tools.Artifact{Module: "runtime-core", Release: Release, Target: tools.Target{OS: "linux", Arch: "amd64", Mode: tools.Full}, URL: "https://example.com/core.zip", SHA256: strings.Repeat("a", 64), Bytes: 100, Format: "zip"}
	payload := FullPayload{Schema: 1, Module: artifact.Module, Release: artifact.Release, Target: artifact.Target, Images: map[string]string{"service": "example.com/core:latest"}, Programs: map[string]string{"loki": "bin/loki-worker"}, Assets: map[string]string{}}
	if err := payload.Validate(artifact); err == nil {
		t.Fatal("mutable image tag accepted")
	}
	payload.Images["service"] = "example.com/core@sha256:" + strings.Repeat("b", 64)
	if err := payload.Validate(artifact); err != nil {
		t.Fatal(err)
	}
	payload.Programs["loki"] = "../foreign"
	if err := payload.Validate(artifact); err == nil {
		t.Fatal("escaping program accepted")
	}
	payload.Programs["loki"] = "bin/loki-worker"
	payload.Images["browser"] = payload.Images["service"]
	if err := payload.Validate(artifact); err == nil {
		t.Fatal("module claimed another owner's service image")
	}
}

func TestFullResourcesUseOnlyActiveClosureAndRejectLinkedExecutable(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux full payload")
	}
	store := Store{Root: t.TempDir()}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	state.Config.Mode = tools.Full
	core := ownedFixtureTarget(t, store, LocalTarget(tools.Full), "runtime-core")
	state.Installed["runtime-core"] = core
	state.Installed["workspace"] = ownedFixtureTarget(t, store, LocalTarget(tools.Full), "workspace", "runtime-core")
	state.Installed["github"] = ownedFixtureTarget(t, store, LocalTarget(tools.Full), "github", "runtime-core")
	state.Config.Tools = []tools.Selection{{ID: "workspace", Enabled: true}, {ID: "github", Enabled: false}}
	for _, id := range []tools.ID{"runtime-core", "workspace"} {
		installation := state.Installed[id]
		generation, _ := store.Generation(installation.Artifact)
		payload := FullPayload{Schema: 1, Module: id, Release: Release, Target: installation.Artifact.Target, Images: map[string]string{}, Programs: map[string]string{}, Assets: map[string]string{}}
		if id == "runtime-core" {
			if err := os.Mkdir(filepath.Join(generation, "bin"), 0755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(filepath.Join(generation, "bin/loki"), []byte("fixture"), 0755); err != nil {
				t.Fatal(err)
			}
			payload.Programs["loki"] = "bin/loki"
		}
		if err := atomicJSON(filepath.Join(generation, "full-runtime.json"), payload); err != nil {
			t.Fatal(err)
		}
	}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	resources, err := store.FullResources()
	if err != nil {
		t.Fatal(err)
	}
	if len(resources.Payloads) != 2 {
		t.Fatalf("disabled provider loaded: %+v", resources.Payloads)
	}
	program, err := resources.Program("runtime-core", "loki")
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(program); err != nil {
		t.Fatal(err)
	}
	foreign := filepath.Join(t.TempDir(), "foreign")
	if err := os.WriteFile(foreign, []byte("foreign"), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(foreign, program); err != nil {
		t.Fatal(err)
	}
	if _, err := store.FullResources(); err == nil {
		t.Fatal("linked executable escaped generation")
	}
}
