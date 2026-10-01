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
	"strconv"
	"strings"

	"loki/internal/host/githubsetup"
	windowshost "loki/internal/host/windows"
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
	case "list", "status", "doctor", "enable", "disable", "remove":
		return runIntegrationAction(ctx, action, args[1:], stdout, stderr)
	case "setup", "rotate":
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
	if strings.TrimSpace(*identityName) == "" {
		value, err := promptWindowsValue(reader, stdout, "Git signing name")
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		*identityName = value
	}
	if strings.TrimSpace(*identityEmail) == "" {
		value, err := promptWindowsValue(reader, stdout, "Git signing email")
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
	manual := flags.Bool("manual", false, "enter existing App credentials")
	noBrowser := flags.Bool("no-browser", false, "print the local registration URL")
	distribution := flags.String("distribution", defaultDistribution(), "WSL distribution name")
	interrupt := flags.Bool("interrupt-active-jobs", false, "explicitly approve interrupting active jobs")
	configFile := flags.String("config-file", "", "public GitHub App TOML configuration")
	privateKeyFile := flags.String("private-key-file", "", "GitHub App RSA private key PEM")
	appID := flags.Int64("app-id", 0, "GitHub App ID")
	account := flags.String("account", "", "GitHub account/organization")
	accountType := flags.String("account-type", "organization", "organization or user")
	installationID := flags.Int64("installation-id", 0, "GitHub App installation ID")
	repositories := flags.String("repositories", "", "comma-separated repository allowlist")
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
	automatic := action == "setup" && !*manual && *configFile == "" && *privateKeyFile == "" && *appID == 0 && *installationID == 0 && *repositories == ""
	if automatic {
		typeProvided := false
		flags.Visit(func(f *flag.Flag) {
			if f.Name == "account-type" {
				typeProvided = true
			}
		})
		if !typeProvided {
			*accountType = ""
		}
		return runWindowsGitHubBrowserSetup(ctx, *distribution, *interrupt, githubsetup.Options{Account: *account, AccountType: *accountType, NoBrowser: *noBrowser}, stdout, stderr)
	}
	if *noBrowser {
		fmt.Fprintln(stderr, "--no-browser applies to automatic setup only")
		return 2
	}
	reader := bufio.NewReader(os.Stdin)
	var configRaw []byte
	if strings.TrimSpace(*configFile) != "" {
		raw, err := readWindowsIntegrationFile(*configFile, maxWindowsIntegrationFileBytes)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		configRaw = raw
	} else {
		if *appID <= 0 {
			value, err := promptWindowsValue(reader, stdout, "GitHub App ID")
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			parsed, parseErr := strconv.ParseInt(value, 10, 64)
			if parseErr != nil || parsed <= 0 {
				fmt.Fprintln(stderr, "GitHub App ID must be a positive integer")
				return 2
			}
			*appID = parsed
		}
		if strings.TrimSpace(*account) == "" {
			value, err := promptWindowsValue(reader, stdout, "GitHub account")
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			*account = value
		}
		if *installationID <= 0 {
			value, err := promptWindowsValue(reader, stdout, "GitHub installation ID")
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			parsed, parseErr := strconv.ParseInt(value, 10, 64)
			if parseErr != nil || parsed <= 0 {
				fmt.Fprintln(stderr, "GitHub installation ID must be a positive integer")
				return 2
			}
			*installationID = parsed
		}
		if strings.TrimSpace(*repositories) == "" {
			value, err := promptWindowsValue(reader, stdout, "Repositories (comma-separated)")
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			*repositories = value
		}
		var err error
		configRaw, err = buildWindowsGitHubConfig(*appID, *account, *accountType, *installationID, *repositories)
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 2
		}
	}
	if strings.TrimSpace(*privateKeyFile) == "" {
		value, err := promptWindowsValue(reader, stdout, "GitHub App private key file")
		if err != nil {
			fmt.Fprintln(stderr, err)
			return 1
		}
		*privateKeyFile = value
	}
	keyRaw, err := readWindowsIntegrationFile(*privateKeyFile, maxWindowsIntegrationFileBytes)
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

func buildWindowsGitHubConfig(appID int64, account, accountType string, installationID int64, repositories string) ([]byte, error) {
	account = strings.ToLower(strings.TrimSpace(account))
	accountType = strings.ToLower(strings.TrimSpace(accountType))
	if appID <= 0 || installationID <= 0 || !safeGitHubName(account, 39) ||
		(accountType != "organization" && accountType != "user") {
		return nil, errors.New("GitHub App installation settings are invalid")
	}
	items := []string{}
	for _, repository := range strings.Split(repositories, ",") {
		repository = strings.ToLower(strings.TrimSpace(repository))
		if !safeGitHubName(repository, 100) || strings.Contains(repository, "..") {
			return nil, fmt.Errorf("invalid GitHub repository %q", repository)
		}
		if !slicesContains(items, repository) {
			items = append(items, repository)
		}
	}
	if len(items) == 0 {
		return nil, errors.New("at least one GitHub repository is required")
	}
	quoted := make([]string, len(items))
	for i, repository := range items {
		quoted[i] = strconv.Quote(repository)
	}
	raw := fmt.Sprintf(
		"github_app_id = %d\ngithub_api_version = \"2026-03-10\"\n\n[[github_installations]]\naccount = %s\naccount_type = %s\ninstallation_id = %d\nrepositories = [%s]\n",
		appID, strconv.Quote(account), strconv.Quote(accountType), installationID, strings.Join(quoted, ", "),
	)
	return []byte(raw), nil
}

func safeGitHubName(value string, max int) bool {
	if value == "" || len(value) > max || strings.HasPrefix(value, "-") || strings.HasSuffix(value, "-") {
		return false
	}
	for _, r := range value {
		if r >= 'a' && r <= 'z' || r >= '0' && r <= '9' || r >= 'A' && r <= 'Z' ||
			r == '-' || r == '_' || r == '.' {
			continue
		}
		return false
	}
	return true
}

func slicesContains(values []string, value string) bool {
	for _, candidate := range values {
		if candidate == value {
			return true
		}
	}
	return false
}
