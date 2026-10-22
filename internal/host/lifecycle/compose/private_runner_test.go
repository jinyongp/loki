package compose

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPrivateHelperSeparatesMetadataAndSensitiveDiagnostics(t *testing.T) {
	for _, failure := range []bool{false, true} {
		t.Run(map[bool]string{false: "success", true: "failure"}[failure], func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "private-helper")
			body := "#!/bin/sh\nvalue=$(cat)\nprintf '%s' \"$value\" >&2\nprintf '{\"prepared\":true}\\n'\n"
			if failure {
				body += "exit 1\n"
			}
			if err := os.WriteFile(path, []byte(body), 0700); err != nil {
				t.Fatal(err)
			}
			const canary = "synthetic-private-input-canary"
			out, err := (ExecRunner{Executable: path}).RunPrivate(t.Context(), nil, strings.NewReader(canary))
			if failure {
				if err == nil || len(out) != 0 || strings.Contains(err.Error(), canary) {
					t.Fatalf("private failure exposed input: %q %v", out, err)
				}
			} else if err != nil || string(out) != "{\"prepared\":true}\n" {
				t.Fatalf("stderr corrupted metadata: %q %v", out, err)
			}
		})
	}
}
