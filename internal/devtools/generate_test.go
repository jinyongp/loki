package devtools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseVersionRequiresPinnedRelease(t *testing.T) {
	if _, err := ParseVersion([]byte(`{"schema_version":1,"ok":true,"data":{"version":"0.9.0","commit":"test"}}`)); err != nil {
		t.Fatal(err)
	}
	if _, err := ParseVersion([]byte(`{"schema_version":1,"ok":true,"data":{"version":"0.8.2","commit":"test"}}`)); err == nil {
		t.Fatal("version drift accepted")
	}
}

func TestGenerateCatalogChecksVersionAndNormalizes(t *testing.T) {
	dir := t.TempDir()
	catalog := filepath.Join(dir, "catalog.json")
	if err := os.WriteFile(catalog, embeddedCatalog, 0600); err != nil {
		t.Fatal(err)
	}
	version := filepath.Join(dir, "version.json")
	if err := os.WriteFile(version, []byte(`{"schema_version":1,"ok":true,"data":{"version":"0.9.0","commit":"test"}}`), 0600); err != nil {
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
