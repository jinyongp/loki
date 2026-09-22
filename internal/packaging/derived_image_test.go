package packaging

import (
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestDerivedImageContractIsNarrowAndVersioned(t *testing.T) {
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join(root, "packaging", "images", "derived-image-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	var contract map[string]any
	if err = json.Unmarshal(raw, &contract); err != nil {
		t.Fatal(err)
	}
	if contract["version"] != float64(1) || contract["required_label"] != "io.loki.derived.base" {
		t.Fatalf("invalid derived image contract: %#v", contract)
	}
	preserve := contract["preserve"].(map[string]any)
	config := stringValues(preserve["config"].([]any))
	files := stringValues(preserve["files"].([]any))
	for _, required := range []string{"Entrypoint", "Cmd", "User", "WorkingDir", "Volumes"} {
		if !sliceHas(config, required) {
			t.Errorf("config invariant %q is missing", required)
		}
	}
	for _, required := range []string{"/opt/loki/bin/loki", "/opt/loki/bin/devtools", "/usr/bin/rg", "/usr/share/doc/loki/provenance.json"} {
		if !sliceHas(files, required) {
			t.Errorf("file invariant %q is missing", required)
		}
	}
	if got := strings.Join(stringValues(contract["extension_roots"].([]any)), " "); got != "/usr/local /opt/project" {
		t.Fatalf("extension roots = %s", got)
	}
}

func TestDerivedDockerfilePreservesLokiConfiguration(t *testing.T) {
	body := readOCIFile(t, filepath.Join("..", "..", "packaging", "images", "derived", "Dockerfile"))
	for _, required := range []string{"ARG LOKI_BASE", "FROM ${LOKI_BASE}", "io.loki.derived.base=\"${LOKI_BASE_ID}\""} {
		if !strings.Contains(body, required) {
			t.Errorf("derived Dockerfile missing %q", required)
		}
	}
	for _, forbidden := range []string{"ENTRYPOINT", "HEALTHCHECK", "VOLUME", "USER "} {
		if strings.Contains(body, forbidden) {
			t.Errorf("derived Dockerfile overrides %q", forbidden)
		}
	}
}

func TestDerivedImageValidatorAcceptsOnlyPreservedImages(t *testing.T) {
	fixture, err := os.ReadFile(filepath.Join("testdata", "fake-derived-docker"))
	if err != nil {
		t.Fatal(err)
	}
	fake := filepath.Join(t.TempDir(), "docker")
	writeExecutable(t, fake, string(fixture))
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "verify", "verify-loki-derived-image.sh"))
	if err != nil {
		t.Fatal(err)
	}
	run := func(want bool, args ...string) {
		t.Helper()
		cmd := exec.Command(script, args...)
		cmd.Env = append(os.Environ(), "LOKI_DOCKER="+fake)
		output, runErr := cmd.CombinedOutput()
		if want && runErr != nil {
			t.Fatalf("%v failed: %v\n%s", args, runErr, output)
		}
		if !want && runErr == nil {
			t.Fatalf("%v unexpectedly passed\n%s", args, output)
		}
	}
	run(true, "loki@sha256:base", "derived-good")
	run(false, "loki@sha256:base", "derived-base-bad")
	run(false, "loki@sha256:base", "derived-config-bad")
	run(false, "loki@sha256:base", "derived-files-bad")
}

func stringValues(values []any) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = value.(string)
	}
	return result
}

func sliceHas(values []string, wanted string) bool {
	for _, value := range values {
		if value == wanted {
			return true
		}
	}
	return false
}
