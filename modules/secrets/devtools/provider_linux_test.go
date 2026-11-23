//go:build linux

package devtools

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestProviderRequiresReviewedIdentityAndJSON(t *testing.T) {
	for _, test := range []struct {
		name, version, commit string
		protocol              int
		accepted              bool
	}{
		{"reviewed", "0.50.0", "237f87813c06cd86a8ae89d1f4f6526c8ff50aff", 26, true},
		{"previous", "0.37.0", "f8c22ac3674a14c8d57ed919e866fc104d6d7b5f", 19, false},
		{"wrong-commit", "0.50.0", "unknown", 26, false},
		{"wrong-version", "0.36.3", "237f87813c06cd86a8ae89d1f4f6526c8ff50aff", 26, false},
		{"wrong-protocol", "0.50.0", "237f87813c06cd86a8ae89d1f4f6526c8ff50aff", 19, false},
	} {
		t.Run(test.name, func(t *testing.T) {
			dir := t.TempDir()
			binary := filepath.Join(dir, "provider")
			body := fmt.Sprintf(`#!/bin/sh
test "$1 $2 $3" = "--format json --isolated-root" || exit 2
case "$5 $6" in
  "version ") printf '%%s\n' '{"schema_version":1,"ok":true,"data":{"version":%q,"commit":%q,"protocol_version":%d}}' ;;
  "vault status") printf '%%s\n' '{"schema_version":1,"ok":true,"data":{"item":{"state":"uninitialized"}}}' ;;
  *) exit 2 ;;
esac
`, test.version, test.commit, test.protocol)
			if err := os.WriteFile(binary, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			p, err := New(t.Context(), binary, filepath.Join(dir, "state"))
			if (err == nil) != test.accepted {
				t.Fatalf("identity accepted = %v, want %v", err == nil, test.accepted)
			}
			if test.accepted {
				status, err := p.ProtectionStatus(t.Context())
				if err != nil || status.State != "uninitialized" {
					t.Fatal("JSON status unavailable:", err)
				}
			}
		})
	}
}

func TestRealProviderReadsAllMetadataPages(t *testing.T) {
	binary := os.Getenv("LOKI_SECRET_TEST_DEVTOOLS")
	if binary == "" {
		t.Skip("native values suite supplies the pinned provider")
	}
	root := t.TempDir()
	p, err := New(t.Context(), binary, filepath.Join(root, "dvt"))
	if err != nil {
		t.Fatal(err)
	}
	const app = "app-123e4567e89b42d3a456426614174441"
	revision, err := p.Revision(t.Context(), app, "common")
	if err != nil {
		t.Fatal(err)
	}
	var contents strings.Builder
	var names []string
	for index := range 60 {
		name := fmt.Sprintf("PUBLIC_%02d", index)
		names = append(names, name)
		fmt.Fprintf(&contents, "%s=fixture\n", name)
	}
	path := filepath.Join(root, "values.env")
	if err := os.WriteFile(path, []byte(contents.String()), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := p.Import(t.Context(), app, "common", path, revision, "123e4567-e89b-42d3-a456-426614174443", "", names, false); err != nil {
		t.Fatal(err)
	}
	metadata, err := p.Metadata(t.Context(), app, "common", "variable")
	if err != nil || metadata.Profile != app || len(metadata.Items) != len(names) ||
		metadata.Items[len(names)-1].Name != names[len(names)-1] {
		t.Fatalf("metadata returned %d of %d values: %v", len(metadata.Items), len(names), err)
	}
}
