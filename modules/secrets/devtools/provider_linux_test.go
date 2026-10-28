//go:build linux

package devtools

import (
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestProviderRequiresReviewedIdentityAndJSON(t *testing.T) {
	for _, test := range []struct {
		name, version, commit string
		protocol              int
		accepted              bool
	}{
		{"reviewed", "0.37.0", "f8c22ac3674a14c8d57ed919e866fc104d6d7b5f", 19, true},
		{"previous", "0.34.0", "ff8edcca1ba5bae4fdd7981779bc4f0c5e557438", 14, false},
		{"wrong-commit", "0.37.0", "unknown", 19, false},
		{"wrong-version", "0.36.3", "f8c22ac3674a14c8d57ed919e866fc104d6d7b5f", 19, false},
		{"wrong-protocol", "0.37.0", "f8c22ac3674a14c8d57ed919e866fc104d6d7b5f", 18, false},
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
