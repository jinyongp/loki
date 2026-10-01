package packaging

import (
	"context"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
)

func TestReleaseImageBuildsRunConcurrentlyAndRequireBothResults(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("candidate images are built on the Linux release runner")
	}
	var script string
	for _, step := range releaseWorkflowJobs(t)["build"].Steps {
		if step.ID == "images" {
			script = step.Run
		}
	}
	if script == "" {
		t.Fatal("candidate image build step is missing")
	}
	for _, failure := range []string{"none", "core", "browser"} {
		t.Run(failure, func(t *testing.T) {
			root := t.TempDir()
			write := func(name, contents string) {
				t.Helper()
				path := filepath.Join(root, name)
				if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(contents), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			fixture := `#!/usr/bin/env bash
set -euo pipefail
kind=$1
other=$2
shift 2
test "$LOKI_BUILD_METADATA" = "$RUNNER_TEMP/$kind-image.json"
test "$LOKI_BUILD_CACHE_SCOPE" = "loki-$kind"
printf '%s\n' "$@" > "$RUNNER_TEMP/$kind.args"
touch "$RUNNER_TEMP/$kind.started"
for ((attempt = 0; attempt < 100; attempt++)); do
  if test -f "$RUNNER_TEMP/$other.started"; then
    if test "$BUILD_FAILURE" != "$kind"; then sleep 0.05; fi
    printf '%s\n' "sha256:$kind" > "$LOKI_BUILD_METADATA"
    touch "$RUNNER_TEMP/$kind.finished"
    test "$BUILD_FAILURE" != "$kind"
    exit
  fi
  sleep 0.02
done
echo 'the other build did not start concurrently' >&2
exit 1
`
			write("scripts/build/fixture.sh", fixture)
			write("scripts/build/build-oci.sh", "#!/usr/bin/env bash\nexec ./scripts/build/fixture.sh core browser \"$@\"\n")
			write("scripts/build/build-browser-oci.sh", "#!/usr/bin/env bash\nexec ./scripts/build/fixture.sh browser core \"$@\"\n")
			write("bin/git", "#!/usr/bin/env bash\nprintf '1\\n'\n")
			write("bin/jq", "#!/usr/bin/env bash\ncat -- \"$3\"\n")
			ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
			defer cancel()
			cmd := exec.CommandContext(ctx, "bash", "-c", script)
			cmd.Dir = root
			cmd.Env = append(os.Environ(),
				"PATH="+filepath.Join(root, "bin")+string(os.PathListSeparator)+os.Getenv("PATH"),
				"RUNNER_TEMP="+root, "GITHUB_SHA=test-revision",
				"GITHUB_OUTPUT="+filepath.Join(root, "output"), "BUILD_FAILURE="+failure,
			)
			output, err := cmd.CombinedOutput()
			if (err == nil) != (failure == "none") {
				t.Fatalf("failure=%s: %v\n%s", failure, err, output)
			}
			for _, kind := range []string{"core", "browser"} {
				if _, err := os.Stat(filepath.Join(root, kind+".finished")); err != nil {
					t.Fatalf("step returned without waiting for %s: %v\n%s", kind, err, output)
				}
				args, err := os.ReadFile(filepath.Join(root, kind+".args"))
				if err != nil {
					t.Fatal(err)
				}
				image := "loki"
				if kind == "browser" {
					image += "-browser"
				}
				want := "ghcr.io/jinyongp/" + image + ":candidate-test-revision\n"
				if kind == "core" {
					for _, tool := range []string{"devtools", "ripgrep", "gh"} {
						binary := tool
						if tool == "ripgrep" {
							binary = "rg"
						}
						for _, arch := range []string{"amd64", "arm64"} {
							want += filepath.ToSlash(filepath.Join(root, "release-inputs", tool, arch, binary)) + "\n"
						}
					}
				}
				if string(args) != want {
					t.Fatalf("%s build arguments = %q, want %q", kind, args, want)
				}
			}
			results, readErr := os.ReadFile(filepath.Join(root, "output"))
			if failure != "none" {
				if readErr == nil || !os.IsNotExist(readErr) {
					t.Fatalf("failed build published image outputs: %q, error=%v", results, readErr)
				}
				return
			}
			if readErr != nil {
				t.Fatal(readErr)
			}
			want := strings.Join([]string{
				"core_image=ghcr.io/jinyongp/loki@sha256:core",
				"browser_image=ghcr.io/jinyongp/loki-browser@sha256:browser", "",
			}, "\n")
			if string(results) != want {
				t.Fatalf("image outputs = %q, want %q", results, want)
			}
		})
	}
}
