package devtools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseVersionAcceptsCompatibleReleases(t *testing.T) {
	for _, release := range []string{"0.9.0", "0.10.0", "1.0.0-rc.1+build.7"} {
		raw := []byte(`{"schema_version":1,"ok":true,"data":{"version":"` + release + `","commit":"test","protocol_version":3}}`)
		if _, err := ParseVersion(raw); err != nil {
			t.Fatalf("release %q: %v", release, err)
		}
	}
	for _, release := range []string{"", "latest", "01.2.3", "1.2"} {
		raw := []byte(`{"schema_version":1,"ok":true,"data":{"version":"` + release + `","commit":"test","protocol_version":3}}`)
		if _, err := ParseVersion(raw); err == nil {
			t.Fatalf("invalid release %q accepted", release)
		}
	}
}

func TestGenerateCatalogChecksVersionAndNormalizes(t *testing.T) {
	dir := t.TempDir()
	catalog := filepath.Join(dir, "catalog.json")
	if err := os.WriteFile(catalog, embeddedCatalog, 0600); err != nil {
		t.Fatal(err)
	}
	version := filepath.Join(dir, "version.json")
	if err := os.WriteFile(version, []byte(`{"schema_version":1,"ok":true,"data":{"version":"0.9.0","commit":"test","protocol_version":3}}`), 0600); err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(dir, "devtools")
	body := "#!/bin/sh\ncase \"$1 $2\" in\n  \"version \" ) cat \"" + version + "\" ;;\n  \"schema --all\" ) cat \"" + catalog + "\" ;;\n  * ) exit 2 ;;\nesac\n"
	if err := os.WriteFile(script, []byte(body), 0700); err != nil {
		t.Fatal(err)
	}
	generated, err := GenerateCatalog(context.Background(), script)
	if err != nil {
		t.Fatal(err)
	}
	if len(generated) == 0 || generated[len(generated)-1] != '\n' {
		t.Fatal("generated catalog needs one final newline")
	}
	if strings.Contains(string(generated), "\r") {
		t.Fatal("generated catalog retained CRLF")
	}
	if _, err = ParseCatalog(generated); err != nil {
		t.Fatal(err)
	}
}
