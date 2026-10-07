package management

import (
	"os"
	"path/filepath"
	"testing"
)

func TestDockerAPIReadinessChecksRequiredContract(t *testing.T) {
	for _, version := range []string{"1.46", "1.47", "1.52", "not-a-version"} {
		binary := filepath.Join(t.TempDir(), "docker")
		if err := os.WriteFile(binary, []byte("#!/bin/sh\nprintf '%s' '"+version+"'\n"), 0700); err != nil {
			t.Fatal(err)
		}
		err := checkDockerAPI(t.Context(), binary)
		wantOK := version == "1.47" || version == "1.52"
		if (err == nil) != wantOK {
			t.Fatalf("API %s: %v", version, err)
		}
	}
}
