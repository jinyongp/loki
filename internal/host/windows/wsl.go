package windows

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"strings"

	"loki/internal/progress"
)

type NativeRunner interface {
	Run(context.Context, string, []string) (NativeProbe, error)
}

type NativeStreamingRunner interface {
	RunStreaming(context.Context, string, []string, progress.Reporter) (NativeProbe, error)
}

type NativeInputRunner interface {
	RunInput(context.Context, string, []string, []byte) (NativeProbe, error)
}

type NativeInputStreamingRunner interface {
	RunInputStreaming(context.Context, string, []string, []byte, progress.Reporter) (NativeProbe, error)
}

type ExecNativeRunner struct{}

func normalizeNativeExitCode(code int) int {
	if int64(code) == int64(^uint32(0)) {
		return -1
	}
	return code
}

func (runner ExecNativeRunner) Run(ctx context.Context, executable string, arguments []string) (NativeProbe, error) {
	return runner.run(ctx, executable, arguments, nil, nil)
}

func (runner ExecNativeRunner) RunInput(ctx context.Context, executable string, arguments []string, input []byte) (NativeProbe, error) {
	return runner.run(ctx, executable, arguments, nil, input)
}

func (runner ExecNativeRunner) RunInputStreaming(ctx context.Context, executable string, arguments []string, input []byte, reporter progress.Reporter) (NativeProbe, error) {
	return runner.run(ctx, executable, arguments, reporter, input)
}

func (runner ExecNativeRunner) RunStreaming(
	ctx context.Context,
	executable string,
	arguments []string,
	reporter progress.Reporter,
) (NativeProbe, error) {
	return runner.run(ctx, executable, arguments, reporter, nil)
}

func (ExecNativeRunner) run(
	ctx context.Context,
	executable string,
	arguments []string,
	reporter progress.Reporter,
	input []byte,
) (NativeProbe, error) {
	command := exec.CommandContext(ctx, executable, arguments...)
	configureNativeProcess(command)
	if progress.Verbose(reporter) {
		command.Env = progress.VerboseEnvironment(os.Environ())
	}
	if input != nil {
		command.Stdin = bytes.NewReader(input)
	}
	var stdout, stderr bytes.Buffer
	relay := newNativeProgressRelay(reporter)
	command.Stdout = &stdout
	if relay == nil {
		command.Stderr = &stderr
	} else {
		command.Stderr = io.MultiWriter(&stderr, relay)
	}
	err := command.Run()
	if relay != nil {
		relay.Flush()
	}
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
		probe.ExitCode = normalizeNativeExitCode(exitError.ExitCode())
		return probe, nil
	}
	return NativeProbe{}, fmt.Errorf("start %s: %w", executable, err)
}

type nativeProgressRelay struct {
	reporter progress.Reporter
	pending  string
}

func newNativeProgressRelay(reporter progress.Reporter) *nativeProgressRelay {
	if reporter == nil {
		return nil
	}
	return &nativeProgressRelay{reporter: reporter}
}

func (relay *nativeProgressRelay) Write(raw []byte) (int, error) {
	if relay == nil {
		return len(raw), nil
	}
	relay.pending += strings.ReplaceAll(string(raw), "\x00", "")
	for {
		index := strings.IndexAny(relay.pending, "\r\n")
		if index < 0 {
			break
		}
		line := relay.pending[:index]
		next := index + 1
		if relay.pending[index] == '\r' && next < len(relay.pending) && relay.pending[next] == '\n' {
			next++
		}
		relay.pending = relay.pending[next:]
		relay.writeLine(line)
	}
	return len(raw), nil
}

func (relay *nativeProgressRelay) Flush() {
	if relay == nil || relay.pending == "" {
		return
	}
	line := strings.TrimSuffix(relay.pending, "\r")
	relay.pending = ""
	relay.writeLine(line)
}

func (relay *nativeProgressRelay) writeLine(line string) {
	line = strings.TrimSpace(line)
	if !progress.IsProgressLine(line) {
		return
	}
	progress.Emit(relay.reporter, progress.Event{
		State:   progress.StateInfo,
		Message: strings.TrimPrefix(line, progress.LinePrefix),
	})
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
	result.ExitCode = normalizeNativeExitCode(result.ExitCode)
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
	if manifest.ExitCode != 0 {
		return nativeFailure("read Loki appliance identity manifest", manifest)
	}
	version, err := client.run(ctx, "-d", distribution, "--user", "root", "--exec",
		"/usr/lib/loki-appliance/loki", "version")
	if err != nil {
		return err
	}
	if version.ExitCode != 0 {
		return nativeFailure("read Loki appliance identity version", version)
	}
	actualVersion, owned := distributionIdentity(manifest, version)
	if !owned || actualVersion != expectedVersion {
		return fmt.Errorf("WSL distribution %q no longer matches approved Loki appliance identity", distribution)
	}
	return nil
}

func (client WSLClient) OwnedDistributionVersion(ctx context.Context, distribution string) (string, error) {
	manifest, err := client.run(ctx, "-d", distribution, "--user", "root", "--exec",
		"/bin/cat", "/usr/lib/loki-appliance/release-manifest.json")
	if err != nil {
		return "", err
	}
	if manifest.ExitCode != 0 {
		return "", nativeFailure("read Loki appliance identity manifest", manifest)
	}
	version, err := client.run(ctx, "-d", distribution, "--user", "root", "--exec",
		"/usr/lib/loki-appliance/loki", "version")
	if err != nil {
		return "", err
	}
	if version.ExitCode != 0 {
		return "", nativeFailure("read Loki appliance identity version", version)
	}
	actualVersion, owned := distributionIdentity(manifest, version)
	if !owned {
		return "", fmt.Errorf("WSL distribution %q does not match Loki appliance identity", distribution)
	}
	return actualVersion, nil
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

func (client WSLClient) runInput(ctx context.Context, input []byte, arguments ...string) (NativeProbe, error) {
	if client.Runner == nil {
		return NativeProbe{}, errors.New("WSL native runner is unavailable")
	}
	inputRunner, ok := client.Runner.(NativeInputRunner)
	if !ok {
		return NativeProbe{}, errors.New("WSL native runner does not support stdin")
	}
	return inputRunner.RunInput(ctx, client.executable(), arguments, input)
}

func (client WSLClient) runInputStreaming(ctx context.Context, input []byte, reporter progress.Reporter, arguments ...string) (NativeProbe, error) {
	if streaming, ok := client.Runner.(NativeInputStreamingRunner); ok {
		return streaming.RunInputStreaming(ctx, client.executable(), arguments, input, reporter)
	}
	result, err := client.runInput(ctx, input, arguments...)
	if err == nil {
		relay := newNativeProgressRelay(reporter)
		if relay != nil {
			_, _ = relay.Write([]byte(result.Stderr))
			relay.Flush()
		}
	}
	return result, err
}

func (client WSLClient) runStreaming(
	ctx context.Context,
	reporter progress.Reporter,
	arguments ...string,
) (NativeProbe, error) {
	if client.Runner == nil {
		return NativeProbe{}, errors.New("WSL native runner is unavailable")
	}
	if streaming, ok := client.Runner.(NativeStreamingRunner); ok {
		return streaming.RunStreaming(ctx, client.executable(), arguments, reporter)
	}
	result, err := client.Runner.Run(ctx, client.executable(), arguments)
	if err != nil || reporter == nil {
		return result, err
	}
	for _, line := range strings.Split(strings.ReplaceAll(result.Stderr, "\r\n", "\n"), "\n") {
		line = strings.TrimSpace(line)
		if progress.IsProgressLine(line) {
			progress.Emit(reporter, progress.Event{
				State:   progress.StateInfo,
				Message: strings.TrimPrefix(line, progress.LinePrefix),
			})
		}
	}
	return result, nil
}

func nativeFailure(operation string, result NativeProbe) error {
	detail := progress.NonProgressText(result.Stderr)
	if detail == "" {
		detail = result.Stdout
	}
	if detail == "" {
		return fmt.Errorf("%s failed with exit code %d", operation, result.ExitCode)
	}
	return fmt.Errorf("%s failed with exit code %d: %s", operation, result.ExitCode, detail)
}
