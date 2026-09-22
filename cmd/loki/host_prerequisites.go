package main

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strconv"
	"strings"

	lifecyclecompose "loki/internal/host/lifecycle/compose"
	"loki/internal/host/releases"
)

const (
	hostDockerAccessDirect = "direct"
	hostDockerAccessSudo   = "sudo"
	maxHostCommandOutput   = 32 << 10
)

type hostRuntimeProbe struct {
	Access         string
	DockerVersion  string
	ComposeVersion string
}

type hostCommandStep struct {
	Description string
	Executable  string
	Args        []string
	Stdin       string
	Root        bool
}

type hostCommandExecutor interface {
	LookPath(string) (string, error)
	Run(context.Context, string, []string, string) ([]byte, error)
}

type execHostCommandExecutor struct{}

func (execHostCommandExecutor) LookPath(name string) (string, error) {
	return exec.LookPath(name)
}

func (execHostCommandExecutor) Run(ctx context.Context, executable string, args []string, stdin string) ([]byte, error) {
	command := exec.CommandContext(ctx, executable, args...)
	if stdin != "" {
		command.Stdin = strings.NewReader(stdin)
	}
	var output bytes.Buffer
	command.Stdout = &output
	command.Stderr = &output
	err := command.Run()
	raw := output.Bytes()
	if len(raw) > maxHostCommandOutput {
		raw = raw[:maxHostCommandOutput]
	}
	if err != nil {
		return append([]byte(nil), raw...), err
	}
	return append([]byte(nil), raw...), nil
}

func probeHostRuntime(
	ctx context.Context,
	requirements releases.RuntimeRequirements,
	command []string,
	executor hostCommandExecutor,
) (hostRuntimeProbe, error) {
	if executor == nil {
		return hostRuntimeProbe{}, errors.New("host prerequisite executor is not configured")
	}
	if len(command) == 0 || strings.TrimSpace(command[0]) == "" {
		return hostRuntimeProbe{}, errors.New("Docker command is not configured")
	}
	if _, err := executor.LookPath(command[0]); err != nil {
		return hostRuntimeProbe{}, errors.New("Docker command is unavailable")
	}
	run := func(args ...string) ([]byte, error) {
		all := append(append([]string(nil), command[1:]...), args...)
		return executor.Run(ctx, command[0], all, "")
	}
	dockerRaw, err := run("version", "--format", "{{.Server.Version}}")
	if err != nil {
		return hostRuntimeProbe{}, errors.New("Docker Engine is unavailable to the current installation boundary")
	}
	composeRaw, err := run("compose", "version", "--short")
	if err != nil {
		return hostRuntimeProbe{}, errors.New("Docker Compose v2 is unavailable to the current installation boundary")
	}
	dockerVersion := strings.TrimSpace(string(dockerRaw))
	composeVersion := strings.TrimPrefix(strings.TrimSpace(string(composeRaw)), "v")
	if err = requireRuntimeVersion("Docker Engine", dockerVersion, requirements.DockerMin); err != nil {
		return hostRuntimeProbe{}, err
	}
	if err = requireRuntimeVersion("Docker Compose", composeVersion, requirements.ComposeMin); err != nil {
		return hostRuntimeProbe{}, err
	}
	access := hostDockerAccessDirect
	if command[0] == "sudo" {
		access = hostDockerAccessSudo
	}
	return hostRuntimeProbe{
		Access: access, DockerVersion: dockerVersion, ComposeVersion: composeVersion,
	}, nil
}

func requireRuntimeVersion(name, actual, minimum string) error {
	got, err := parseRuntimeVersion(actual)
	if err != nil {
		return fmt.Errorf("%s returned an unsupported version %q", name, actual)
	}
	want, err := parseRuntimeVersion(minimum)
	if err != nil {
		return fmt.Errorf("%s release requirement %q is invalid", name, minimum)
	}
	for index := range got {
		if got[index] > want[index] {
			return nil
		}
		if got[index] < want[index] {
			return fmt.Errorf("%s %s is below required %s", name, actual, minimum)
		}
	}
	return nil
}

func parseRuntimeVersion(value string) ([3]uint64, error) {
	value = strings.TrimPrefix(strings.TrimSpace(value), "v")
	parts := strings.Split(value, ".")
	if len(parts) < 2 || len(parts) > 3 {
		return [3]uint64{}, errors.New("runtime version must contain two or three numeric components")
	}
	var result [3]uint64
	for index, part := range parts {
		if part == "" {
			return [3]uint64{}, errors.New("runtime version component is empty")
		}
		number, err := strconv.ParseUint(part, 10, 32)
		if err != nil {
			return [3]uint64{}, errors.New("runtime version component is invalid")
		}
		result[index] = number
	}
	return result, nil
}

func dockerLifecycleRunner(access string) (lifecyclecompose.Runner, error) {
	switch access {
	case hostDockerAccessDirect:
		return lifecyclecompose.ExecRunner{Executable: "docker"}, nil
	case hostDockerAccessSudo:
		return lifecyclecompose.ExecRunner{Executable: "sudo", Prefix: []string{"docker"}}, nil
	default:
		return nil, errors.New("Docker access mode is invalid")
	}
}

func ubuntuDockerPrerequisitePlan(host releases.SupportedHost) ([]hostCommandStep, error) {
	if host.Environment != "native" && host.Environment != "wsl" ||
		host.Distribution != "ubuntu" || host.Version != "24.04" || host.Arch != "amd64" {
		return nil, errors.New("automatic Docker prerequisite installation is supported only on Ubuntu 24.04 amd64 and WSL2 Ubuntu 24.04 amd64")
	}
	const sources = "Types: deb\n" +
		"URIs: https://download.docker.com/linux/ubuntu\n" +
		"Suites: noble\n" +
		"Components: stable\n" +
		"Architectures: amd64\n" +
		"Signed-By: /etc/apt/keyrings/docker.asc\n"
	return []hostCommandStep{
		{Description: "refresh the Ubuntu package index", Executable: "apt-get", Args: []string{"update"}, Root: true},
		{Description: "install HTTPS and ACL prerequisites", Executable: "apt-get", Args: []string{"install", "-y", "ca-certificates", "curl", "acl"}, Root: true},
		{Description: "create Docker's apt keyring directory", Executable: "install", Args: []string{"-m", "0755", "-d", "/etc/apt/keyrings"}, Root: true},
		{Description: "download Docker's official apt signing key", Executable: "curl", Args: []string{"-fsSL", "https://download.docker.com/linux/ubuntu/gpg", "-o", "/etc/apt/keyrings/docker.asc"}, Root: true},
		{Description: "make Docker's apt signing key readable by apt", Executable: "chmod", Args: []string{"a+r", "/etc/apt/keyrings/docker.asc"}, Root: true},
		{Description: "register Docker's official stable apt repository", Executable: "tee", Args: []string{"/etc/apt/sources.list.d/docker.sources"}, Stdin: sources, Root: true},
		{Description: "refresh the package index with Docker's repository", Executable: "apt-get", Args: []string{"update"}, Root: true},
		{Description: "install Docker Engine, Buildx and Compose v2", Executable: "apt-get", Args: []string{"install", "-y", "docker-ce", "docker-ce-cli", "containerd.io", "docker-buildx-plugin", "docker-compose-plugin"}, Root: true},
		{Description: "enable and start Docker Engine", Executable: "systemctl", Args: []string{"enable", "--now", "docker"}, Root: true},
	}, nil
}

func executeHostCommandPlan(ctx context.Context, steps []hostCommandStep, executor hostCommandExecutor) error {
	if executor == nil {
		return errors.New("host prerequisite executor is not configured")
	}
	for _, step := range steps {
		executable := step.Executable
		args := append([]string(nil), step.Args...)
		if step.Root && os.Geteuid() != 0 {
			if _, err := executor.LookPath("sudo"); err != nil {
				return fmt.Errorf("%s requires root privileges and sudo is unavailable", step.Description)
			}
			args = append([]string{executable}, args...)
			executable = "sudo"
		}
		if _, err := executor.LookPath(executable); err != nil {
			return fmt.Errorf("%s requires %s", step.Description, executable)
		}
		if _, err := executor.Run(ctx, executable, args, step.Stdin); err != nil {
			return fmt.Errorf("%s failed", step.Description)
		}
	}
	return nil
}

func formatHostCommand(step hostCommandStep, root bool) string {
	parts := append([]string{step.Executable}, step.Args...)
	if step.Root && !root {
		parts = append([]string{"sudo"}, parts...)
	}
	return strings.Join(parts, " ")
}

func validateAutomaticDockerInstallHost(host releases.SupportedHost, initName string) error {
	if host.Environment != "wsl" {
		return nil
	}
	if strings.TrimSpace(initName) == "systemd" {
		return nil
	}
	return errors.New("WSL2 systemd is disabled; enable it in /etc/wsl.conf with [boot] systemd=true, then run wsl.exe --shutdown from Windows and restart WSL before installing Docker")
}

func prepareHostDockerRuntime(
	ctx context.Context,
	requirements releases.RuntimeRequirements,
	host releases.SupportedHost,
	installPrerequisites, allowSudo bool,
	executor hostCommandExecutor,
	planWriter io.Writer,
) (hostRuntimeProbe, error) {
	if override := strings.TrimSpace(os.Getenv("LOKI_DOCKER")); override != "" {
		return probeHostRuntime(ctx, requirements, []string{override}, executor)
	}
	if direct, err := probeHostRuntime(ctx, requirements, []string{"docker"}, executor); err == nil {
		return direct, nil
	}

	_, dockerPathErr := executor.LookPath("docker")
	if dockerPathErr != nil {
		if !installPrerequisites {
			return hostRuntimeProbe{}, errors.New("Docker Engine and Compose v2 are required; rerun interactively or pass --install-prerequisites")
		}
		if host.Environment == "wsl" {
			initRaw, readErr := os.ReadFile("/proc/1/comm")
			if readErr != nil {
				return hostRuntimeProbe{}, errors.New("cannot determine whether WSL2 systemd is enabled")
			}
			if err := validateAutomaticDockerInstallHost(host, string(initRaw)); err != nil {
				return hostRuntimeProbe{}, err
			}
		}
		steps, err := ubuntuDockerPrerequisitePlan(host)
		if err != nil {
			return hostRuntimeProbe{}, err
		}
		if planWriter != nil {
			fmt.Fprintln(planWriter, "Loki needs the following host prerequisites:")
			for _, step := range steps {
				fmt.Fprintf(planWriter, "  - %s\n    %s\n", step.Description, formatHostCommand(step, os.Geteuid() == 0))
			}
		}
		if err = executeHostCommandPlan(ctx, steps, executor); err != nil {
			return hostRuntimeProbe{}, err
		}
		if direct, probeErr := probeHostRuntime(ctx, requirements, []string{"docker"}, executor); probeErr == nil {
			return direct, nil
		}
	}

	if allowSudo {
		if sudoProbe, err := probeHostRuntime(ctx, requirements, []string{"sudo", "docker"}, executor); err == nil {
			return sudoProbe, nil
		}
	}
	return hostRuntimeProbe{}, errors.New("Docker is installed but Loki cannot use a compatible Engine and Compose v2; allow the explicit sudo Docker boundary or fix the Docker installation")
}
