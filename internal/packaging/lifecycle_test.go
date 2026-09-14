package packaging

import (
	"crypto/sha256"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

func writeExecutable(t *testing.T, path, body string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(body), 0755); err != nil {
		t.Fatal(err)
	}
}

func candidateArtifact(t *testing.T, name string) string {
	t.Helper()
	artifact := filepath.Join(t.TempDir(), name)
	root := filepath.Join(artifact, "rootfs")
	source, err := filepath.Abs(filepath.Join("..", "..", "scripts", "loki-go-lifecycle.sh"))
	if err != nil {
		t.Fatal(err)
	}
	lifecycle, err := os.ReadFile(source)
	if err != nil {
		t.Fatal(err)
	}
	writeExecutable(t, filepath.Join(root, "opt/loki/bin/loki"), "#!/bin/sh\ntest \"$1\" = toolchain\n")
	writeExecutable(t, filepath.Join(root, "opt/loki/libexec/lifecycle"), string(lifecycle))
	writeExecutable(t, filepath.Join(root, "opt/loki/libexec/devtools"), "#!/bin/sh\nexit 0\n")
	writeExecutable(t, filepath.Join(root, "opt/loki/libexec/render-layouts"), "#!/bin/sh\nset -eu\nmkdir -p \"$2\"\nfor f in runtime.json mcp.json identity.env; do : > \"$2/$f\"; chmod 0640 \"$2/$f\"; done\n")
	files := map[string]string{
		"usr/share/doc/loki/config.toml":                       "version = 1\n",
		"usr/share/doc/loki/gitconfig":                         "[user]\n",
		"usr/lib/tmpfiles.d/loki-go.conf":                      "d /run/loki-go 0750 root workspace -\n",
		"usr/lib/systemd/system/loki-go.target":                "[Unit]\nDescription=test\n",
		"usr/lib/systemd/system/loki-go-runtime.service":       "[Service]\nExecStart=/opt/loki/bin/loki\n",
		"usr/lib/systemd/system/loki-go-mcp.service":           "[Service]\nExecStart=/opt/loki/bin/loki\n",
		"usr/lib/systemd/system/loki-go-port-guard.service":    "[Service]\nExecStart=/opt/loki/bin/loki\n",
		"usr/lib/systemd/system/loki-go-signing-agent.service": "[Service]\nExecStart=/opt/loki/bin/loki\n",
		"usr/lib/systemd/system/loki-go-browser.service":       "[Service]\nExecStart=/opt/loki/bin/loki\n",
		"usr/lib/systemd/system/loki-go-browser-proxy.service": "[Service]\nExecStart=/opt/loki/bin/loki\n",
		"usr/lib/systemd/system/loki-go-egress-proxy.service":  "[Service]\nExecStart=/opt/loki/bin/loki\n",
		"srv/workspace/loki/.agents/skills/verify/SKILL.md":    "verify\n",
		"usr/share/loki/toolchain/manifest.json":               "{}\n",
	}
	for relative, content := range files {
		path := filepath.Join(root, filepath.FromSlash(relative))
		if err = os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
		if err = os.WriteFile(path, []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}
	var entries []string
	err = filepath.Walk(root, func(path string, info os.FileInfo, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if info.Mode().IsRegular() {
			relative, relErr := filepath.Rel(root, path)
			if relErr != nil {
				return relErr
			}
			data, readErr := os.ReadFile(path)
			if readErr != nil {
				return readErr
			}
			entries = append(entries, fmt.Sprintf("%x  ./%s\n", sha256.Sum256(data), filepath.ToSlash(relative)))
		}
		return nil
	})
	if err != nil {
		t.Fatal(err)
	}
	sort.Strings(entries)
	if err = os.WriteFile(filepath.Join(artifact, "SHA256SUMS"), []byte(strings.Join(entries, "")), 0644); err != nil {
		t.Fatal(err)
	}
	return artifact
}

func lifecycleEnvironment(t *testing.T, root string) ([]string, func()) {
	t.Helper()
	for _, relative := range []string{"runtime/control.sock", "port-guard/control.sock", "browser/control.sock", "signing/agent.sock"} {
		path := filepath.Join(root, "run/loki-go", relative)
		if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
			t.Fatal(err)
		}
	}
	listeners := make([]net.Listener, 0, 4)
	for _, relative := range []string{"runtime/control.sock", "port-guard/control.sock", "browser/control.sock", "signing/agent.sock"} {
		listener, err := net.Listen("unix", filepath.Join(root, "run/loki-go", relative))
		if err != nil {
			t.Fatal(err)
		}
		listeners = append(listeners, listener)
	}
	fake := filepath.Join(t.TempDir(), "systemctl")
	log := filepath.Join(t.TempDir(), "systemctl.log")
	writeExecutable(t, fake, "#!/bin/sh\necho \"$*\" >> \"$TEST_SYSTEMCTL_LOG\"\nif test \"$1\" = is-active; then test \"$(readlink \"$TEST_ROOT/opt/loki-go/current\")\" != releases/bad; fi\n")
	environment := append(os.Environ(),
		"LOKI_INSTALL_ROOT="+root,
		"LOKI_SYSTEMCTL="+fake,
		"LOKI_SKIP_APT=1",
		"LOKI_HEALTH_ATTEMPTS=1",
		"LOKI_HEALTH_INTERVAL=0",
		"TEST_ROOT="+root,
		"TEST_SYSTEMCTL_LOG="+log,
	)
	return environment, func() {
		for _, listener := range listeners {
			_ = listener.Close()
		}
	}
}

func runLifecycle(t *testing.T, environment []string, wantSuccess bool, arguments ...string) {
	t.Helper()
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "loki-go-lifecycle.sh"))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(script, arguments...)
	command.Env = environment
	output, err := command.CombinedOutput()
	if wantSuccess && err != nil {
		t.Fatalf("lifecycle %v: %v\n%s", arguments, err, output)
	}
	if !wantSuccess && err == nil {
		t.Fatalf("lifecycle %v unexpectedly succeeded\n%s", arguments, output)
	}
}

func shortTempRoot(t *testing.T) string {
	t.Helper()
	root, err := os.MkdirTemp("/tmp", "loki-life-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(root) })
	return root
}

func TestLifecycleActivatesAndRollsBackReleases(t *testing.T) {
	root := shortTempRoot(t)
	environment, closeSockets := lifecycleEnvironment(t, root)
	defer closeSockets()
	uid := fmt.Sprint(os.Getuid())
	gid := fmt.Sprint(os.Getgid())
	first := candidateArtifact(t, "first")
	second := candidateArtifact(t, "second")
	bad := candidateArtifact(t, "bad")
	runLifecycle(t, environment, true, "install", first, "v1", uid, gid, gid, uid)
	if got, _ := os.Readlink(filepath.Join(root, "opt/loki-go/current")); got != "releases/v1" {
		t.Fatalf("current release = %q", got)
	}
	runLifecycle(t, environment, true, "activate", "v1")
	runLifecycle(t, environment, true, "install", second, "v2", uid, gid, gid, uid)
	if got, _ := os.Readlink(filepath.Join(root, "opt/loki-go/previous")); got != "releases/v1" {
		t.Fatalf("previous release = %q", got)
	}
	runLifecycle(t, environment, false, "install", bad, "bad", uid, gid, gid, uid)
	if got, _ := os.Readlink(filepath.Join(root, "opt/loki-go/current")); got != "releases/v2" {
		t.Fatalf("failed activation left current at %q", got)
	}
	runLifecycle(t, environment, true, "rollback")
	if got, _ := os.Readlink(filepath.Join(root, "opt/loki-go/current")); got != "releases/v1" {
		t.Fatalf("rolled back release = %q", got)
	}
	if _, err := os.Stat(filepath.Join(root, "srv/workspace/loki/.agents/skills/verify/SKILL.md")); err != nil {
		t.Fatal("bundled general skill was not installed:", err)
	}
}

func TestLifecycleRejectsUnsafeInputs(t *testing.T) {
	root := shortTempRoot(t)
	environment, closeSockets := lifecycleEnvironment(t, root)
	defer closeSockets()
	artifact := candidateArtifact(t, "candidate")
	runLifecycle(t, environment, false, "install", artifact, "../escape", "1", "1", "1", "1")
	runLifecycle(t, environment, false, "install", artifact, "v1", "runner", "1", "1", "1")
}

func TestLifecycleRejectsUnmanagedPathAndCanRetryActivation(t *testing.T) {
	root := shortTempRoot(t)
	environment, closeSockets := lifecycleEnvironment(t, root)
	defer closeSockets()
	if err := os.MkdirAll(filepath.Join(root, "opt/loki"), 0755); err != nil {
		t.Fatal(err)
	}
	uid := fmt.Sprint(os.Getuid())
	gid := fmt.Sprint(os.Getgid())
	artifact := candidateArtifact(t, "candidate")
	runLifecycle(t, environment, false, "install", artifact, "v1", uid, gid, gid, uid)
	if err := os.Remove(filepath.Join(root, "opt/loki")); err != nil {
		t.Fatal(err)
	}
	runLifecycle(t, environment, true, "activate", "v1")
}
