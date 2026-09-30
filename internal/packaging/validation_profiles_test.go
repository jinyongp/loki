package packaging_test

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestValidationProfilesKeepRequiredBoundaries(t *testing.T) {
	root := filepath.Join("..", "..")
	read := func(name string) string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(root, "scripts", "verify", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
	}

	source := read("verify-source.sh")
	for _, required := range []string{
		"profile=source",
		"go test ./... -count=1",
		"go vet ./...",
		"go build ./...",
		"go run ./tools/archcheck",
		"go mod tidy -diff",
		"git diff --check",
		"integration=excluded",
		"all independent source checks were attempted",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("source profile lacks %q", required)
		}
	}
	if strings.Contains(source, "accept-oci-jobs.sh") {
		t.Fatal("source profile must not silently include fixture-gated integration")
	}

	race := read("verify-race.sh")
	for _, required := range []string{"profile=race", "go test -race ./... -count=1", "integration=excluded"} {
		if !strings.Contains(race, required) {
			t.Errorf("race profile lacks %q", required)
		}
	}

	preflight := read("verify-preflight.sh")
	for _, required := range []string{
		"profile=preflight",
		"requires Linux because real OCI integration is mandatory",
		"verify-source.sh",
		"verify-race.sh",
		"accept-oci-jobs.sh",
		"all independent preflight profiles were attempted",
		"remaining=exact-candidate,windows-wsl,publication",
	} {
		if !strings.Contains(preflight, required) {
			t.Errorf("preflight profile lacks %q", required)
		}
	}

	candidate := read("verify-release.sh")
	for _, required := range []string{
		"profile=exact-candidate",
		"image must be pinned by sha256 digest",
		"LOKI_SIGNING_KEY_FILE is required for signing acceptance",
		"LOKI_OCI_ACCEPTANCE_IMAGE=",
		"accept-oci-jobs.sh",
		"accept-authority-matrix.sh",
		"accept-bootstrap.sh",
		"accept-candidate.sh",
		"accept-compose.sh",
		"all independent exact-candidate domains were attempted",
		"remaining=windows-wsl,publication",
	} {
		if !strings.Contains(candidate, required) {
			t.Errorf("exact-candidate profile lacks %q", required)
		}
	}
}

func TestReleaseWorkflowUsesCanonicalSourceAndRaceProfiles(t *testing.T) {
	root := filepath.Join("..", "..")
	raw, err := os.ReadFile(filepath.Join(root, ".github", "workflows", "release.yml"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(raw)
	for _, required := range []string{
		"Run deterministic source profile",
		"sh ./scripts/verify/verify-source.sh",
		"Run Go race profile",
		"sh ./scripts/verify/verify-race.sh",
		"Run real OCI job acceptance",
		"./scripts/verify/accept-oci-jobs.sh",
	} {
		if !strings.Contains(text, required) {
			t.Errorf("release workflow lacks validation profile wiring %q", required)
		}
	}
}

func TestValidationProfileShellSyntax(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX validation scripts are Linux release-engineering tools")
	}
	root := filepath.Join("..", "..")
	for _, name := range []string{
		"verify-source.sh",
		"verify-race.sh",
		"verify-preflight.sh",
		"verify-release.sh",
		"accept-oci-jobs.sh",
	} {
		path := filepath.Join(root, "scripts", "verify", name)
		if output, err := exec.Command("sh", "-n", path).CombinedOutput(); err != nil {
			t.Errorf("%s shell syntax: %v\n%s", name, err, output)
		}
	}
}
