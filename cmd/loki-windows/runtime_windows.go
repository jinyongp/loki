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
	"path/filepath"
	"strings"

	"golang.org/x/term"

	windowshost "loki/internal/host/windows"
)

const defaultWindowsDistribution = "loki-mcp"

func runWindowsCommand(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	switch args[0] {
	case "bootstrap":
		return runBootstrap(args[1:], stdout, stderr)
	case "install":
		return runInstall(args[1:], stdout, stderr)
	case "status":
		return runStatus(args[1:], stdout, stderr)
	case "doctor":
		return runDoctor(args[1:], stdout, stderr)
	case "connection":
		return runConnection(args[1:], stdout, stderr)
	case "update":
		return runUpdate(args[1:], stdout, stderr)
	case "backup":
		return runMaintenance("backup", args[1:], stdout, stderr)
	case "rollback":
		return runMaintenance("rollback", args[1:], stdout, stderr)
	case "restore":
		return runRestore(args[1:], stdout, stderr)
	case "uninstall":
		return runUninstall(args[1:], stdout, stderr)
	case "connect":
		fmt.Fprintln(stderr, "managed connection commands are not available in this release candidate")
		return 2
	default:
		fmt.Fprintf(stderr, "unknown Windows Loki command %q\n", args[0])
		printUsage(stderr)
		return 2
	}
}

func runBootstrap(args []string, stdout, stderr io.Writer) int {
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
	}).Install(context.Background(), source, paths, binding)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "Windows Loki frontend %s: %s\n", result.Ownership.ReleaseTag, result.Disposition)
	if result.PathChanged {
		fmt.Fprintf(stdout, "Persistent user PATH now includes %s\n", paths.BinDir)
	}
	return runCanonicalInstall(paths.Binary, args[1:], stdout, stderr)
}

func runCanonicalInstall(binary string, args []string, stdout, stderr io.Writer) int {
	command := exec.CommandContext(context.Background(), binary, append([]string{"install"}, args...)...)
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

func runInstall(args []string, stdout, stderr io.Writer) int {
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
	result, err := windowshost.NewWindowsInstallController(binding).Run(
		context.Background(), expected, options, approve,
	)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	switch result.Disposition {
	case windowshost.InstallNoop:
		fmt.Fprintf(stdout, "Loki appliance %q is already healthy.\n", options.Distribution)
	case windowshost.InstallCompleted:
		fmt.Fprintf(stdout, "Installed Loki appliance %q.\n", options.Distribution)
	default:
		fmt.Fprintf(stdout, "Loki appliance %q: %s\n", options.Distribution, result.Disposition)
	}
	fmt.Fprintln(stdout, "Run 'loki status' or 'loki connection' for operator details.")
	return 0
}

func runStatus(args []string, stdout, stderr io.Writer) int {
	distribution, jsonOutput, err := parseInfoFlags("status", args, true)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	result, err := windowshost.NewWindowsOperatorClient().Execute(
		context.Background(), distribution, windowshost.OperatorRequest{Command: "status"},
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

func runDoctor(args []string, stdout, stderr io.Writer) int {
	distribution, _, err := parseInfoFlags("doctor", args, false)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	result, err := windowshost.NewWindowsOperatorClient().Execute(
		context.Background(), distribution, windowshost.OperatorRequest{Command: "doctor"},
	)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	return writeNativeProbe(result.Probe, stdout, stderr)
}

func runConnection(args []string, stdout, stderr io.Writer) int {
	distribution, jsonOutput, err := parseInfoFlags("connection", args, true)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	delegated, err := windowshost.NewWindowsOperatorClient().Execute(
		context.Background(), distribution, windowshost.OperatorRequest{Command: "connection"},
	)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if delegated.Probe.ExitCode != 0 {
		return writeNativeProbe(delegated.Probe, stdout, stderr)
	}
	expected, err := expectedInstallation(distribution, windowshost.InstallOptions{Distribution: distribution})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	syncer := windowshost.NewWindowsReplicaSynchronizer(nil)
	if _, err = syncer.Sync(context.Background(), expected); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if jsonOutput {
		raw, readErr := os.ReadFile(filepath.Join(expected.StateDir, "connection.json"))
		if readErr != nil {
			fmt.Fprintln(stderr, "read refreshed Windows connection state:", readErr)
			return 1
		}
		if _, err = stdout.Write(append(raw, '\n')); err != nil {
			fmt.Fprintln(stderr, "write connection output:", err)
			return 1
		}
		return 0
	}
	view, err := (windowshost.NewWindowsReplicaStore()).Read(expected)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintln(stdout, "Loki MCP connection")
	fmt.Fprintf(stdout, "  Distribution: %s\n", distribution)
	fmt.Fprintf(stdout, "  Local origin: %s\n", view.LocalOrigin)
	fmt.Fprintln(stdout, "  Authentication: bearer token file")
	fmt.Fprintf(stdout, "  Token file: %s\n", filepath.Join(expected.StateDir, "mcp-token"))
	fmt.Fprintln(stdout, "  This is a loopback local origin, not a public MCP URL.")
	return 0
}

func runUpdate(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "usage: loki update status|prepare|apply [--distribution NAME] [--approve] [--interrupt-active-jobs]")
		return 2
	}
	action := args[0]
	flags := flag.NewFlagSet("loki update "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	distribution := flags.String("distribution", defaultDistribution(), "WSL distribution name")
	approve := flags.Bool("approve", false, "approve update apply")
	interrupt := flags.Bool("interrupt-active-jobs", false, "approve interrupting active jobs")
	if err := flags.Parse(args[1:]); err != nil || flags.NArg() != 0 {
		return 2
	}
	if err := windowshost.ValidateDistributionName(*distribution); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if action != "apply" && (*approve || *interrupt) {
		fmt.Fprintln(stderr, "--approve and --interrupt-active-jobs are valid only for update apply")
		return 2
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
	result, err := windowshost.NewWindowsOperatorClient().Execute(context.Background(), *distribution, windowshost.OperatorRequest{
		Command: "update", Action: action, Approve: *approve, InterruptActiveJobs: *interrupt,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	code := writeNativeProbe(result.Probe, stdout, stderr)
	if code != 0 || action != "apply" {
		return code
	}
	return refreshAfterLifecycleMutation(*distribution, stdout, stderr)
}

func runMaintenance(command string, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("loki "+command, flag.ContinueOnError)
	flags.SetOutput(stderr)
	distribution := flags.String("distribution", defaultDistribution(), "WSL distribution name")
	interrupt := flags.Bool("interrupt-active-jobs", false, "approve interrupting active jobs")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 2
	}
	if err := windowshost.ValidateDistributionName(*distribution); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	result, err := windowshost.NewWindowsOperatorClient().Execute(context.Background(), *distribution, windowshost.OperatorRequest{
		Command: command, InterruptActiveJobs: *interrupt,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	code := writeNativeProbe(result.Probe, stdout, stderr)
	if code != 0 || command != "rollback" {
		return code
	}
	return refreshAfterLifecycleMutation(*distribution, stdout, stderr)
}

func runRestore(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("loki restore", flag.ContinueOnError)
	flags.SetOutput(stderr)
	distribution := flags.String("distribution", defaultDistribution(), "WSL distribution name")
	interrupt := flags.Bool("interrupt-active-jobs", false, "approve interrupting active jobs")
	if err := flags.Parse(args); err != nil || flags.NArg() != 1 {
		fmt.Fprintln(stderr, "usage: loki restore [--distribution NAME] [--interrupt-active-jobs] BACKUP_ID")
		return 2
	}
	if err := windowshost.ValidateDistributionName(*distribution); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	result, err := windowshost.NewWindowsOperatorClient().Execute(context.Background(), *distribution, windowshost.OperatorRequest{
		Command: "restore", BackupID: flags.Arg(0), InterruptActiveJobs: *interrupt,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	code := writeNativeProbe(result.Probe, stdout, stderr)
	if code != 0 {
		return code
	}
	return refreshAfterLifecycleMutation(*distribution, stdout, stderr)
}

func runUninstall(args []string, stdout, stderr io.Writer) int {
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
	if err = windowshost.NewWindowsUninstallController(nil).Run(context.Background(), expected, *approve); err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	fmt.Fprintf(stdout, "Uninstalled verified local Loki appliance %q.\n", *distribution)
	fmt.Fprintln(stdout, "The Windows Loki frontend and shared helper cache were left installed.")
	return 0
}

func refreshAfterLifecycleMutation(distribution string, stdout, stderr io.Writer) int {
	expected, err := expectedInstallation(distribution, windowshost.InstallOptions{Distribution: distribution})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	result, err := windowshost.NewWindowsReplicaSynchronizer(nil).Sync(context.Background(), expected)
	if err != nil {
		fmt.Fprintln(stderr, "appliance lifecycle mutation succeeded, but Windows connection resync failed:", err)
		return 1
	}
	if result.Changed {
		fmt.Fprintln(stdout, "Refreshed Windows MCP connection state from the live appliance.")
	}
	return 0
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
	if probe.Stderr != "" {
		fmt.Fprintln(stderr, probe.Stderr)
	}
	if probe.ExitCode == 0 {
		return 0
	}
	if probe.ExitCode < 0 {
		return 1
	}
	return probe.ExitCode
}
