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

	windowshost "loki/internal/host/windows"
)

const maxWindowsIntegrationFileBytes = 1 << 20

type windowsIntegrationReport struct {
	SchemaVersion  int    `json:"schema_version"`
	Name           string `json:"name"`
	Configured     bool   `json:"configured"`
	Enabled        bool   `json:"enabled"`
	Ready          bool   `json:"ready"`
	State          string `json:"state"`
	PublicKey      string `json:"public_key,omitempty"`
	Fingerprint    string `json:"fingerprint,omitempty"`
	IdentityName   string `json:"identity_name,omitempty"`
	IdentityEmail  string `json:"identity_email,omitempty"`
	GitHubAppID    int64  `json:"github_app_id,omitempty"`
	TargetCount    int    `json:"target_count,omitempty"`
	Authentication string `json:"authentication,omitempty"`
	Detail         string `json:"detail,omitempty"`
}

type windowsGitHubEnvelope struct {
	Config     []byte `json:"config"`
	PrivateKey []byte `json:"private_key"`
}

func runIntegration(ctx context.Context, args []string, stdout, stderr io.Writer) int {
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

func printIntegrationUsage(output io.Writer) {
	fmt.Fprintln(output, "usage:")
	fmt.Fprintln(output, "  loki integration list [--distribution NAME] [--json]")
	fmt.Fprintln(output, "  loki integration status|doctor [--distribution NAME] [--json] NAME")
	fmt.Fprintln(output, "  loki integration enable|disable|remove [--distribution NAME] [--interrupt-active-jobs] NAME")
	fmt.Fprintln(output, "  loki integration setup|rotate signing [--identity-name NAME] [--identity-email EMAIL] [--key-file PATH]")
	fmt.Fprintln(output, "  loki integration setup|rotate github [--config-file PATH | --app-id ID --account OWNER --account-type TYPE --installation-id ID --repositories LIST] --private-key-file PATH")
}

func runIntegrationAction(ctx context.Context, action string, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("integration "+action, flag.ContinueOnError)
	flags.SetOutput(stderr)
	distribution := flags.String("distribution", defaultDistribution(), "WSL distribution name")
	jsonOutput := flags.Bool("json", false, "emit machine-readable JSON")
	interrupt := flags.Bool("interrupt-active-jobs", false, "explicitly approve interrupting active jobs")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if err := windowshost.ValidateDistributionName(*distribution); err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if (action == "list" && flags.NArg() != 0) || (action != "list" && flags.NArg() != 1) {
		printIntegrationUsage(stderr)
		return 2
	}
	if (action == "list" || action == "status" || action == "doctor") && *interrupt {
		fmt.Fprintln(stderr, "--interrupt-active-jobs is valid only for integration mutations")
		return 2
	}
	if action != "list" && action != "status" && action != "doctor" && *jsonOutput {
		fmt.Fprintln(stderr, "--json is valid only for list, status, or doctor")
		return 2
	}
	name := ""
	if action != "list" {
		name = strings.ToLower(strings.TrimSpace(flags.Arg(0)))
		if name != "browser" && name != "signing" && name != "github" {
			fmt.Fprintln(stderr, "integration name must be browser, signing, or github")
			return 2
		}
	}
	request := windowshost.OperatorRequest{
		Command: "integration", Action: action, Integration: name,
		InterruptActiveJobs: *interrupt,
	}
	result, err := windowshost.NewWindowsOperatorClient().Execute(ctx, *distribution, request)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if result.Probe.ExitCode != 0 {
		return writeNativeProbe(result.Probe, stdout, stderr)
	}
	if action == "list" {
		if *jsonOutput {
			fmt.Fprintln(stdout, result.Probe.Stdout)
			return 0
		}
		var envelope struct {
			Integrations []windowsIntegrationReport `json:"integrations"`
		}
		if err = json.Unmarshal([]byte(result.Probe.Stdout), &envelope); err != nil {
			fmt.Fprintln(stderr, "cannot decode Loki integration list")
			return 1
		}
		fmt.Fprintln(stdout, "Loki integrations")
		for _, item := range envelope.Integrations {
			fmt.Fprintf(stdout, "  %s: %s\n", item.Name, item.State)
		}
		return 0
	}
	if action == "status" || action == "doctor" {
		if *jsonOutput {
			fmt.Fprintln(stdout, result.Probe.Stdout)
			return 0
		}
		var report windowsIntegrationReport
		if err = json.Unmarshal([]byte(result.Probe.Stdout), &report); err != nil {
			fmt.Fprintln(stderr, "cannot decode Loki integration status")
			return 1
		}
		renderWindowsIntegration(report, stdout)
		return 0
	}
	fmt.Fprintf(stdout, "Loki %s integration %s completed.\n", name, action)
	return 0
}

func renderWindowsIntegration(report windowsIntegrationReport, stdout io.Writer) {
	fmt.Fprintf(stdout, "Loki integration: %s\n", report.Name)
	fmt.Fprintf(stdout, "  State: %s\n", report.State)
	fmt.Fprintf(stdout, "  Configured: %t\n", report.Configured)
	fmt.Fprintf(stdout, "  Enabled: %t\n", report.Enabled)
	fmt.Fprintf(stdout, "  Ready: %t\n", report.Ready)
	if report.PublicKey != "" {
		fmt.Fprintf(stdout, "  Public key: %s\n", report.PublicKey)
		fmt.Fprintf(stdout, "  Fingerprint: %s\n", report.Fingerprint)
		fmt.Fprintf(stdout, "  Identity: %s <%s>\n", report.IdentityName, report.IdentityEmail)
	}
	if report.GitHubAppID != 0 {
		fmt.Fprintf(stdout, "  App ID: %d\n", report.GitHubAppID)
		fmt.Fprintf(stdout, "  Repositories: %d\n", report.TargetCount)
	}
	if report.Authentication != "" {
		fmt.Fprintf(stdout, "  Authentication: %s\n", report.Authentication)
	}
	if report.Detail != "" {
		fmt.Fprintf(stdout, "  Detail: %s\n", report.Detail)
	}
}

func runIntegrationSetup(ctx context.Context, action string, args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printIntegrationUsage(stderr)
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
		printIntegrationUsage(stderr)
		return 2
	}
}

func runWindowsSigningSetup(ctx context.Context, action string, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("integration "+action+" signing", flag.ContinueOnError)
	flags.SetOutput(stderr)
	distribution := flags.String("distribution", defaultDistribution(), "WSL distribution name")
	interrupt := flags.Bool("interrupt-active-jobs", false, "explicitly approve interrupting active jobs")
	identityName := flags.String("identity-name", "", "Git user.name")
	identityEmail := flags.String("identity-email", "", "Git user.email")
	keyFile := flags.String("key-file", "", "existing private Ed25519 OpenSSH key; omit to generate in appliance")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
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
		result, err = client.ExecuteInput(ctx, *distribution, request, raw)
	} else {
		result, err = client.Execute(ctx, *distribution, request)
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
	flags.SetOutput(stderr)
	distribution := flags.String("distribution", defaultDistribution(), "WSL distribution name")
	interrupt := flags.Bool("interrupt-active-jobs", false, "explicitly approve interrupting active jobs")
	configFile := flags.String("config-file", "", "public GitHub App TOML configuration")
	privateKeyFile := flags.String("private-key-file", "", "GitHub App RSA private key PEM")
	appID := flags.Int64("app-id", 0, "GitHub App ID")
	account := flags.String("account", "", "GitHub account/organization")
	accountType := flags.String("account-type", "organization", "organization or user")
	installationID := flags.Int64("installation-id", 0, "GitHub App installation ID")
	repositories := flags.String("repositories", "", "comma-separated repository allowlist")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return 2
	}
	if err := windowshost.ValidateDistributionName(*distribution); err != nil {
		fmt.Fprintln(stderr, err)
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
	result, err := windowshost.NewWindowsOperatorClient().ExecuteInput(ctx, *distribution, request, envelope)
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
