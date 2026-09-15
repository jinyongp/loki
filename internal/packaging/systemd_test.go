package packaging

import (
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"syscall"
	"testing"
	"time"

	"loki/internal/service"
)

func TestBundledSkillsIncludeGeneralWorkflowAndPinnedDevtools(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "bundled_skills"))
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		t.Fatal(err)
	}
	got := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() {
			t.Fatalf("bundled skill entry %q is not a directory", entry.Name())
		}
		got = append(got, entry.Name())
	}
	want := []string{"close", "dev-docs", "devtools", "git-commit", "humanize-korean", "minify", "planning", "queue", "review-loop", "survey", "taskwarrior", "verify", "workstream"}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("bundled skills = %#v, want %#v", got, want)
	}
	data, err := os.ReadFile(filepath.Join(root, "devtools", "SKILL.md"))
	if err != nil {
		t.Fatal(err)
	}
	if got, want := fmt.Sprintf("%x", sha256.Sum256(data)), "dd389e167c2109551c918271dc504d60f01a8d0da0500e8af3313b84e213be61"; got != want {
		t.Fatalf("devtools 0.9.0 skill digest = %s, want %s", got, want)
	}
}

func TestCandidateIncludesExecutionContract(t *testing.T) {
	path, err := filepath.Abs(filepath.Join("..", "..", "packaging", "go", "execution-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var document map[string]any
	if err = json.Unmarshal(data, &document); err != nil {
		t.Fatal(err)
	}
	if document["version"] != float64(1) {
		t.Fatalf("execution contract version = %#v", document["version"])
	}
}

func TestDevtoolsLauncherUsesRunnerEnvironmentContract(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	launcher, err := os.ReadFile(filepath.Join(root, "scripts", "loki-devtools-launch"))
	if err != nil {
		t.Fatal(err)
	}
	want := "#!/bin/sh\nset -eu\n\nnetwork_profile=runtime-default\ncase \"${1-}\" in\n  run|update) network_profile=dependency-install ;;\nesac\n\nexec /opt/loki/bin/loki runner-exec \\\n  --contract /usr/share/doc/loki/execution-contract.json \\\n  --network-profile \"$network_profile\" \\\n  -- /opt/loki/bin/devtools \"$@\"\n"
	if string(launcher) != want {
		t.Fatalf("devtools launcher = %q, want %q", launcher, want)
	}
	build, err := os.ReadFile(filepath.Join(root, "scripts", "build-loki-go-candidate.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(build), "ln -s ../../../opt/loki/libexec/devtools \"$ROOT/usr/local/bin/devtools\"") {
		t.Fatal("candidate does not expose the contracted devtools launcher")
	}
}

func waitScript(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "scripts", "wait-for-loki-sockets.sh"))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLayoutRendererProducesServiceOwnedInputs(t *testing.T) {
	root := t.TempDir()
	templates, err := filepath.Abs(filepath.Join("..", "..", "packaging", "go"))
	if err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "render-loki-go-layouts.sh"))
	if err != nil {
		t.Fatal(err)
	}
	command := exec.Command(script, templates, root, "1001", "1002", "1003", "1004")
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("render layouts: %v %s", err, output)
	}
	data, err := os.ReadFile(filepath.Join(root, "runtime.json"))
	if err != nil {
		t.Fatal(err)
	}
	var runtime service.RuntimeOptions
	if err = json.Unmarshal(data, &runtime); err != nil {
		t.Fatal(err)
	}
	if runtime.RunnerUID != 1001 || runtime.RunnerGID != 1002 || runtime.SocketGID != 1003 {
		t.Fatalf("runtime identities = %#v", runtime)
	}
	if runtime.Socket != "/run/loki-go/runtime/control.sock" || runtime.StateDirectory != "/var/lib/loki-go/runtime" {
		t.Fatalf("runtime paths = %#v", runtime)
	}
	if runtime.ExecutionContract != "/usr/share/doc/loki/execution-contract.json" || runtime.SnapshotDirectory != "/var/lib/loki-go/snapshots" {
		t.Fatalf("runner paths = %#v", runtime)
	}
	if runtime.GitHubTempDirectory != "/var/tmp/loki-go/github" {
		t.Fatalf("GitHub temporary directory = %q", runtime.GitHubTempDirectory)
	}
	data, err = os.ReadFile(filepath.Join(root, "mcp.json"))
	if err != nil {
		t.Fatal(err)
	}
	var mcp map[string]any
	if err = json.Unmarshal(data, &mcp); err != nil {
		t.Fatal(err)
	}
	if mcp["PortGuardUID"] != float64(1001) || mcp["BrowserUID"] != float64(1004) {
		t.Fatalf("MCP identities = %#v", mcp)
	}
	if mcp["RuntimeSocket"] != "/run/loki-go/runtime/control.sock" {
		t.Fatalf("MCP runtime socket = %#v", mcp["RuntimeSocket"])
	}
	for _, name := range []string{"runtime.json", "mcp.json", "identity.env"} {
		info, err := os.Stat(filepath.Join(root, name))
		if err != nil {
			t.Fatal(name, err)
		}
		if info.Mode().Perm() != 0640 {
			t.Fatalf("%s mode = %v", name, info.Mode().Perm())
		}
		if stat, ok := info.Sys().(*syscall.Stat_t); !ok || stat.Gid != uint32(os.Getgid()) {
			t.Fatalf("%s group does not match workspace group", name)
		}
	}
}

func TestRuntimeUnitSeparatesRunnerState(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	unit, err := os.ReadFile(filepath.Join(root, "packaging", "go", "systemd", "loki-go-runtime.service"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(unit)
	for _, want := range []string{
		"StateDirectory=loki-go/runtime\n",
		"/var/lib/loki-go/runner",
		"/var/cache/loki-go/runner",
		"/var/tmp/loki-go/runner",
		"/var/tmp/loki-go/github",
		"After=local-fs.target systemd-tmpfiles-setup.service",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("runtime unit does not contain %q", want)
		}
	}
	if strings.Contains(text, "StateDirectory=loki-go/runtime loki-go/") {
		t.Fatal("root runtime still creates runner state")
	}
	tmpfiles, err := os.ReadFile(filepath.Join(root, "packaging", "go", "tmpfiles.d", "loki-go.conf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"d /var/lib/loki-go/runner 0700 runner runner -",
		"d /var/cache/loki-go/runner 0700 runner runner -",
		"d /var/tmp/loki-go/runner 0700 runner runner -",
		"d /var/tmp/loki-go/github 0710 root workspace -",
	} {
		if !strings.Contains(string(tmpfiles), want) {
			t.Fatalf("tmpfiles contract does not contain %q", want)
		}
	}
}

func TestServiceSuiteHasSingleBootTarget(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "packaging", "go", "systemd"))
	if err != nil {
		t.Fatal(err)
	}
	target, err := os.ReadFile(filepath.Join(root, "loki-go.target"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{"Requires=loki-go-mcp.service", "WantedBy=multi-user.target"} {
		if !strings.Contains(string(target), want) {
			t.Fatalf("suite target does not contain %q", want)
		}
	}
	entries, err := filepath.Glob(filepath.Join(root, "*.service"))
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) == 0 {
		t.Fatal("service suite is empty")
	}
	for _, path := range entries {
		unit, readErr := os.ReadFile(path)
		if readErr != nil {
			t.Fatal(readErr)
		}
		if !strings.Contains(string(unit), "PartOf=loki-go.target\n") {
			t.Fatalf("%s does not follow suite lifecycle", filepath.Base(path))
		}
		if !strings.Contains(string(unit), "Restart=on-failure\n") {
			t.Fatalf("%s does not recover after a forced failure", filepath.Base(path))
		}
		if strings.Contains(string(unit), "WantedBy=multi-user.target") {
			t.Fatalf("%s is independently enabled at boot", filepath.Base(path))
		}
	}
}

func TestBrowserUnitUsesCandidateChromium(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	unit, err := os.ReadFile(filepath.Join(root, "packaging", "go", "systemd", "loki-go-browser.service"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(unit)
	for _, want := range []string{
		"ConditionFileIsExecutable=/opt/loki/toolchain/bin/chromium",
		"--chrome /opt/loki/toolchain/bin/chromium",
		"RuntimeDirectoryMode=0750",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("browser unit does not contain %q", want)
		}
	}
}

func TestStageNormalizesArtifactOwnership(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	stage, err := os.ReadFile(filepath.Join(root, "scripts", "stage-loki-go-candidate.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(stage), "cp -a --no-preserve=ownership \"$ARTIFACT/rootfs/.\" \"$TARGET/\"") {
		t.Fatal("candidate stage preserves untrusted builder ownership")
	}
}

func TestSocketWaitHandlesDelayedDependencies(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{"runtime", "port-guard", "browser", "signing"} {
		if err := os.Mkdir(filepath.Join(root, directory), 0700); err != nil {
			t.Fatal(err)
		}
	}
	command := exec.Command(waitScript(t))
	command.Env = append(os.Environ(), "LOKI_RUN_ROOT="+root, "LOKI_SOCKET_ATTEMPTS=100", "LOKI_SOCKET_INTERVAL=0.01")
	if err := command.Start(); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	listeners := []net.Listener{}
	for _, socket := range []string{"runtime/control.sock", "port-guard/control.sock", "browser/control.sock", "signing/agent.sock"} {
		listener, err := net.Listen("unix", filepath.Join(root, socket))
		if err != nil {
			t.Fatal(err)
		}
		listeners = append(listeners, listener)
	}
	defer func() {
		for _, listener := range listeners {
			listener.Close()
		}
	}()
	if err := command.Wait(); err != nil {
		t.Fatal("wait helper rejected delayed sockets:", err)
	}
}

func TestSocketWaitIsBounded(t *testing.T) {
	command := exec.Command(waitScript(t))
	command.Env = append(os.Environ(), "LOKI_RUN_ROOT="+t.TempDir(), "LOKI_SOCKET_ATTEMPTS=2", "LOKI_SOCKET_INTERVAL=0.01")
	started := time.Now()
	if err := command.Run(); err == nil {
		t.Fatal("wait helper accepted missing sockets")
	}
	if time.Since(started) > time.Second {
		t.Fatal("wait helper exceeded its bounded retry interval")
	}
}
