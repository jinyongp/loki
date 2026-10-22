//go:build linux

package application

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestLegacyMigrationPreflightPreservesExistingValuesAndMutationHistory(t *testing.T) {
	for _, material := range []string{"none", "provider state", "request history", "unexpected metadata", "symlink"} {
		t.Run(material, func(t *testing.T) {
			root := t.TempDir()
			identity := Identity{UID: uint32(os.Getuid()), GID: uint32(os.Getgid()), ActiveGeneration: strings.Repeat("a", 32)}
			if identity.UID == 0 {
				identity.UID, identity.GID = 10006, 10006
			}
			generation := filepath.Join(root, "generations", identity.ActiveGeneration)
			for _, path := range []string{filepath.Join(generation, "dvt"), filepath.Join(generation, "metadata"), filepath.Join(generation, "metadata", "requests")} {
				if err := os.MkdirAll(path, 0700); err != nil {
					t.Fatal(err)
				}
				if err := os.Chown(path, int(identity.UID), int(identity.GID)); err != nil {
					t.Fatal(err)
				}
			}
			var retained string
			switch material {
			case "provider state":
				retained = filepath.Join(generation, "dvt", "opaque-state")
			case "request history":
				retained = filepath.Join(generation, "metadata", "requests", "spent.json")
			case "unexpected metadata":
				retained = filepath.Join(generation, "metadata", "retained-state")
			case "symlink":
				if err := os.Remove(filepath.Join(generation, "dvt")); err != nil {
					t.Fatal(err)
				}
				if err := os.Symlink(t.TempDir(), filepath.Join(generation, "dvt")); err != nil {
					t.Fatal(err)
				}
			}
			if retained != "" {
				if err := os.WriteFile(retained, []byte("synthetic-retained-state"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			err := RequireEmptyGeneration(root, identity)
			if material == "none" && err != nil || material != "none" && err == nil {
				t.Fatal("preflight did not distinguish empty from materialized generation", err)
			}
			if retained != "" {
				body, err := os.ReadFile(retained)
				if err != nil || string(body) != "synthetic-retained-state" {
					t.Fatal("preflight changed retained application state", err)
				}
			}
		})
	}
}
