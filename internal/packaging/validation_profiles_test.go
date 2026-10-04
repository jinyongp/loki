package packaging_test

import (
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"
)

func TestExactCandidateProfileKeepsRequiredBoundaries(t *testing.T) {
	root := filepath.Join("..", "..")
	read := func(name string) string {
		t.Helper()
		raw, err := os.ReadFile(filepath.Join(root, "scripts", "verify", name))
		if err != nil {
			t.Fatal(err)
		}
		return string(raw)
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

func TestValidationProfilesExecuteEachCheckAndReportFailures(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("POSIX validation scripts")
	}
	for _, test := range []struct {
		name, script, failure string
		args                  []string
		want                  []string
	}{
		{name: "source", script: "verify-source.sh", want: []string{
			"go test -vet=off ./...", "go vet ./...", "go build ./cmd/...",
			"go run ./tools/archcheck", "go mod tidy -diff", "git diff --check",
		}},
		{name: "source-failure", script: "verify-source.sh", failure: "test", want: []string{
			"go test -vet=off ./...", "go vet ./...", "go build ./cmd/...",
			"go run ./tools/archcheck", "go mod tidy -diff", "git diff --check",
		}},
		{name: "race", script: "verify-race.sh", want: []string{"go test -race -vet=off ./..."}},
		{name: "preflight", script: "verify-preflight.sh", want: []string{"source", "race"}},
		{name: "preflight-oci", script: "verify-preflight.sh", args: []string{"--oci"}, want: []string{"source", "race", "oci"}},
		{name: "preflight-oci-failure", script: "verify-preflight.sh", args: []string{"--oci"}, failure: "oci", want: []string{"source", "race", "oci"}},
		{name: "preflight-source-failure", script: "verify-preflight.sh", args: []string{"--oci"}, failure: "source", want: []string{"source", "race", "oci"}},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			bin := filepath.Join(root, "bin")
			verify := filepath.Join(root, "scripts", "verify")
			for _, dir := range []string{bin, verify} {
				if err := os.MkdirAll(dir, 0755); err != nil {
					t.Fatal(err)
				}
			}
			write := func(path, body string) {
				t.Helper()
				if err := os.WriteFile(path, []byte(body), 0755); err != nil {
					t.Fatal(err)
				}
			}
			script, err := os.ReadFile(filepath.Join("..", "..", "scripts", "verify", test.script))
			if err != nil {
				t.Fatal(err)
			}
			write(filepath.Join(verify, test.script), string(script))
			for _, tool := range []string{"go", "git"} {
				write(filepath.Join(bin, tool), "#!/bin/sh\nprintf '%s\\n' '"+tool+" '"+`"$*" >> "$LOKI_VERIFY_LOG"`+"\n"+`test "$1" != "$LOKI_VERIFY_FAIL"`+"\n")
			}
			write(filepath.Join(bin, "rg"), "#!/bin/sh\nexit 0\n")
			if test.script == "verify-preflight.sh" {
				for name, label := range map[string]string{"verify-source.sh": "source", "verify-race.sh": "race", "accept-oci-jobs.sh": "oci"} {
					write(filepath.Join(verify, name), "#!/bin/sh\nprintf '%s\\n' '"+label+"' >> \"$LOKI_VERIFY_LOG\"\ntest '"+label+"' != \"$LOKI_VERIFY_FAIL\"\n")
				}
			}
			log := filepath.Join(root, "commands")
			t.Setenv("PATH", bin+string(os.PathListSeparator)+os.Getenv("PATH"))
			t.Setenv("LOKI_TEST_RG", filepath.Join(bin, "rg"))
			t.Setenv("LOKI_VERIFY_LOG", log)
			t.Setenv("LOKI_VERIFY_FAIL", test.failure)
			args := append([]string{filepath.Join(verify, test.script)}, test.args...)
			output, err := exec.Command("sh", args...).CombinedOutput()
			if (err != nil) != (test.failure != "") {
				t.Fatalf("failure=%q err=%v\n%s", test.failure, err, output)
			}
			commands, err := os.ReadFile(log)
			if err != nil {
				t.Fatal(err)
			}
			if got := strings.Split(strings.TrimSpace(string(commands)), "\n"); !reflect.DeepEqual(got, test.want) {
				t.Fatalf("commands = %#v, want %#v", got, test.want)
			}
		})
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
		"go test ./...",
		"go test -race ./internal/tools ./internal/management ./internal/config ./cmd/loki-manager",
		"accept_full_workspace_candidate.py",
		"release_gate.py gate",
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
