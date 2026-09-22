package main

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	lifecyclecompose "loki/internal/host/lifecycle/compose"
	"loki/internal/host/releases"
)

type fakeHostExecutor struct {
	paths   map[string]bool
	outputs map[string]string
	errs    map[string]error
	calls   []string
	after   func(string)
}

func (f *fakeHostExecutor) LookPath(name string) (string, error) {
	if f.paths[name] {
		return "/usr/bin/" + name, nil
	}
	return "", errors.New("not found")
}

func (f *fakeHostExecutor) Run(_ context.Context, executable string, args []string, stdin string) ([]byte, error) {
	key := strings.Join(append([]string{executable}, args...), "::")
	f.calls = append(f.calls, key)
	output := f.outputs[key]
	err := f.errs[key]
	if f.after != nil {
		f.after(key)
	}
	if err != nil {
		return nil, err
	}
	return []byte(output), nil
}

func runtimeRequirements() releases.RuntimeRequirements {
	return releases.RuntimeRequirements{DockerMin: "28.0.0", ComposeMin: "2.39.0"}
}

func TestProbeHostRuntimeRequiresReleaseMinimums(t *testing.T) {
	executor := &fakeHostExecutor{
		paths: map[string]bool{"docker": true},
		outputs: map[string]string{
			"docker::version::--format::{{.Server.Version}}": "29.8.1\n",
			"docker::compose::version::--short":              "v2.39.4\n",
		},
		errs: map[string]error{},
	}
	probe, err := probeHostRuntime(t.Context(), runtimeRequirements(), []string{"docker"}, executor)
	if err != nil {
		t.Fatal(err)
	}
	if probe.Access != hostDockerAccessDirect || probe.DockerVersion != "29.8.1" || probe.ComposeVersion != "2.39.4" {
		t.Fatalf("runtime probe = %#v", probe)
	}
	executor.outputs["docker::version::--format::{{.Server.Version}}"] = "27.5.1\n"
	if _, err = probeHostRuntime(t.Context(), runtimeRequirements(), []string{"docker"}, executor); err == nil ||
		!strings.Contains(err.Error(), "below required") {
		t.Fatalf("outdated Docker error = %v", err)
	}
}

func TestProbeHostRuntimeSupportsExplicitSudoBoundary(t *testing.T) {
	executor := &fakeHostExecutor{
		paths: map[string]bool{"sudo": true},
		outputs: map[string]string{
			"sudo::docker::version::--format::{{.Server.Version}}": "29.8.1\n",
			"sudo::docker::compose::version::--short":              "2.40.0\n",
		},
		errs: map[string]error{},
	}
	probe, err := probeHostRuntime(t.Context(), runtimeRequirements(), []string{"sudo", "docker"}, executor)
	if err != nil {
		t.Fatal(err)
	}
	if probe.Access != hostDockerAccessSudo {
		t.Fatalf("sudo runtime probe = %#v", probe)
	}
	runner, err := dockerLifecycleRunner(probe.Access)
	if err != nil {
		t.Fatal(err)
	}
	typed, ok := runner.(lifecyclecompose.ExecRunner)
	if !ok || typed.Executable != "sudo" || !slices.Equal(typed.Prefix, []string{"docker"}) {
		t.Fatalf("sudo lifecycle runner = %#v", runner)
	}
}

func TestUbuntuDockerPrerequisitePlanIsExplicitAndDoesNotGrantDockerGroup(t *testing.T) {
	steps, err := ubuntuDockerPrerequisitePlan(releases.SupportedHost{
		Environment: "native", Distribution: "ubuntu", Version: "24.04", Arch: "amd64",
	})
	if err != nil {
		t.Fatal(err)
	}
	if len(steps) < 8 {
		t.Fatalf("prerequisite steps = %d", len(steps))
	}
	var joined []string
	for _, step := range steps {
		joined = append(joined, formatHostCommand(step, false), step.Stdin)
	}
	body := strings.Join(joined, "\n")
	for _, required := range []string{
		"download.docker.com/linux/ubuntu",
		"docker-ce", "docker-compose-plugin", "systemctl enable --now docker", "apt-get install -y ca-certificates curl acl",
	} {
		if !strings.Contains(body, required) {
			t.Fatalf("prerequisite plan lacks %q: %s", required, body)
		}
	}
	for _, forbidden := range []string{"usermod", "groupadd", "docker group", "chmod 666", "get.docker.com"} {
		if strings.Contains(body, forbidden) {
			t.Fatalf("prerequisite plan contains forbidden mutation %q: %s", forbidden, body)
		}
	}
}

func TestPrepareHostDockerRuntimeDoesNotMutateWithoutApproval(t *testing.T) {
	executor := &fakeHostExecutor{
		paths: map[string]bool{
			"apt-get": true, "install": true, "curl": true, "chmod": true, "tee": true,
			"systemctl": true, "sudo": true,
		},
		outputs: map[string]string{},
		errs:    map[string]error{},
	}
	_, err := prepareHostDockerRuntime(
		t.Context(), runtimeRequirements(),
		releases.SupportedHost{Environment: "native", Distribution: "ubuntu", Version: "24.04", Arch: "amd64"},
		false, false, executor, nil,
	)
	if err == nil || !strings.Contains(err.Error(), "--install-prerequisites") {
		t.Fatalf("missing Docker without approval error = %v", err)
	}
	for _, call := range executor.calls {
		if strings.Contains(call, "apt-get") {
			t.Fatalf("package mutation ran without approval: %q", call)
		}
	}
}

func TestAutomaticDockerInstallRequiresSystemdOnWSL(t *testing.T) {
	host := releases.SupportedHost{Environment: "wsl", Distribution: "ubuntu", Version: "24.04", Arch: "amd64"}
	if err := validateAutomaticDockerInstallHost(host, "init\n"); err == nil ||
		!strings.Contains(err.Error(), "wsl.exe --shutdown") ||
		!strings.Contains(err.Error(), "systemd=true") {
		t.Fatalf("WSL systemd-disabled guidance = %v", err)
	}
	if err := validateAutomaticDockerInstallHost(host, "systemd\n"); err != nil {
		t.Fatalf("WSL systemd host rejected: %v", err)
	}
	if err := validateAutomaticDockerInstallHost(releases.SupportedHost{Environment: "native"}, "init"); err != nil {
		t.Fatalf("native host unexpectedly required systemd PID 1: %v", err)
	}
}

func TestPrepareHostDockerRuntimeKeepsWorkingDockerUntouched(t *testing.T) {
	executor := &fakeHostExecutor{
		paths: map[string]bool{"docker": true},
		outputs: map[string]string{
			"docker::version::--format::{{.Server.Version}}": "29.8.1\n",
			"docker::compose::version::--short":              "2.40.0\n",
		},
		errs: map[string]error{},
	}
	probe, err := prepareHostDockerRuntime(
		t.Context(), runtimeRequirements(), releases.SupportedHost{},
		true, false, executor, nil,
	)
	if err != nil {
		t.Fatal(err)
	}
	if probe.Access != hostDockerAccessDirect {
		t.Fatalf("prepared runtime = %#v", probe)
	}
	for _, call := range executor.calls {
		if strings.Contains(call, "apt-get") {
			t.Fatalf("working Docker was reconfigured: %q", call)
		}
	}
}

func TestPrepareHostDockerRuntimeUpgradesOutdatedDockerOnlyAfterApproval(t *testing.T) {
	executor := &fakeHostExecutor{
		paths: map[string]bool{
			"docker": true, "sudo": true, "apt-get": true, "install": true, "curl": true,
			"chmod": true, "tee": true, "systemctl": true,
		},
		outputs: map[string]string{
			"docker::version::--format::{{.Server.Version}}": "27.5.1\n",
			"docker::compose::version::--short":              "2.38.0\n",
		},
		errs: map[string]error{},
	}
	executor.after = func(call string) {
		if call == "sudo::systemctl::enable::--now::docker" {
			executor.outputs["docker::version::--format::{{.Server.Version}}"] = "29.8.1\n"
			executor.outputs["docker::compose::version::--short"] = "2.40.0\n"
		}
	}
	host := releases.SupportedHost{
		Environment: "native", Distribution: "ubuntu", Version: "24.04", Arch: "amd64",
	}
	if _, err := prepareHostDockerRuntime(
		t.Context(), runtimeRequirements(), host, false, false, executor, nil,
	); err == nil || !strings.Contains(err.Error(), "--install-prerequisites") {
		t.Fatalf("outdated Docker without approval error = %v", err)
	}
	before := len(executor.calls)
	var plan strings.Builder
	probe, err := prepareHostDockerRuntime(
		t.Context(), runtimeRequirements(), host, true, true, executor, &plan,
	)
	if err != nil {
		t.Fatal(err)
	}
	if probe.Access != hostDockerAccessDirect || probe.DockerVersion != "29.8.1" || probe.ComposeVersion != "2.40.0" {
		t.Fatalf("upgraded runtime = %#v", probe)
	}
	calls := strings.Join(executor.calls[before:], "\n")
	if !strings.Contains(calls, "apt-get::install::-y::docker-ce::docker-ce-cli::containerd.io::docker-buildx-plugin::docker-compose-plugin") {
		t.Fatalf("upgrade plan did not install official Docker packages: %s", calls)
	}
	if !strings.Contains(plan.String(), "Types: deb") || !strings.Contains(plan.String(), "Signed-By: /etc/apt/keyrings/docker.asc") {
		t.Fatalf("prerequisite preview omitted docker.sources content: %s", plan.String())
	}
}
