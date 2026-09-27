package connect

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func catalogFixture() Catalog {
	asset := func(name string) Asset {
		return Asset{
			SourceURL:    "https://example.com/releases/v1.2.3/" + name,
			SourceSHA256: strings.Repeat("a", 64),
			SourceLength: 10,
			MirrorAsset:  "loki-" + name,
		}
	}
	return Catalog{
		SchemaVersion: 1,
		Helpers: []Helper{{
			ID: "openai-tunnel-client", Provider: "openai", Version: "1.2.3", Platform: "windows-amd64",
			Executable:     "tunnel-client.exe",
			ArchiveMembers: []string{"NOTICE", "tunnel-client.exe"},
			Archive:        asset("client.zip"),
			LicenseReport:  asset("licenses.txt"),
			Notice:         asset("NOTICE"),
			SPDX:           asset("client.spdx.json"),
		}},
	}
}

func TestCatalogNormalizesAndRoundTrips(t *testing.T) {
	catalog, err := NewCatalog(catalogFixture())
	if err != nil {
		t.Fatal(err)
	}
	raw, err := EncodeCatalog(catalog)
	if err != nil {
		t.Fatal(err)
	}
	loaded, err := LoadCatalog(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(loaded.Helpers) != 1 || loaded.Helpers[0].ArchiveMembers[0] != "NOTICE" {
		t.Fatalf("unexpected catalog %#v", loaded)
	}
}

func TestCatalogRejectsMutableOrAmbiguousHelperInputs(t *testing.T) {
	tests := map[string]func(*Catalog){
		"latest-url":         func(c *Catalog) { c.Helpers[0].Archive.SourceURL = "https://example.com/releases/latest/client.zip" },
		"bad-digest":         func(c *Catalog) { c.Helpers[0].Archive.SourceSHA256 = "abc" },
		"nested-mirror":      func(c *Catalog) { c.Helpers[0].Archive.MirrorAsset = "../client.zip" },
		"missing-executable": func(c *Catalog) { c.Helpers[0].ArchiveMembers = []string{"NOTICE"} },
		"duplicate-member":   func(c *Catalog) { c.Helpers[0].ArchiveMembers = []string{"tunnel-client.exe", "tunnel-client.exe"} },
		"duplicate-mirror":   func(c *Catalog) { c.Helpers[0].Notice.MirrorAsset = c.Helpers[0].Archive.MirrorAsset },
	}
	for name, mutate := range tests {
		t.Run(name, func(t *testing.T) {
			fixture := catalogFixture()
			mutate(&fixture)
			if _, err := NewCatalog(fixture); err == nil {
				t.Fatal("expected validation failure")
			}
		})
	}
}

func TestCheckedInWindowsHelperCatalogIsValid(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join(root, "packaging", "windows", "connect-helpers.json"))
	if err != nil {
		t.Fatal(err)
	}
	catalog, err := LoadCatalog(raw)
	if err != nil {
		t.Fatal(err)
	}
	if len(catalog.Helpers) != 1 || catalog.Helpers[0].ID != "openai-tunnel-client" {
		t.Fatalf("unexpected checked-in catalog %#v", catalog)
	}
}
