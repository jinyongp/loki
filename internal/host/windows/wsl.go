package windows

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os/exec"
	"strings"
)

type NativeRunner interface {
	Run(context.Context, string, []string) (NativeProbe, error)
}

type ExecNativeRunner struct{}

func (ExecNativeRunner) Run(ctx context.Context, executable string, arguments []string) (NativeProbe, error) {
	command := exec.CommandContext(ctx, executable, arguments...)
	var stdout, stderr bytes.Buffer
	command.Stdout = &stdout
	command.Stderr = &stderr
	err := command.Run()
	probe := NativeProbe{
		ExitCode: 0,
		Stdout:   strings.TrimSpace(strings.ReplaceAll(stdout.String(), "\x00", "")),
		Stderr:   strings.TrimSpace(strings.ReplaceAll(stderr.String(), "\x00", "")),
	}
	if err == nil {
		return probe, nil
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		probe.ExitCode = exitError.ExitCode()
		return probe, nil
	}
	return NativeProbe{}, fmt.Errorf("start %s: %w", executable, err)
}

type WSLClient struct {
	Runner NativeRunner
	Exe    string
}

func (client WSLClient) executable() string {
	if strings.TrimSpace(client.Exe) != "" {
		return client.Exe
	}
	return "wsl.exe"
}

func (client WSLClient) ListDistributions(ctx context.Context) ([]string, error) {
	result, err := client.run(ctx, "--list", "--quiet")
	if err != nil {
		return nil, err
	}
	if result.ExitCode != 0 {
		return nil, nativeFailure("list WSL distributions", result)
	}
	lines := strings.Split(strings.ReplaceAll(result.Stdout, "\r\n", "\n"), "\n")
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		value := strings.TrimSpace(strings.ReplaceAll(line, "\x00", ""))
		if value != "" {
			out = append(out, value)
		}
	}
	return out, nil
}

func (client WSLClient) RequireInstallCapabilities(ctx context.Context) error {
	result, err := client.run(ctx, "--help")
	if err != nil {
		return err
	}
	if result.ExitCode != -1 && result.ExitCode != 0 && result.ExitCode != 1 {
		return nativeFailure("inspect WSL capabilities", result)
	}
	for _, required := range []string{"--from-file", "--name", "--no-launch"} {
		if !strings.Contains(result.Stdout, required) {
			return errors.New("WSL 2.4.4 or newer is required for custom .wsl distributions")
		}
	}
	return nil
}

func (client WSLClient) DistributionPresent(ctx context.Context, distribution string) (bool, error) {
	distributions, err := client.ListDistributions(ctx)
	if err != nil {
		return false, err
	}
	for _, candidate := range distributions {
		if strings.EqualFold(candidate, distribution) {
			return true, nil
		}
	}
	return false, nil
}

func (client WSLClient) VerifyDistributionIdentity(ctx context.Context, distribution, expectedVersion string) error {
	if strings.TrimSpace(expectedVersion) == "" {
		return errors.New("expected Loki distribution version is empty")
	}
	manifest, err := client.run(ctx, "-d", distribution, "--user", "root", "--exec",
		"/bin/cat", "/usr/lib/loki-appliance/release-manifest.json")
	if err != nil {
		return err
	}
	version, err := client.run(ctx, "-d", distribution, "--user", "root", "--exec",
		"/usr/lib/loki-appliance/loki", "version")
	if err != nil {
		return err
	}
	actualVersion, owned := distributionIdentity(manifest, version)
	if !owned || actualVersion != expectedVersion {
		return fmt.Errorf("WSL distribution %q no longer matches approved Loki appliance identity", distribution)
	}
	return nil
}

func (client WSLClient) ProbeDistribution(ctx context.Context, distribution string, present bool) (DistributionProbe, error) {
	probe := DistributionProbe{Present: present}
	if !present {
		return probe, nil
	}
	var err error
	probe.Manifest, err = client.run(ctx, "-d", distribution, "--user", "root", "--exec",
		"/bin/cat", "/usr/lib/loki-appliance/release-manifest.json")
	if err != nil {
		return DistributionProbe{}, err
	}
	probe.Version, err = client.run(ctx, "-d", distribution, "--user", "root", "--exec",
		"/usr/lib/loki-appliance/loki", "version")
	if err != nil {
		return DistributionProbe{}, err
	}
	if probe.Manifest.ExitCode != 0 || probe.Version.ExitCode != 0 {
		return probe, nil
	}
	probe.Provisioned, err = client.run(ctx, "-d", distribution, "--user", "root", "--exec",
		"/usr/bin/test", "-f", "/var/lib/loki-appliance/provisioned")
	if err != nil {
		return DistributionProbe{}, err
	}
	switch probe.Provisioned.ExitCode {
	case 0:
		probe.Doctor, err = client.run(ctx, "-d", distribution, "--user", "root", "--exec",
			"/usr/local/bin/loki", "host", "doctor", "--system")
		if err != nil {
			return DistributionProbe{}, err
		}
		probe.Connection, err = client.run(ctx, "-d", distribution, "--user", "root", "--exec",
			"/usr/local/bin/loki", "host", "connection", "--system", "--json")
		if err != nil {
			return DistributionProbe{}, err
		}
	case 1:
		probe.ServiceState, err = client.run(ctx, "-d", distribution, "--user", "root", "--exec",
			"/usr/bin/systemctl", "show", "loki-appliance-provision.service", "--no-pager",
			"--property=ActiveState", "--property=SubState", "--property=Result",
			"--property=NRestarts", "--property=ExecMainStatus")
		if err != nil {
			return DistributionProbe{}, err
		}
	}
	return probe, nil
}

func (client WSLClient) Terminate(ctx context.Context, distribution string) error {
	result, err := client.run(ctx, "--terminate", distribution)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return nativeFailure("terminate WSL distribution", result)
	}
	return nil
}

func (client WSLClient) Unregister(ctx context.Context, distribution string) error {
	result, err := client.run(ctx, "--unregister", distribution)
	if err != nil {
		return err
	}
	if result.ExitCode != 0 {
		return nativeFailure("unregister WSL distribution", result)
	}
	return nil
}

func (client WSLClient) run(ctx context.Context, arguments ...string) (NativeProbe, error) {
	if client.Runner == nil {
		return NativeProbe{}, errors.New("WSL native runner is unavailable")
	}
	return client.Runner.Run(ctx, client.executable(), arguments)
}

func nativeFailure(operation string, result NativeProbe) error {
	detail := result.Stderr
	if detail == "" {
		detail = result.Stdout
	}
	if detail == "" {
		return fmt.Errorf("%s failed with exit code %d", operation, result.ExitCode)
	}
	return fmt.Errorf("%s failed with exit code %d: %s", operation, result.ExitCode, detail)
}
