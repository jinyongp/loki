package management

import (
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"loki/internal/tools"
)

func TestBrowserFullTopologySeparatesNetworksAndCredentialOwners(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux full topology")
	}
	store := Store{Root: t.TempDir()}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	state.Config.Mode = tools.Full
	for _, id := range []tools.ID{"runtime-core", "browser", "github", "secrets"} {
		var requires []tools.ID
		if id != "runtime-core" {
			requires = []tools.ID{"runtime-core"}
		}
		installation := ownedFixtureTarget(t, store, LocalTarget(tools.Full), id, requires...)
		state.Installed[id] = installation
		generation, _ := store.Generation(installation.Artifact)
		payload := FullPayload{Schema: 1, Module: id, Release: Release, Target: installation.Artifact.Target, Images: map[string]string{}, Programs: map[string]string{}, Assets: map[string]string{}}
		if id == "runtime-core" {
			payload.Images["service"] = "example.com/core@sha256:" + strings.Repeat("a", 64)
		}
		if id == "browser" {
			payload.Images["browser"] = "example.com/browser@sha256:" + strings.Repeat("b", 64)
		}
		if err := atomicJSON(filepath.Join(generation, "full-runtime.json"), payload); err != nil {
			t.Fatal(err)
		}
	}
	state.Config.Tools = []tools.Selection{{ID: "browser", Enabled: true}, {ID: "github", Enabled: false}, {ID: "secrets", Enabled: false}}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	topology, err := store.FullTopology()
	if err != nil {
		t.Fatal(err)
	}
	for _, service := range topology.Services {
		for _, mount := range service.Mounts {
			if strings.Contains(mount.Target, "github") || strings.Contains(mount.Target, "secrets") || strings.Contains(mount.Target, "modules/git") || mount.Source == store.Root {
				t.Fatalf("browser-only service received unrelated authority: %+v", service)
			}
			if mount.Target == "/etc/loki/activation" && (!mount.ReadOnly || mount.Source != store.ControlDirectory()) {
				t.Fatal("activation bound as mutable file or root")
			}
		}
		if service.Name == "browser" && (service.UID != 10003 || len(service.Networks) != 1 || !strings.HasSuffix(service.Networks[0], "-browser-private")) {
			t.Fatalf("browser escaped separate role/network: %+v", service)
		}
		if service.Name == "browser-proxy" && (service.UID != 10005 || !slices.Contains(service.Networks, topology.Owner+"-outbound")) {
			t.Fatalf("proxy lost inspected egress: %+v", service)
		}
	}
}
