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
	"strings"

	"loki/internal/host/githubsetup"
	windowshost "loki/internal/host/windows"
	"loki/internal/progress"
)

const maxWindowsIntegrationFileBytes = 1 << 20

type windowsGitHubEnvelope struct {
	Config     []byte `json:"config"`
	PrivateKey []byte `json:"private_key"`
}

func runIntegration(ctx context.Context, args []string, stdout, stderr io.Writer) int {
	if integrationHelpRequested(args) {
		printIntegrationUsage(stdout, args[:len(args)-1]...)
		return 0
	}
	if len(args) == 0 {
		printIntegrationUsage(stderr)
		return 2
	}
	action := args[0]
	switch action {
	case "login", "logout", "user-status":
		return runWindowsGitHubUser(ctx, action, args[1:], stdout, stderr)
	case "list", "status", "doctor", "enable", "disable", "remove":
		return runIntegrationAction(ctx, action, args[1:], stdout, stderr)
	case "setup", "rotate", "import":
		return runIntegrationSetup(ctx, action, args[1:], stdout, stderr)
	default:
		printIntegrationUsage(stderr)
		return 2
	}
}

func runIntegrationAction(ctx context.Context, action string, args []string, stdout, stderr io.Writer) int {
	options, err := parseIntegrationAction(action, args, defaultDistribution())
	if errors.Is(err, flag.ErrHelp) {
		printIntegrationUsage(stdout, action)
		return 0
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		printIntegrationUsage(stderr, action)
		return 2
	}
	request := windowshost.OperatorRequest{
		Command: "integration", Action: action, Integration: options.Name,
		InterruptActiveJobs: options.InterruptJobs,
	}
	client := windowshost.NewWindowsOperatorClient()
	var result windowshost.OperatorResult
	if action == "list" || action == "status" {
		result, err = client.Execute(ctx, options.Distribution, request)
	} else {
		result, err = executeWindowsIntegrationWithProgress(ctx, client, options.Distribution, request, nil, stderr)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if action == "list" || action == "status" || action == "doctor" {
		return writeWindowsIntegrationInspection(action, result.Probe, options.JSON, stdout, stderr)
	}
	if result.Probe.ExitCode != 0 {
		return writeNativeProbe(result.Probe, stdout, stderr)
	}
	fmt.Fprintf(stdout, "Loki %s integration %s completed.\n", options.Name, action)
	return 0
}

func runIntegrationSetup(ctx context.Context, action string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		fmt.Fprintf(stderr, "integration %s requires a NAME: signing or github\n", action)
		printIntegrationUsage(stderr, action)
		return 2
	}
	// Accept the integration name either first or last. This keeps the public
	// Windows syntax natural while the appliance's flag parser receives it last.
	name := strings.ToLower(strings.TrimSpace(args[0]))
	rest := args[1:]
	if name != "signing" && name != "github" {
		name = strings.ToLower(strings.TrimSpace(args[len(args)-1]))
		rest = args[:len(args)-1]
	}
	switch name {
	case "signing":
		if action == "import" {
			fmt.Fprintln(stderr, "integration import supports github only")
			return 2
		}
		return runWindowsSigningSetup(ctx, action, rest, stdout, stderr)
	case "github":
		return runWindowsGitHubSetup(ctx, action, rest, stdout, stderr)
	default:
		fmt.Fprintln(stderr, "integration name must be signing or github")
		printIntegrationUsage(stderr, action)
		return 2
	}
}

func runWindowsSigningSetup(ctx context.Context, action string, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("integration "+action+" signing", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	distribution := flags.String("distribution", defaultDistribution(), "WSL distribution name")
	interrupt := flags.Bool("interrupt-active-jobs", false, "explicitly approve interrupting active jobs")
	identityName := flags.String("identity-name", "", "Git user.name")
	identityEmail := flags.String("identity-email", "", "Git user.email")
	keyFile := flags.String("key-file", "", "existing private Ed25519 OpenSSH key; omit to generate in appliance")
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		printIntegrationUsage(stdout, action, "signing")
		return 0
	} else if err != nil || flags.NArg() != 0 {
		if err != nil {
			fmt.Fprintln(stderr, err)
		}
		printIntegrationUsage(stderr, action, "signing")
		return 2
	}
	if err := windowshost.ValidateDistributionName(*distribution); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	reader := bufio.NewReader(os.Stdin)
	if strings.TrimSpace(*identityName) == "" || strings.TrimSpace(*identityEmail) == "" {
		fmt.Fprintln(stdout, "Enter the name and email you want displayed on Git commits created by Loki.")
	}
	if strings.TrimSpace(*identityName) == "" {
		value, err := promptWindowsValue(reader, stdout, "Name or nickname")
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		*identityName = value
	}
	if strings.TrimSpace(*identityEmail) == "" {
		value, err := promptWindowsValue(reader, stdout, "Email address")
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		*identityEmail = value
	}
	request := windowshost.OperatorRequest{
		Command: "integration", Action: action, Integration: "signing",
		IdentityName: *identityName, IdentityEmail: *identityEmail,
		InterruptActiveJobs: *interrupt,
	}
	client := windowshost.NewWindowsOperatorClient()
	var result windowshost.OperatorResult
	var err error
	if strings.TrimSpace(*keyFile) != "" {
		raw, readErr := readWindowsIntegrationFile(*keyFile, 64<<10)
		if readErr != nil {
			fmt.Fprintln(stderr, readErr)
			return 1
		}
		defer clear(raw)
		request.UseStdin = true
		result, err = executeWindowsIntegrationWithProgress(ctx, client, *distribution, request, raw, stderr)
	} else {
		result, err = executeWindowsIntegrationWithProgress(ctx, client, *distribution, request, nil, stderr)
	}
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if result.Probe.ExitCode != 0 {
		return writeNativeProbe(result.Probe, stdout, stderr)
	}
	fmt.Fprintln(stdout, result.Probe.Stdout)
	return 0
}

func runWindowsGitHubSetup(ctx context.Context, action string, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("integration "+action+" github", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	var noBrowser bool
	if action == "setup" {
		flags.BoolVar(&noBrowser, "no-browser", false, "print the local registration URL")
	}
	distribution := flags.String("distribution", defaultDistribution(), "WSL distribution name")
	interrupt := flags.Bool("interrupt-active-jobs", false, "explicitly approve interrupting active jobs")
	var configFile, privateKeyFile string
	if action != "setup" {
		flags.StringVar(&configFile, "config-file", "", "public GitHub App TOML configuration")
		flags.StringVar(&privateKeyFile, "private-key-file", "", "GitHub App RSA private key PEM")
	}
	if err := flags.Parse(args); errors.Is(err, flag.ErrHelp) {
		printIntegrationUsage(stdout, action, "github")
		return 0
	} else if err != nil || flags.NArg() != 0 {
		if err != nil {
			fmt.Fprintln(stderr, err)
		}
		printIntegrationUsage(stderr, action, "github")
		return 2
	}
	if err := windowshost.ValidateDistributionName(*distribution); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if action == "setup" {
		return runWindowsGitHubBrowserSetup(ctx, *distribution, *interrupt, githubsetup.Options{NoBrowser: noBrowser, Verbose: progress.Verbose(stderr)}, stdout, stderr)
	}
	if strings.TrimSpace(configFile) == "" || strings.TrimSpace(privateKeyFile) == "" {
		fmt.Fprintln(stderr, "--config-file and --private-key-file are required for GitHub import or rotation")
		return 2
	}
	configRaw, err := readWindowsIntegrationFile(configFile, maxWindowsIntegrationFileBytes)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	keyRaw, err := readWindowsIntegrationFile(privateKeyFile, maxWindowsIntegrationFileBytes)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer clear(keyRaw)
	envelope, err := json.Marshal(windowsGitHubEnvelope{Config: configRaw, PrivateKey: keyRaw})
	if err != nil {
		fmt.Fprintln(stderr, "cannot encode GitHub setup input")
		return 1
	}
	defer clear(envelope)
	request := windowshost.OperatorRequest{
		Command: "integration", Action: action, Integration: "github",
		UseStdin: true, InterruptActiveJobs: *interrupt,
	}
	result, err := executeWindowsIntegrationWithProgress(ctx, windowshost.NewWindowsOperatorClient(), *distribution, request, envelope, stderr)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if result.Probe.ExitCode != 0 {
		return writeNativeProbe(result.Probe, stdout, stderr)
	}
	fmt.Fprintln(stdout, result.Probe.Stdout)
	return 0
}

func promptWindowsValue(reader *bufio.Reader, stdout io.Writer, label string) (string, error) {
	fmt.Fprintf(stdout, "%s: ", label)
	value, err := reader.ReadString('\n')
	if err != nil && !errors.Is(err, io.EOF) {
		return "", err
	}
	value = strings.TrimSpace(value)
	if value == "" || strings.ContainsRune(value, 0) {
		return "", fmt.Errorf("%s is required", label)
	}
	return value, nil
}

func readWindowsIntegrationFile(path string, limit int) ([]byte, error) {
	path = strings.TrimSpace(path)
	if path == "" || limit <= 0 {
		return nil, errors.New("integration file path is required")
	}
	filesystem := windowshost.OSStateFilesystem{}
	state, err := filesystem.Lstat(path)
	if err != nil || !state.Exists || !state.Regular || state.Reparse {
		return nil, errors.New("integration file must be a regular non-reparse file")
	}
	file, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > int64(limit) {
		return nil, errors.New("integration file is unavailable, empty, or too large")
	}
	raw, err := io.ReadAll(io.LimitReader(file, int64(limit)+1))
	if err != nil || len(raw) == 0 || len(raw) > limit {
		clear(raw)
		return nil, errors.New("integration file is unavailable, empty, or too large")
	}
	after, err := filesystem.Lstat(path)
	if err != nil || !after.Exists || !after.Regular || after.Reparse {
		clear(raw)
		return nil, errors.New("integration file identity changed during read")
	}
	return raw, nil
}
