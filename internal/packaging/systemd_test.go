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

	appruntime "loki/internal/app/runtime"
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
	path, err := filepath.Abs(filepath.Join("..", "..", "packaging", "native", "execution-contract.json"))
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
	launcher, err := os.ReadFile(filepath.Join(root, "scripts", "maintainer", "devtools-launch"))
	if err != nil {
		t.Fatal(err)
	}
	want := "#!/bin/sh\nset -eu\n\nnetwork_profile=runtime-default\ncase \"${1-}\" in\n  run|update) network_profile=dependency-install ;;\nesac\n\nexec /opt/loki/bin/loki runner-exec \\\n  --contract /usr/share/doc/loki/execution-contract.json \\\n  --network-profile \"$network_profile\" \\\n  -- /opt/loki/bin/devtools \"$@\"\n"
	if string(launcher) != want {
		t.Fatalf("devtools launcher = %q, want %q", launcher, want)
	}
	build, err := os.ReadFile(filepath.Join(root, "scripts", "build", "build-candidate.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(build), "ln -s ../../../opt/loki/libexec/devtools \"$ROOT/usr/local/bin/devtools\"") {
		t.Fatal("candidate does not expose the contracted devtools launcher")
	}
}

func TestCandidateToolchainBundleBindsManagedCatalog(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	bundleScript, err := os.ReadFile(filepath.Join(root, "scripts", "build", "build-toolchain-bundle.sh"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(bundleScript), `--catalog "$SOURCE_DIR/packaging/native/toolchain-catalog.json"`) {
		t.Fatal("toolchain bundle build does not include the managed catalog")
	}
	candidate, err := os.ReadFile(filepath.Join(root, "scripts", "build", "build-candidate.sh"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		`"$TOOLCHAIN_BUNDLE/catalog.json"`,
		`cmp "$SOURCE_DIR/packaging/native/toolchain-catalog.json" "$TOOLCHAIN_BUNDLE/catalog.json"`,
		`install -m 0644 "$SOURCE_DIR/packaging/native/toolchain-catalog.json" "$ROOT/usr/share/doc/loki/toolchain-catalog.json"`,
	} {
		if !strings.Contains(string(candidate), want) {
			t.Fatalf("candidate build does not bind managed catalog contract: %s", want)
		}
	}
}

func waitScript(t *testing.T) string {
	t.Helper()
	path, err := filepath.Abs(filepath.Join("..", "..", "scripts", "maintainer", "wait-for-sockets.sh"))
	if err != nil {
		t.Fatal(err)
	}
	return path
}

func TestLayoutRendererProducesServiceOwnedInputs(t *testing.T) {
	root := t.TempDir()
	templates, err := filepath.Abs(filepath.Join("..", "..", "packaging", "native"))
	if err != nil {
		t.Fatal(err)
	}
	script, err := filepath.Abs(filepath.Join("..", "..", "scripts", "maintainer", "render-layouts.sh"))
	if err != nil {
		t.Fatal(err)
	}
	jobImage := "registry.example/loki@sha256:" + strings.Repeat("a", 64)
	command := exec.Command(script, templates, root, "1001", "1002", "1003", "1004", "1005", jobImage)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("render layouts: %v %s", err, output)
	}
	data, err := os.ReadFile(filepath.Join(root, "runtime.json"))
	if err != nil {
		t.Fatal(err)
	}
	var runtime struct {
		appruntime.RuntimeOptions
		ExecutionContract string
	}
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
	if mcp["PortGuardUID"] != float64(1001) || mcp["BrowserUID"] != float64(1004) || mcp["ExecutorUID"] != float64(1005) {
		t.Fatalf("MCP identities = %#v", mcp)
	}
	if mcp["RuntimeSocket"] != "/run/loki-go/runtime/control.sock" || mcp["ExecutorSocket"] != "/run/loki-go/executor/control.sock" {
		t.Fatalf("MCP runtime socket = %#v", mcp["RuntimeSocket"])
	}
	if mcp["ExecutionContract"] != "/usr/share/doc/loki/execution-contract.json" {
		t.Fatalf("MCP execution contract = %#v", mcp["ExecutionContract"])
	}
	if mcp["PackagedSkillRoot"] != "/opt/loki/share/skills" {
		t.Fatalf("MCP packaged Skill root = %#v", mcp["PackagedSkillRoot"])
	}
	data, err = os.ReadFile(filepath.Join(root, "launcher.json"))
	if err != nil {
		t.Fatal(err)
	}
	var launcher map[string]any
	if err = json.Unmarshal(data, &launcher); err != nil {
		t.Fatal(err)
	}
	if launcher["Socket"] != "/run/loki-go/launcher/control.sock" ||
		launcher["ExecutorUID"] != float64(1005) ||
		launcher["WorkloadUID"] != float64(1001) ||
		launcher["WorkloadGID"] != float64(1002) ||
		launcher["Image"] != jobImage || launcher["GatewayImage"] != jobImage ||
		launcher["Workspace"] != "/srv/workspace/loki" ||
		launcher["MaxConcurrentJobs"] != float64(8) {
		t.Fatalf("launcher layout = %#v", launcher)
	}
	data, err = os.ReadFile(filepath.Join(root, "executor.json"))
	if err != nil {
		t.Fatal(err)
	}
	var executor map[string]any
	if err = json.Unmarshal(data, &executor); err != nil {
		t.Fatal(err)
	}
	if executor["Socket"] != "/run/loki-go/executor/control.sock" ||
		executor["AgentUID"] != float64(1001) ||
		executor["ExecutorUID"] != float64(1005) ||
		executor["LauncherSocket"] != "/run/loki-go/launcher/control.sock" ||
		executor["LauncherUID"] != float64(0) {
		t.Fatalf("executor layout = %#v", executor)
	}
	identity, err := os.ReadFile(filepath.Join(root, "identity.env"))
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(identity), "EXECUTOR_UID=1005\n") {
		t.Fatalf("identity environment = %q", identity)
	}
	for _, name := range []string{"runtime.json", "mcp.json", "launcher.json", "executor.json", "identity.env"} {
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
	unit, err := os.ReadFile(filepath.Join(root, "packaging", "native", "systemd", "loki-go-runtime.service"))
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
	tmpfiles, err := os.ReadFile(filepath.Join(root, "packaging", "native", "tmpfiles.d", "loki-go.conf"))
	if err != nil {
		t.Fatal(err)
	}
	for _, want := range []string{
		"d /var/lib/loki-go/runner 0700 runner runner -",
		"d /var/lib/loki-go/runner/agents 0700 runner runner -",
		"d /var/lib/loki-go/runner/agents/skills 0700 runner runner -",
		"d /var/cache/loki-go/runner 0700 runner runner -",
		"d /var/tmp/loki-go/runner 0700 runner runner -",
		"d /var/tmp/loki-go/github 0710 root workspace -",
	} {
		if !strings.Contains(string(tmpfiles), want) {
			t.Fatalf("tmpfiles contract does not contain %q", want)
		}
	}
}

func TestMCPUnitMountsUserSkillsReadOnly(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", ".."))
	if err != nil {
		t.Fatal(err)
	}
	unit, err := os.ReadFile(filepath.Join(root, "packaging", "native", "systemd", "loki-go-mcp.service"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(unit)
	for _, want := range []string{
		"ProtectHome=read-only\n",
		"BindReadOnlyPaths=/var/lib/loki-go/runner/agents:/home/runner/.agents\n",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("MCP unit does not contain %q", want)
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.HasPrefix(line, "ReadWritePaths=") && strings.Contains(line, "/home/runner/.agents") {
			t.Fatalf("MCP unit makes user Skill path writable: %q", line)
		}
	}
}

func TestJobRoleUnitsKeepLauncherAuthorityNarrow(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "packaging", "native", "systemd"))
	if err != nil {
		t.Fatal(err)
	}
	read := func(name string) string {
		t.Helper()
		data, readErr := os.ReadFile(filepath.Join(root, name))
		if readErr != nil {
			t.Fatal(readErr)
		}
		return string(data)
	}
	launcher := read("loki-go-launcher.service")
	for _, want := range []string{
		"User=root\n",
		"ConditionPathExists=/run/docker.sock\n",
		"ExecStart=/opt/loki/bin/loki-launcher ",
		"ReadOnlyPaths=/var/lib/loki-go/toolchains/generations\n",
		"ReadWritePaths=/var/lib/loki-go/launcher /var/lib/loki-go/toolchains/refs /run/loki-go/launcher -/run/docker.sock\n",
		"RestrictAddressFamilies=AF_UNIX\n",
	} {
		if !strings.Contains(launcher, want) {
			t.Fatalf("launcher unit does not contain %q", want)
		}
	}
	for _, forbidden := range []string{"/var/lib/loki-go/toolchains/staging", "/var/lib/loki-go/toolchains/locks"} {
		if strings.Contains(launcher, forbidden) {
			t.Fatalf("launcher received managed toolchain mutation authority: %s", forbidden)
		}
	}
	executor := read("loki-go-executor.service")
	for _, want := range []string{
		"Requires=loki-go-launcher.service\n",
		"User=loki-executor\n",
		"ExecStart=/opt/loki/bin/loki-executor ",
		"InaccessiblePaths=/srv/workspace /var/lib/loki-go/runtime /var/lib/loki-go/launcher /var/lib/loki-go/signing /etc/loki-go/token -/run/docker.sock\n",
		"RestrictAddressFamilies=AF_UNIX\n",
	} {
		if !strings.Contains(executor, want) {
			t.Fatalf("executor unit does not contain %q", want)
		}
	}
	if strings.Contains(executor, "ConditionPathExists=/run/docker.sock") ||
		strings.Contains(executor, "ReadWritePaths=-/run/docker.sock") {
		t.Fatal("executor received Docker authority")
	}
	mcp := read("loki-go-mcp.service")
	if !strings.Contains(mcp, "loki-go-executor.service") ||
		!strings.Contains(mcp, "InaccessiblePaths=/run/loki-go/launcher -/run/docker.sock /var/lib/loki-go/launcher") ||
		strings.Contains(mcp, "Requires=loki-go-launcher.service") ||
		!strings.Contains(mcp, "Wants=loki-go-browser.service") {
		t.Fatal("MCP does not preserve the executor-only launcher and optional browser boundaries")
	}
	for _, line := range strings.Split(mcp, "\n") {
		if strings.HasPrefix(line, "Requires=") && strings.Contains(line, "loki-go-browser.service") {
			t.Fatalf("MCP makes optional browser a hard dependency: %q", line)
		}
	}
}

func TestServiceSuiteHasSingleBootTarget(t *testing.T) {
	root, err := filepath.Abs(filepath.Join("..", "..", "packaging", "native", "systemd"))
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
	unit, err := os.ReadFile(filepath.Join(root, "packaging", "native", "systemd", "loki-go-browser.service"))
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
	stage, err := os.ReadFile(filepath.Join(root, "scripts", "maintainer", "stage-candidate.sh"))
	if err != nil {
		t.Fatal(err)
	}
	text := string(stage)
	if !strings.Contains(text, "cp -a --no-preserve=ownership \"$ARTIFACT/rootfs/.\" \"$TARGET/\"") {
		t.Fatal("candidate stage preserves untrusted builder ownership")
	}
	for _, want := range []string{
		"EXECUTOR_UID JOB_IMAGE",
		"test \"$EXECUTOR_UID\" -ne \"$RUNNER_UID\"",
		"@sha256:[0-9a-f]{64}",
		"\"$RUNNER_UID\" \"$RUNNER_GID\" \"$WORKSPACE_GID\" \"$BROWSER_UID\" \"$EXECUTOR_UID\" \"$JOB_IMAGE\"",
		"$TARGET/var/lib/loki-go/launcher",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("candidate stage does not contain %q", want)
		}
	}
}

func TestSocketWaitHandlesDelayedDependencies(t *testing.T) {
	root := t.TempDir()
	for _, directory := range []string{"runtime", "port-guard", "signing", "executor"} {
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
	for _, socket := range []string{"runtime/control.sock", "port-guard/control.sock", "signing/agent.sock", "executor/control.sock"} {
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
