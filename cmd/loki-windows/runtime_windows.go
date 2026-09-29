//go:build windows

package main

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"os/signal"
	"strings"

	"golang.org/x/term"

	windowshost "loki/internal/host/windows"
	"loki/internal/progress"
)

const defaultWindowsDistribution = "loki-mcp"

func runWindowsCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()

	switch args[0] {
	case "bootstrap":
		return runBootstrap(ctx, args[1:], stdout, stderr)
	case "install":
		return runInstall(ctx, args[1:], stdout, stderr)
	case "status":
		return runStatus(ctx, args[1:], stdout, stderr)
	case "doctor":
		return runDoctor(ctx, args[1:], stdout, stderr)
	case "connection":
		return runConnection(ctx, args[1:], stdout, stderr)
	case "update":
		return runUpdate(ctx, args[1:], stdout, stderr)
	case "backup":
		return runMaintenance(ctx, "backup", args[1:], stdout, stderr)
	case "rollback":
		return runMaintenance(ctx, "rollback", args[1:], stdout, stderr)
	case "restore":
		return runRestore(ctx, args[1:], stdout, stderr)
	case "uninstall":
		return runUninstall(ctx, args[1:], stdout, stderr)
	case "connect":
		return runConnect(ctx, args[1:], stdout, stderr)
	default:
		fmt.Fprintf(stderr, "unknown Windows Loki command %q\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func runBootstrap(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 || args[0] != "install" {
		fmt.Fprintln(stderr, "usage: loki bootstrap install [INSTALL_OPTIONS]")
		return 2
	}
	binding, err := windowshost.CurrentReleaseBinding()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	localAppData, _, err := windowsEnvironmentRoots()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	paths, err := windowshost.ResolveFrontendPaths(localAppData)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	source, err := os.Executable()
	if err != nil {
		fmt.Fprintln(stderr, "resolve trusted Windows frontend executable:", err)
		return 1
	}
	result, err := (windowshost.FrontendInstaller{
		Platform: windowshost.NewWindowsFrontendPlatform(),
	}).Install(ctx, source, paths, binding)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "Windows Loki frontend %s: %s\n", result.Ownership.ReleaseTag, result.Disposition)
	if result.PathChanged {
		fmt.Fprintf(stdout, "Persistent user PATH now includes %s\n", paths.BinDir)
	}
	return runCanonicalInstall(ctx, paths.Binary, args[1:], stdout, stderr)
}

func runCanonicalInstall(ctx context.Context, binary string, args []string, stdout, stderr io.Writer) int {
	command := exec.CommandContext(ctx, binary, append([]string{"install"}, args...)...)
	command.Stdin = os.Stdin
	command.Stdout = stdout
	command.Stderr = stderr
	err := command.Run()
	if err == nil {
		return 0
	}
	var exitError *exec.ExitError
	if errors.As(err, &exitError) {
		return exitError.ExitCode()
	}
	fmt.Fprintln(stderr, "start canonical Windows Loki frontend:", err)
	return 1
}

func runInstall(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	binding, err := windowshost.CurrentReleaseBinding()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	options, err := windowshost.ResolveInstallOptionsWithArgs(args, os.LookupEnv)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	expected, err := expectedInstallation(options.Distribution, options)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	approve := func(windowshost.ExistingSnapshot) (bool, error) {
		return confirmWindows(
			fmt.Sprintf("Reinstall stale Loki appliance %q and remove its verified local state?", options.Distribution),
			stdout,
		)
	}
	reporter := progress.NewLineReporter(stderr)
	progress.Emit(reporter, progress.Event{Operation: "install", Phase: "inspect", State: progress.StateStarted, Message: "Inspecting the existing Windows Loki installation..."})
	result, err := windowshost.NewWindowsInstallControllerWithProgress(binding, reporter).Run(
		ctx, expected, options, approve,
	)
	if err != nil {
		fmt.Fprintln(stderr, formatWindowsInstallError(err, options.Distribution, options.MCPPort))
		return 1
	}
	switch result.Disposition {
	case windowshost.InstallNoop:
		fmt.Fprintf(stdout, "Loki appliance %q is already healthy and current.\n", options.Distribution)
	case windowshost.InstallUpgradeRequired:
		return upgradeExistingAppliance(ctx, options.Distribution, result.CurrentVersion, binding.ReleaseTag, stdout, stderr)
	case windowshost.InstallCompleted:
		fmt.Fprintf(stdout, "Installed Loki appliance %q.\n", options.Distribution)
	default:
		fmt.Fprintf(stdout, "Loki appliance %q: %s\n", options.Distribution, result.Disposition)
	}
	fmt.Fprintln(stdout, "Run 'loki status' or 'loki connection' for operator details.")
	return 0
}

func upgradeExistingAppliance(
	ctx context.Context,
	distribution, currentVersion, targetTag string,
	stdout, stderr io.Writer,
) int {
	reporter := progress.NewLineReporter(stderr)
	progress.Emit(reporter, progress.Event{
		Operation: "update", Phase: "appliance", State: progress.StateStarted,
		Message: fmt.Sprintf("Updating Loki appliance %q from v%s to %s...", distribution, strings.TrimPrefix(currentVersion, "v"), targetTag),
	})

	client := windowshost.NewWindowsOperatorClient()
	stopHeartbeat := progress.StartHeartbeat(ctx, reporter, progress.HeartbeatOptions{
		Operation: "update", Phase: "prepare", Message: "Still preparing the appliance update",
	})
	prepared, err := client.ExecuteStreaming(
		ctx, distribution, windowshost.OperatorRequest{Command: "update", Action: "prepare"}, reporter,
	)
	stopHeartbeat()
	if err != nil {
		fmt.Fprintln(stderr, "prepare managed appliance update:", err)
		return 1
	}
	if prepared.Probe.ExitCode != 0 {
		fmt.Fprintln(stderr, "The existing appliance is healthy but cannot be updated in place by this frontend.")
		fmt.Fprintln(stderr, "No destructive reinstall was attempted.")
		return writeNativeProbe(prepared.Probe, stdout, stderr)
	}
	updateStatus, err := client.Execute(ctx, distribution, windowshost.OperatorRequest{Command: "update", Action: "status"})
	if err != nil {
		fmt.Fprintln(stderr, "verify prepared appliance update:", err)
		return 1
	}
	if updateStatus.Probe.ExitCode != 0 {
		return writeNativeProbe(updateStatus.Probe, stdout, stderr)
	}
	status, err := decodeMachineJSON[machineUpdateStatus](updateStatus.Probe.Stdout)
	if err != nil || status.Available == nil || status.Prepared == nil {
		fmt.Fprintln(stderr, "prepared appliance update could not be verified against the Windows frontend release")
		return 1
	}
	preparedTag := "v" + strings.TrimPrefix(status.Available.Spec.Version, "v")
	if preparedTag != targetTag {
		fmt.Fprintf(stderr, "refusing appliance update to %s; Windows frontend is bound to %s\n", preparedTag, targetTag)
		return 1
	}
	progress.Emit(reporter, progress.Event{Operation: "update", Phase: "apply", State: progress.StateStarted, Message: "Applying the prepared appliance update..."})
	stopHeartbeat = progress.StartHeartbeat(ctx, reporter, progress.HeartbeatOptions{
		Operation: "update", Phase: "apply", Message: "Still applying the appliance update",
	})
	applied, err := client.ExecuteStreaming(ctx, distribution, windowshost.OperatorRequest{
		Command: "update", Action: "apply", Approve: true,
	}, reporter)
	stopHeartbeat()
	if err != nil {
		fmt.Fprintln(stderr, "apply managed appliance update:", err)
		return 1
	}
	if applied.Probe.ExitCode != 0 {
		return writeNativeProbe(applied.Probe, stdout, stderr)
	}
	postStatus, err := client.Execute(ctx, distribution, windowshost.OperatorRequest{Command: "update", Action: "status"})
	if err != nil {
		fmt.Fprintln(stderr, "verify updated appliance release:", err)
		return 1
	}
	if postStatus.Probe.ExitCode != 0 {
		return writeNativeProbe(postStatus.Probe, stdout, stderr)
	}
	updated, err := decodeMachineJSON[machineUpdateStatus](postStatus.Probe.Stdout)
	if err != nil || updated.Installed == nil {
		fmt.Fprintln(stderr, "updated appliance release could not be verified")
		return 1
	}
	actualTag := "v" + strings.TrimPrefix(updated.Installed.Spec.Version, "v")
	if actualTag != targetTag {
		fmt.Fprintf(stderr, "updated appliance release %s does not match Windows frontend release %s\n",
			actualTag, targetTag)
		return 1
	}
	progress.Emit(reporter, progress.Event{Operation: "update", Phase: "resync", State: progress.StateStarted, Message: "Refreshing the Windows MCP connection state..."})
	syncResult, err := syncAfterLifecycleMutation(ctx, distribution, reporter)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if err = renderAppliedUpdate(applied.Probe.Stdout, stdout); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if syncResult.Changed {
		fmt.Fprintln(stdout, "Refreshed Windows MCP connection state from the live appliance.")
	}
	fmt.Fprintf(stdout, "Loki appliance %q is now updated to %s.\n", distribution, targetTag)
	return 0
}

func runStatus(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	distribution, jsonOutput, err := parseInfoFlags("status", args, true)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	result, err := windowshost.NewWindowsOperatorClient().Execute(
		ctx, distribution, windowshost.OperatorRequest{Command: "status"},
	)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if result.Probe.ExitCode != 0 {
		return writeNativeProbe(result.Probe, stdout, stderr)
	}
	status, err := windowshost.ParseOperatorStatus([]byte(result.Probe.Stdout))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	binding, err := windowshost.CurrentReleaseBinding()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if jsonOutput {
		payload := struct {
			SchemaVersion int                        `json:"schema_version"`
			Distribution  string                     `json:"distribution"`
			Frontend      windowshost.ReleaseBinding `json:"frontend"`
			ApplianceBase string                     `json:"appliance_base_version"`
			Status        json.RawMessage            `json:"status"`
		}{
			SchemaVersion: 1,
			Distribution:  distribution,
			Frontend:      binding,
			ApplianceBase: result.DistributionVersion,
			Status:        json.RawMessage(result.Probe.Stdout),
		}
		if err = json.NewEncoder(stdout).Encode(payload); err != nil {
			fmt.Fprintln(stderr, "encode Windows Loki status:", err)
			return 1
		}
		return 0
	}
	fmt.Fprintln(stdout, "Loki Windows")
	fmt.Fprintf(stdout, "  Distribution: %s\n", distribution)
	fmt.Fprintf(stdout, "  Frontend: %s\n", binding.ReleaseTag)
	fmt.Fprintf(stdout, "  Appliance image: v%s\n", result.DistributionVersion)
	if status.Release != "" {
		fmt.Fprintf(stdout, "  Appliance release: v%s\n", strings.TrimPrefix(status.Release, "v"))
	}
	fmt.Fprintf(stdout, "  Status: %s\n", status.State)
	switch {
	case status.UpdatePrepared:
		fmt.Fprintln(stdout, "  Update: prepared")
	case status.UpdateAvailable:
		fmt.Fprintln(stdout, "  Update: available")
	default:
		fmt.Fprintln(stdout, "  Update: none prepared")
	}
	return 0
}

func runDoctor(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	distribution, jsonOutput, err := parseInfoFlags("doctor", args, true)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	result, err := windowshost.NewWindowsOperatorClient().Execute(
		ctx, distribution, windowshost.OperatorRequest{Command: "doctor"},
	)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if result.Probe.Stderr != "" {
		fmt.Fprintln(stderr, result.Probe.Stderr)
	}
	if result.Probe.Stdout != "" {
		if jsonOutput {
			if err = writeMachineJSON(stdout, result.Probe.Stdout); err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
		} else if err = renderDoctor(result.Probe.Stdout, stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	return nativeProbeExitCode(result.Probe)
}

func runConnect(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "status":
			return runLegacyConnectStatus(ctx, args[1:], stdout, stderr)
		case "startup":
			return runConnectionStartup(ctx, args[1:], stdout, stderr)
		}
	}
	return runConnection(ctx, args, stdout, stderr)
}

func runLegacyConnectStatus(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("loki connect status", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	distribution := flags.String("distribution", defaultDistribution(), "WSL distribution name")
	jsonOutput := flags.Bool("json", false, "emit machine-readable JSON")
	if err := flags.Parse(args); err != nil || flags.NArg() > 1 {
		fmt.Fprintln(stderr, "usage: loki connection list [--distribution NAME] [--json] | loki connection show [--distribution NAME] [--json] PROVIDER")
		return 2
	}
	translated := []string{"list", "--distribution", *distribution}
	if flags.NArg() == 1 {
		translated[0] = "show"
	}
	if *jsonOutput {
		translated = append(translated, "--json")
	}
	if flags.NArg() == 1 {
		translated = append(translated, flags.Arg(0))
	}
	return runConnection(ctx, translated, stdout, stderr)
}

func runConnectionSetup(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("loki connection setup", flag.ContinueOnError)
	flags.SetOutput(stderr)
	distribution := flags.String("distribution", defaultDistribution(), "WSL distribution name")
	tunnelID := flags.String("tunnel-id", "", "existing OpenAI tunnel id")
	runtimeKeyEnv := flags.String("runtime-key-env", "", "environment variable containing the OpenAI runtime API key")
	credentialTarget := flags.String("runtime-key-credential", "", "existing Loki Windows Credential Manager target")
	if err := flags.Parse(args); err != nil || flags.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: loki connection setup [--distribution NAME] [--tunnel-id ID] [--runtime-key-env NAME | --runtime-key-credential TARGET] PROVIDER")
		return 2
	}
	if err := windowshost.ValidateDistributionName(*distribution); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	provider := flags.Arg(0)
	if provider != windowshost.OpenAIProviderID {
		fmt.Fprintf(stderr, "unsupported managed connection provider %q\n", provider)
		return 2
	}
	if *runtimeKeyEnv != "" && *credentialTarget != "" {
		fmt.Fprintln(stderr, "--runtime-key-env and --runtime-key-credential are mutually exclusive")
		return 2
	}

	config := windowshost.OpenAISetupConfig{
		TunnelID:         strings.TrimSpace(*tunnelID),
		CredentialTarget: strings.TrimSpace(*credentialTarget),
	}
	fmt.Fprintln(stdout, "OpenAI Secure MCP Tunnel setup references:")
	fmt.Fprintf(stdout, "  Tunnels: %s\n", windowshost.OpenAITunnelsURL)
	fmt.Fprintf(stdout, "  Runtime API keys: %s\n", windowshost.OpenAIRuntimeKeysURL)
	fmt.Fprintf(stdout, "  ChatGPT connectors: %s\n", windowshost.OpenAIConnectorsURL)

	if config.TunnelID == "" && term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(stdout, "OpenAI tunnel ID: ")
		line, err := bufio.NewReader(os.Stdin).ReadString('\n')
		if err != nil && !errors.Is(err, io.EOF) {
			fmt.Fprintln(stderr, err)
			return 1
		}
		config.TunnelID = strings.TrimSpace(line)
	}
	if name := strings.TrimSpace(*runtimeKeyEnv); name != "" {
		if !validEnvironmentVariableName(name) {
			fmt.Fprintln(stderr, "--runtime-key-env must name a valid environment variable")
			return 2
		}
		value, ok := os.LookupEnv(name)
		if !ok || value == "" {
			fmt.Fprintf(stderr, "environment variable %s is not set\n", name)
			return 1
		}
		config.RuntimeKey = value
	} else if config.CredentialTarget == "" && term.IsTerminal(int(os.Stdin.Fd())) {
		fmt.Fprint(stdout, "OpenAI runtime API key: ")
		raw, err := term.ReadPassword(int(os.Stdin.Fd()))
		fmt.Fprintln(stdout)
		if err != nil {
			fmt.Fprintln(stderr, "read OpenAI runtime API key:", err)
			return 1
		}
		defer clear(raw)
		config.RuntimeKey = strings.TrimSpace(string(raw))
	}

	reporter := progress.NewLineReporter(stderr)
	manager, err := newWindowsConnectionManagerWithAdaptersAndProgress(
		windowshost.WindowsConnectionAdaptersWithOpenAISetup(config), reporter,
	)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	progress.Emit(reporter, progress.Event{
		Operation: "connection", Phase: "setup", State: progress.StateStarted,
		Message: fmt.Sprintf("Configuring managed %s connection and verifying its helper runtime...", provider),
	})
	stopHeartbeat := progress.StartHeartbeat(ctx, reporter, progress.HeartbeatOptions{
		Operation: "connection", Phase: "setup", Message: "Still configuring the managed connection",
	})
	err = manager.Setup(ctx, *distribution, provider)
	stopHeartbeat()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "Managed %s connection setup completed.\n", provider)
	return 0
}

func validEnvironmentVariableName(value string) bool {
	if value == "" {
		return false
	}
	for index, char := range value {
		if index == 0 {
			if char != '_' && (char < 'A' || char > 'Z') && (char < 'a' || char > 'z') {
				return false
			}
			continue
		}
		if char != '_' && (char < 'A' || char > 'Z') && (char < 'a' || char > 'z') &&
			(char < '0' || char > '9') {
			return false
		}
	}
	return true
}

func runConnectionMutation(ctx context.Context, action string, args []string, stdout, stderr io.Writer) int {
	if action == "setup" {
		return runConnectionSetup(ctx, args, stdout, stderr)
	}
	flags := flag.NewFlagSet("loki connection "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	distribution := flags.String("distribution", defaultDistribution(), "WSL distribution name")
	if err := flags.Parse(args); err != nil || flags.NArg() != 1 {
		fmt.Fprintf(stderr, "usage: loki connection %s [--distribution NAME] PROVIDER\n", action)
		return 2
	}
	if err := windowshost.ValidateDistributionName(*distribution); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	provider := flags.Arg(0)
	reporter := progress.NewLineReporter(stderr)
	manager, err := newWindowsConnectionManagerWithProgress(reporter)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	message := map[string]string{
		"start":  fmt.Sprintf("Starting managed %s connection...", provider),
		"stop":   fmt.Sprintf("Stopping managed %s connection...", provider),
		"remove": fmt.Sprintf("Removing managed %s connection...", provider),
	}[action]
	progress.Emit(reporter, progress.Event{
		Operation: "connection", Phase: action, State: progress.StateStarted, Message: message,
	})
	stopHeartbeat := progress.StartHeartbeat(ctx, reporter, progress.HeartbeatOptions{
		Operation: "connection", Phase: action, Message: "Still waiting for the managed connection operation",
	})
	switch action {
	case "setup":
		err = manager.Setup(ctx, *distribution, provider)
	case "start":
		err = manager.Start(ctx, *distribution, provider)
	case "stop":
		err = manager.Stop(ctx, *distribution, provider)
	case "remove":
		err = manager.Remove(ctx, *distribution, provider)
	}
	stopHeartbeat()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "Managed %s connection %s completed.\n", provider, action)
	return 0
}

func runConnectionStartup(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("loki connect startup", flag.ContinueOnError)
	flags.SetOutput(stderr)
	distribution := flags.String("distribution", defaultDistribution(), "WSL distribution name")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 2
	}
	if err := windowshost.ValidateDistributionName(*distribution); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	manager, err := newWindowsConnectionManager()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	expected, err := expectedInstallation(*distribution, windowshost.InstallOptions{Distribution: *distribution})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	localAppData, _, err := windowsEnvironmentRoots()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	controller := windowshost.NewWindowsConnectionStartupController(localAppData, &manager)
	result, err := controller.Run(ctx, expected)
	if err != nil {
		fmt.Fprintf(stderr, "managed connection startup failed after %d health attempts: %v\n", result.HealthAttempts, err)
		return 1
	}
	fmt.Fprintf(stdout, "Managed connection startup restored %d enabled connection(s); keepalive_started=%t health_attempts=%d.\n",
		result.EnabledConnections, result.KeepaliveStarted, result.HealthAttempts)
	return 0
}

func newWindowsConnectionManager() (windowshost.ConnectionManager, error) {
	return newWindowsConnectionManagerWithProgress(nil)
}

func newWindowsConnectionManagerWithProgress(
	reporter progress.Reporter,
) (windowshost.ConnectionManager, error) {
	return newWindowsConnectionManagerWithAdaptersAndProgress(
		windowshost.WindowsConnectionAdapters(), reporter,
	)
}

func newWindowsConnectionManagerWithAdapters(
	adapters []windowshost.RemoteConnectionAdapter,
) (windowshost.ConnectionManager, error) {
	return newWindowsConnectionManagerWithAdaptersAndProgress(adapters, nil)
}

func newWindowsConnectionManagerWithAdaptersAndProgress(
	adapters []windowshost.RemoteConnectionAdapter,
	reporter progress.Reporter,
) (windowshost.ConnectionManager, error) {
	binding, err := windowshost.CurrentReleaseBinding()
	if err != nil {
		return windowshost.ConnectionManager{}, err
	}
	localAppData, _, err := windowsEnvironmentRoots()
	if err != nil {
		return windowshost.ConnectionManager{}, err
	}
	return windowshost.NewWindowsConnectionManagerWithProgress(binding, localAppData, adapters, reporter)
}

func runUpdate(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		return runProductUpdate(ctx, stdout, stderr)
	}
	action := args[0]
	flags := flag.NewFlagSet("loki update "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	distribution := flags.String("distribution", defaultDistribution(), "WSL distribution name")
	jsonOutput := flags.Bool("json", false, "emit machine-readable JSON")
	approve := flags.Bool("approve", false, "approve update apply")
	interrupt := flags.Bool("interrupt-active-jobs", false, "approve interrupting active jobs")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		return 2
	}
	if err := windowshost.ValidateDistributionName(*distribution); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if action != "status" && action != "prepare" && action != "apply" {
		fmt.Fprintln(stderr, "update action must be status, prepare, or apply")
		return 2
	}
	if action != "apply" && (*approve || *interrupt) {
		fmt.Fprintln(stderr, "--approve and --interrupt-active-jobs are valid only for update apply")
		return 2
	}
	if action == "apply" && *jsonOutput && !*approve {
		fmt.Fprintln(stderr, "update apply --json requires --approve")
		return 2
	}
	client := windowshost.NewWindowsOperatorClient()
	if action == "apply" {
		preflight, preflightErr := client.Execute(ctx, *distribution, windowshost.OperatorRequest{
			Command: "update", Action: "status",
		})
		if preflightErr != nil {
			fmt.Fprintln(stderr, preflightErr)
			return 1
		}
		if preflight.Probe.ExitCode != 0 {
			return writeNativeProbe(preflight.Probe, stdout, stderr)
		}
		status, decodeErr := decodeMachineJSON[machineUpdateStatus](preflight.Probe.Stdout)
		if decodeErr != nil {
			fmt.Fprintln(stderr, decodeErr)
			return 1
		}
		if readinessErr := updateApplyReadinessError(status); readinessErr != nil {
			fmt.Fprintln(stderr, readinessErr)
			return 1
		}
	}
	if action == "apply" && !*approve {
		confirmed, err := confirmWindows("Apply the prepared Loki appliance update?", stdout)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		if !confirmed {
			fmt.Fprintln(stderr, "Loki appliance update cancelled.")
			return 1
		}
		*approve = true
	}
	reporter := progress.NewLineReporter(stderr)
	if action == "prepare" {
		progress.Emit(reporter, progress.Event{Operation: "update", Phase: "prepare", State: progress.StateStarted, Message: "Preparing the appliance update..."})
	} else if action == "apply" {
		progress.Emit(reporter, progress.Event{Operation: "update", Phase: "apply", State: progress.StateStarted, Message: "Applying the prepared appliance update..."})
	}
	request := windowshost.OperatorRequest{
		Command: "update", Action: action, Approve: *approve, InterruptActiveJobs: *interrupt,
	}
	var (
		result windowshost.OperatorResult
		err    error
	)
	if action == "status" {
		result, err = client.Execute(ctx, *distribution, request)
	} else {
		message := "Still preparing the appliance update"
		if action == "apply" {
			message = "Still applying the appliance update"
		}
		stopHeartbeat := progress.StartHeartbeat(ctx, reporter, progress.HeartbeatOptions{
			Operation: "update", Phase: action, Message: message,
		})
		result, err = client.ExecuteStreaming(ctx, *distribution, request, reporter)
		stopHeartbeat()
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if result.Probe.ExitCode != 0 {
		return writeNativeProbe(result.Probe, stdout, stderr)
	}
	if detail := progress.NonProgressText(result.Probe.Stderr); detail != "" {
		fmt.Fprintln(stderr, detail)
	}
	var syncResult windowshost.ReplicaSyncResult
	if action == "apply" {
		progress.Emit(reporter, progress.Event{Operation: "update", Phase: "resync", State: progress.StateStarted, Message: "Refreshing the Windows MCP connection state..."})
		syncResult, err = syncAfterLifecycleMutation(ctx, *distribution, reporter)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if *jsonOutput {
		if err = writeMachineJSON(stdout, result.Probe.Stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	} else {
		switch action {
		case "status":
			err = renderUpdateStatus(result.Probe.Stdout, stdout)
		case "prepare":
			err = renderPreparedUpdate(result.Probe.Stdout, stdout)
		case "apply":
			err = renderAppliedUpdate(result.Probe.Stdout, stdout)
		}
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		if action == "apply" && syncResult.Changed {
			fmt.Fprintln(stdout, "Refreshed Windows MCP connection state from the live appliance.")
		}
	}
	return 0
}

func runMaintenance(ctx context.Context, command string, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("loki "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	distribution := flags.String("distribution", defaultDistribution(), "WSL distribution name")
	jsonOutput := flags.Bool("json", false, "emit machine-readable JSON")
	interrupt := flags.Bool("interrupt-active-jobs", false, "approve interrupting active jobs")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 2
	}
	if err := windowshost.ValidateDistributionName(*distribution); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	reporter := progress.NewLineReporter(stderr)
	message := map[string]string{
		"backup":   "Creating an appliance lifecycle backup...",
		"rollback": "Rolling back the appliance lifecycle state...",
	}[command]
	progress.Emit(reporter, progress.Event{Operation: command, Phase: "execute", State: progress.StateStarted, Message: message})
	stopHeartbeat := progress.StartHeartbeat(ctx, reporter, progress.HeartbeatOptions{
		Operation: command, Phase: "execute", Message: "Still waiting for the appliance lifecycle operation",
	})
	result, err := windowshost.NewWindowsOperatorClient().ExecuteStreaming(ctx, *distribution, windowshost.OperatorRequest{
		Command: command, InterruptActiveJobs: *interrupt,
	}, reporter)
	stopHeartbeat()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if result.Probe.ExitCode != 0 {
		return writeNativeProbe(result.Probe, stdout, stderr)
	}
	if detail := progress.NonProgressText(result.Probe.Stderr); detail != "" {
		fmt.Fprintln(stderr, detail)
	}
	var syncResult windowshost.ReplicaSyncResult
	if command == "rollback" {
		progress.Emit(reporter, progress.Event{Operation: command, Phase: "resync", State: progress.StateStarted, Message: "Refreshing the Windows MCP connection state..."})
		syncResult, err = syncAfterLifecycleMutation(ctx, *distribution, reporter)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
	}
	if *jsonOutput {
		if err = writeMachineJSON(stdout, result.Probe.Stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	switch command {
	case "backup":
		err = renderBackup(result.Probe.Stdout, stdout)
	case "rollback":
		err = renderBooleanMutation(result.Probe.Stdout, "rolled_back", "Rolled back Loki appliance.", stdout)
	default:
		err = fmt.Errorf("unsupported Windows maintenance command %q", command)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if command == "rollback" && syncResult.Changed {
		fmt.Fprintln(stdout, "Refreshed Windows MCP connection state from the live appliance.")
	}
	return 0
}

func runRestore(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("loki restore", flag.ContinueOnError)
	flags.SetOutput(stderr)
	distribution := flags.String("distribution", defaultDistribution(), "WSL distribution name")
	jsonOutput := flags.Bool("json", false, "emit machine-readable JSON")
	interrupt := flags.Bool("interrupt-active-jobs", false, "approve interrupting active jobs")
	if err := flags.Parse(args); err != nil || flags.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: loki restore [--distribution NAME] [--json] [--interrupt-active-jobs] BACKUP_ID")
		return 2
	}
	if err := windowshost.ValidateDistributionName(*distribution); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	reporter := progress.NewLineReporter(stderr)
	progress.Emit(reporter, progress.Event{Operation: "restore", Phase: "execute", State: progress.StateStarted, Message: "Restoring the selected appliance lifecycle backup..."})
	stopHeartbeat := progress.StartHeartbeat(ctx, reporter, progress.HeartbeatOptions{
		Operation: "restore", Phase: "execute", Message: "Still restoring the appliance lifecycle backup",
	})
	result, err := windowshost.NewWindowsOperatorClient().ExecuteStreaming(ctx, *distribution, windowshost.OperatorRequest{
		Command: "restore", BackupID: flags.Arg(0), InterruptActiveJobs: *interrupt,
	}, reporter)
	stopHeartbeat()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if result.Probe.ExitCode != 0 {
		return writeNativeProbe(result.Probe, stdout, stderr)
	}
	if detail := progress.NonProgressText(result.Probe.Stderr); detail != "" {
		fmt.Fprintln(stderr, detail)
	}
	progress.Emit(reporter, progress.Event{Operation: "restore", Phase: "resync", State: progress.StateStarted, Message: "Refreshing the Windows MCP connection state..."})
	syncResult, err := syncAfterLifecycleMutation(ctx, *distribution, reporter)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *jsonOutput {
		if err = writeMachineJSON(stdout, result.Probe.Stdout); err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		return 0
	}
	if err = renderBooleanMutation(result.Probe.Stdout, "restored", "Restored Loki appliance backup.", stdout); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if syncResult.Changed {
		fmt.Fprintln(stdout, "Refreshed Windows MCP connection state from the live appliance.")
	}
	return 0
}

func runUninstall(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("loki uninstall", flag.ContinueOnError)
	flags.SetOutput(stderr)
	distribution := flags.String("distribution", defaultDistribution(), "WSL distribution name")
	approve := flags.Bool("approve", false, "approve destructive local uninstall")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 2
	}
	if err := windowshost.ValidateDistributionName(*distribution); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if !*approve {
		confirmed, err := confirmWindows(
			fmt.Sprintf("Uninstall verified local Loki appliance %q? The Windows CLI remains installed.", *distribution),
			stdout,
		)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
		if !confirmed {
			fmt.Fprintln(stderr, "Loki uninstall cancelled.")
			return 1
		}
		*approve = true
	}
	expected, err := expectedInstallation(*distribution, windowshost.InstallOptions{Distribution: *distribution})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	reporter := progress.NewLineReporter(stderr)
	connections, err := newWindowsConnectionManagerWithProgress(reporter)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	stopHeartbeat := progress.StartHeartbeat(ctx, reporter, progress.HeartbeatOptions{
		Operation: "uninstall", Phase: "remove", Message: "Still removing the local Loki installation",
	})
	err = windowshost.NewWindowsUninstallControllerWithProgress(
		&connections, reporter,
	).Run(ctx, expected, *approve)
	stopHeartbeat()
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "Uninstalled verified local Loki appliance %q.\n", *distribution)
	fmt.Fprintln(stdout, "The Windows Loki frontend and shared helper cache were left installed.")
	return 0
}

func syncAfterLifecycleMutation(
	ctx context.Context,
	distribution string,
	reporter progress.Reporter,
) (windowshost.ReplicaSyncResult, error) {
	expected, err := expectedInstallation(distribution, windowshost.InstallOptions{Distribution: distribution})
	if err != nil {
		return windowshost.ReplicaSyncResult{}, err
	}
	connections, err := newWindowsConnectionManagerWithProgress(reporter)
	if err != nil {
		return windowshost.ReplicaSyncResult{}, fmt.Errorf(
			"appliance lifecycle mutation succeeded, but managed connection reconciliation is unavailable: %w", err,
		)
	}
	result, err := windowshost.NewWindowsReplicaSynchronizer(&connections).Sync(ctx, expected)
	if err != nil {
		return windowshost.ReplicaSyncResult{}, fmt.Errorf(
			"appliance lifecycle mutation succeeded, but Windows connection resync failed: %w", err,
		)
	}
	return result, nil
}

func parseInfoFlags(command string, args []string, allowJSON bool) (string, bool, error) {
	flags := flag.NewFlagSet("loki "+command, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	distribution := flags.String("distribution", defaultDistribution(), "WSL distribution name")
	jsonOutput := false
	if allowJSON {
		flags.BoolVar(&jsonOutput, "json", false, "emit machine-readable JSON")
	}
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		if allowJSON {
			return "", false, fmt.Errorf("usage: loki %s [--distribution NAME] [--json]", command)
		}
		return "", false, fmt.Errorf("usage: loki %s [--distribution NAME]", command)
	}
	if err := windowshost.ValidateDistributionName(*distribution); err != nil {
		return "", false, err
	}
	return *distribution, jsonOutput, nil
}

func expectedInstallation(distribution string, base windowshost.InstallOptions) (windowshost.ExpectedInstallation, error) {
	if err := windowshost.ValidateDistributionName(distribution); err != nil {
		return windowshost.ExpectedInstallation{}, err
	}
	localAppData, systemRoot, err := windowsEnvironmentRoots()
	if err != nil {
		return windowshost.ExpectedInstallation{}, err
	}
	base.Distribution = distribution
	return windowshost.ExpectedFromOptions(base, localAppData, systemRoot)
}

func windowsEnvironmentRoots() (string, string, error) {
	localAppData := strings.TrimSpace(os.Getenv("LOCALAPPDATA"))
	systemRoot := strings.TrimSpace(os.Getenv("SystemRoot"))
	if localAppData == "" {
		return "", "", errors.New("LOCALAPPDATA is not available")
	}
	if systemRoot == "" {
		return "", "", errors.New("SystemRoot is not available")
	}
	return localAppData, systemRoot, nil
}

func defaultDistribution() string {
	value := strings.TrimSpace(os.Getenv("LOKI_WSL_NAME"))
	if value == "" {
		return defaultWindowsDistribution
	}
	return value
}

func confirmWindows(prompt string, stdout io.Writer) (bool, error) {
	if !term.IsTerminal(int(os.Stdin.Fd())) {
		return false, errors.New("non-interactive destructive operation requires an explicit approval flag")
	}
	fmt.Fprintf(stdout, "%s [y/N] ", prompt)
	line, err := bufio.NewReader(os.Stdin).ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return false, err
	}
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "y", "yes":
		return true, nil
	default:
		return false, nil
	}
}

func writeNativeProbe(probe windowshost.NativeProbe, stdout, stderr io.Writer) int {
	if probe.Stdout != "" {
		fmt.Fprintln(stdout, probe.Stdout)
	}
	if detail := progress.NonProgressText(probe.Stderr); detail != "" {
		fmt.Fprintln(stderr, detail)
	}
	if probe.ExitCode == 0 {
		return 0
	}
	if probe.ExitCode < 0 {
		return 1
	}
	return probe.ExitCode
}
