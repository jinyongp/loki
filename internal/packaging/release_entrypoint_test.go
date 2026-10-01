package packaging

import (
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

func TestReleaseEntrypointRequiresUnchangedValidatedSource(t *testing.T) {
	if runtime.GOOS == "windows" {
		t.Skip("local release entrypoint uses a POSIX release-engineering host")
	}
	script, err := os.ReadFile(filepath.Join("..", "..", "scripts", "maintainer", "release.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, setup, validation string
		args                    []string
		validated, dispatched   bool
	}{
		{name: "publish", validated: true, dispatched: true},
		{name: "candidate-options", args: []string{"minor", "--validate-only", "--check-updates", "--oci"}, validated: true, dispatched: true},
		{name: "validation-failed", validation: "exit 1", validated: true},
		{name: "dirty", setup: "dirty"},
		{name: "untracked", setup: "untracked"},
		{name: "unpushed", setup: "unpushed"},
		{name: "other-branch", setup: "branch"},
		{name: "source-edited-during-validation", validation: "printf changed >> source", validated: true},
		{name: "head-changed-during-validation", validation: "git commit --allow-empty -qm changed && git push -q origin main", validated: true},
		{name: "remote-changed-during-validation", validation: "git commit --allow-empty -qm changed && git push -q origin main && git reset -q --hard HEAD~1", validated: true},
		{name: "help", args: []string{"--help"}},
		{name: "invalid-option", args: []string{"--skip-validation"}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			fixture := t.TempDir()
			root := filepath.Join(fixture, "repo")
			bin := filepath.Join(fixture, "bin")
			for _, dir := range []string{filepath.Join(root, "scripts", "verify"), filepath.Join(root, "scripts", "maintainer"), bin} {
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
			write(filepath.Join(root, "scripts", "maintainer", "release.sh"), string(script))
			write(filepath.Join(root, "scripts", "verify", "verify-preflight.sh"), "#!/bin/sh\nset -eu\nprintf 'preflight %s\\n' \"$*\" >> \"$RELEASE_CALLS\"\n"+tc.validation+"\n")
			write(filepath.Join(root, "source"), "initial\n")
			write(filepath.Join(bin, "gh"), "#!/bin/sh\nprintf 'dispatch\\n' >> \"$RELEASE_CALLS\"\nprintf '%s\\n' \"$@\" >> \"$RELEASE_CALLS\"\n")
			git := func(args ...string) string {
				t.Helper()
				cmd := exec.Command("git", args...)
				cmd.Dir = root
				output, err := cmd.CombinedOutput()
				if err != nil {
					t.Fatalf("git %v: %v\n%s", args, err, output)
				}
				return strings.TrimSpace(string(output))
			}
			git("init", "-q", "--initial-branch=main")
			git("config", "user.name", "Release Fixture")
			git("config", "user.email", "fixture@example.invalid")
			git("config", "commit.gpgsign", "false")
			git("config", "core.hooksPath", filepath.Join(fixture, "no-hooks"))
			remote := filepath.Join(fixture, "remote.git")
			git("init", "-q", "--bare", remote)
			git("remote", "add", "origin", remote)
			git("add", ".")
			git("commit", "-qm", "fixture")
			git("push", "-q", "origin", "main")
			sha := git("rev-parse", "HEAD")
			switch tc.setup {
			case "dirty":
				write(filepath.Join(root, "source"), "dirty\n")
			case "untracked":
				write(filepath.Join(root, "extra"), "untracked\n")
			case "unpushed":
				git("commit", "--allow-empty", "-qm", "unpushed")
			case "branch":
				git("switch", "-qc", "work")
			}
			calls := filepath.Join(fixture, "calls")
			cmd := exec.Command("sh", append([]string{filepath.Join(root, "scripts", "maintainer", "release.sh")}, tc.args...)...)
			cmd.Env = append(os.Environ(), "PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"), "RELEASE_CALLS="+calls)
			output, runErr := cmd.CombinedOutput()
			pass := tc.dispatched || tc.name == "help"
			if (runErr == nil) != pass {
				t.Fatalf("pass=%v: %v\n%s", pass, runErr, output)
			}
			raw, err := os.ReadFile(calls)
			if err != nil && !os.IsNotExist(err) {
				t.Fatal(err)
			}
			log := string(raw)
			if strings.Contains(log, "preflight ") != tc.validated || strings.Contains(log, "dispatch\n") != tc.dispatched {
				t.Fatalf("unexpected validation/dispatch calls:\n%s\n%s", log, output)
			}
			if tc.dispatched {
				want := "preflight \ndispatch\nworkflow\nrun\nrelease.yml\n--ref\nmain\n-f\nbump=patch\n-f\npublish=true\n-f\ncheck_updates=false\n-f\nexpected_sha=" + sha + "\n"
				if tc.name == "candidate-options" {
					want = strings.NewReplacer("preflight \n", "preflight --oci\n", "bump=patch", "bump=minor", "publish=true", "publish=false", "check_updates=false", "check_updates=true").Replace(want)
				}
				if log != want {
					t.Fatalf("dispatch calls:\n%s\nwant:\n%s", log, want)
				}
			}
		})
	}
}

func TestReleaseWorkflowRejectsDifferentLocallyValidatedCommit(t *testing.T) {
	var gate string
	for _, step := range releaseWorkflowJobs(t)["preflight"].Steps {
		if step.Env["EXPECTED_SHA"] == "${{ inputs.expected_sha }}" {
			gate = step.Run
		}
	}
	if gate == "" {
		t.Fatal("release preflight must bind the locally validated commit")
	}
	if runtime.GOOS == "windows" {
		t.Skip("CI identity gate executes on Linux")
	}
	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, "git"), []byte("#!/bin/sh\nif test \"$1\" = rev-parse; then printf '%s\\n' actual; fi\n"), 0755); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		expected, publish string
		pass              bool
	}{
		{"actual", "true", true},
		{"", "true", false},
		{"other", "true", false},
		{"", "false", true},
		{"actual", "false", true},
		{"other", "false", false},
	} {
		cmd := exec.Command("bash", "-c", gate)
		cmd.Env = append(os.Environ(), "PATH="+root+string(os.PathListSeparator)+os.Getenv("PATH"), "GITHUB_REF=refs/heads/main", "GITHUB_SHA=actual", "EXPECTED_SHA="+tc.expected, "PUBLISH_RELEASE="+tc.publish)
		output, err := cmd.CombinedOutput()
		if (err == nil) != tc.pass {
			t.Fatalf("expected=%q publish=%s: %v\n%s", tc.expected, tc.publish, err, output)
		}
	}
}
