package management

import (
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"

	"loki/internal/tools"
)

func TestApplicationFullTopologyHasOneIsolatedValueOwner(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("Linux service contract")
	}
	store := Store{Root: t.TempDir()}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	state.Config.Mode = tools.Full
	for _, id := range []tools.ID{"runtime-core", "secrets"} {
		var required []tools.ID
		if id == "secrets" {
			required = []tools.ID{"runtime-core"}
		}
		installed := ownedFixtureTarget(t, store, LocalTarget(tools.Full), id, required...)
		state.Installed[id] = installed
		generation, err := store.Generation(installed.Artifact)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(generation, "program"), []byte("fixture"), 0755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(generation, "contract.json"), []byte("{}"), 0644); err != nil {
			t.Fatal(err)
		}
		payload := FullPayload{Schema: 1, Module: id, Release: Release, Target: installed.Artifact.Target, Images: map[string]string{}, Programs: map[string]string{}, Assets: map[string]string{}}
		if id == "runtime-core" {
			payload.Images["service"] = "example.com/core@sha256:" + strings.Repeat("a", 64)
			payload.Programs["loki"], payload.Programs["devtools"] = "program", "program"
			payload.Assets["execution-contract"] = "contract.json"
		} else {
			payload.Programs["secret-broker"] = "program"
		}
		if err := atomicJSON(filepath.Join(generation, "full-runtime.json"), payload); err != nil {
			t.Fatal(err)
		}
	}
	state.Config.Tools = []tools.Selection{{ID: "secrets", Enabled: true}}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	topology, err := store.FullTopology()
	if err != nil {
		t.Fatal(err)
	}
	if len(topology.Services) != 2 {
		t.Fatalf("app-only composition created unrelated services: %+v", topology.Services)
	}
	for _, service := range topology.Services {
		switch service.Name {
		case "secrets":
			if service.UID != 10006 || service.GID != 10006 || len(service.Networks) != 0 || !slices.Contains(service.SupplementaryGroups, uint32(10001)) {
				t.Fatal("incorrect broker UID, network or metadata group")
			}
			for _, mount := range service.Mounts {
				if !slices.Contains([]string{"/var/lib/loki/secrets", "/run/loki/secrets", "/opt/loki/modules/runtime-core", "/opt/loki/modules/secrets"}, mount.Target) {
					t.Fatalf("broker received unrelated authority: %+v", mount)
				}
			}
		case "mcp":
			found := false
			for _, mount := range service.Mounts {
				if mount.Target == "/run/loki/secrets/metadata" {
					found = mount.ReadOnly
				}
				if mount.Target == "/var/lib/loki/secrets" || mount.Target == "/run/loki/secrets" || strings.HasSuffix(mount.Target, "/admin") || strings.HasSuffix(mount.Target, "/jobs") {
					t.Fatalf("agent received private value authority: %+v", mount)
				}
			}
			if !found {
				t.Fatal("agent lost its sole metadata channel")
			}
		default:
			t.Fatalf("unexpected service %s", service.Name)
		}
	}
	reservation, err := store.ReserveDeployment(topology.Resources.Plan)
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(reservation.Services, []string{"mcp", "secrets"}) {
		t.Fatalf("private service was not reserved: %+v", reservation)
	}
	// Existing reservations remain readable, including the dedicated owner.
	if _, err := store.Deployments(); err != nil {
		t.Fatal(err)
	}
	layoutDir, authDir := t.TempDir(), t.TempDir()
	bootstrap, err := topology.Bootstrap(uint32(os.Geteuid()), layoutDir, authDir, reservation.ID)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(bootstrap.Script, "'prepare-secrets'") || strings.Contains(bootstrap.Script, "'protection'") || strings.Contains(bootstrap.Script, "'unlock'") {
		t.Fatal("metadata preparation initialized or unlocked cryptography")
	}
	state.Config.Tools[0].Enabled = false
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	disabled, err := store.FullTopology()
	if err != nil || len(disabled.Services) != 0 {
		t.Fatal("disabled module kept services", err)
	}
}
