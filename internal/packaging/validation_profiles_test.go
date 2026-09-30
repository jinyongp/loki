package packaging_test

import (
	"fmt"
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
		"ripgrep is required; set LOKI_TEST_RG to a pinned executable",
	} {
		if !strings.Contains(source, required) {
			t.Errorf("source profile lacks %q", required)
		}
	}
	if strings.Contains(source, "accept-oci-jobs.sh") {
		t.Fatal("source profile must not silently include fixture-gated integration")
	}

	race := read("verify-race.sh")
	for _, required := range []string{"profile=race", "go test -race ./... -count=1", "integration=excluded", "ripgrep is required; set LOKI_TEST_RG to a pinned executable"} {
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
		"prepare-candidate.sh",
		"accept-oci-jobs.sh",
	} {
		path := filepath.Join(root, "scripts", "verify", name)
		if output, err := exec.Command("sh", "-n", path).CombinedOutput(); err != nil {
			t.Errorf("%s shell syntax: %v\n%s", name, err, output)
		}
	}
}

func TestCandidatePreparationRestoresExecutableModes(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("candidate preparation is a POSIX release-engineering helper")
	}
	root := filepath.Join("..", "..")
	script := filepath.Join(root, "scripts", "verify", "prepare-candidate.sh")

	for _, withInstaller := range []bool{false, true} {
		t.Run(fmt.Sprintf("installer-%v", withInstaller), func(t *testing.T) {
			candidate := t.TempDir()
			required := []string{
				filepath.Join("inputs", "loki"),
				filepath.Join("inputs", "loki-bootstrap"),
				filepath.Join("inputs", "loki-windows-amd64.exe"),
			}
			files := append([]string(nil), required...)
			if withInstaller {
				files = append(files, "install.sh")
			}
			for _, relative := range files {
				path := filepath.Join(candidate, relative)
				if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte("fixture"), 0600); err != nil {
					t.Fatal(err)
				}
			}
			if output, err := exec.Command("sh", script, candidate).CombinedOutput(); err != nil {
				t.Fatalf("prepare candidate: %v\n%s", err, output)
			}
			for _, relative := range files {
				info, err := os.Stat(filepath.Join(candidate, relative))
				if err != nil {
					t.Fatal(err)
				}
				if info.Mode().Perm() != 0755 {
					t.Errorf("%s mode = %o, want 0755", relative, info.Mode().Perm())
				}
			}
		})
	}
}
