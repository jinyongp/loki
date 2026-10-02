package githubapp

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"loki/internal/fault"
	"loki/internal/process"
)

type RepositoryTokenSource interface {
	Token(context.Context, string) (string, error)
}

type CommandConfig struct {
	Binary         string
	CWD            string
	TempDir        string
	Environment    []string
	Identity       *process.Identity
	Timeout        time.Duration
	MaxInputBytes  int
	MaxOutputBytes int
}

type CommandRequest struct {
	Target string
	Args   []string
	Input  []byte
}

type CommandRunner struct {
	Config     CommandConfig
	Tokens     RepositoryTokenSource
	Supervisor process.Supervisor
	Projects   *ProjectAuthority
}

const (
	MaxCommandArguments          = 128
	MaxCommandArgumentBytes      = 8192
	MaxCommandArgumentTotalBytes = 65536
	MaxCommandInputBytes         = 16 << 20
)

type CommandCapabilities struct {
	CommandGroups       []string
	SearchSubcommands   []string
	ProhibitedFlags     []string
	MaxArguments        int
	MaxArgumentBytes    int
	MaxArgumentTotal    int
	MaxInputBytes       int
	RepositoryTokenOnly bool
	ProjectSubcommands  []string
}

var repositoryCommandGroups = map[string]bool{
	"api": true, "attestation": true, "cache": true, "issue": true, "label": true,
	"pr": true, "release": true, "repo": true, "ruleset": true, "run": true,
	"project": true,
	"search":  true, "secret": true, "status": true, "variable": true, "workflow": true,
}

var prohibitedCommandFlags = map[string]bool{
	"-R": true, "--repo": true, "--hostname": true, "--org": true,
	"--web": true, "--editor": true, "--browser": true,
}

var repositorySearchSubcommands = map[string]bool{
	"code": true, "commits": true, "issues": true, "prs": true,
}

func RepositoryCommandCapabilities() CommandCapabilities {
	groups := make([]string, 0, len(repositoryCommandGroups))
	for name := range repositoryCommandGroups {
		groups = append(groups, name)
	}
	sort.Strings(groups)
	search := make([]string, 0, len(repositorySearchSubcommands))
	for name := range repositorySearchSubcommands {
		search = append(search, name)
	}
	sort.Strings(search)
	flags := make([]string, 0, len(prohibitedCommandFlags))
	for name := range prohibitedCommandFlags {
		flags = append(flags, name)
	}
	sort.Strings(flags)
	return CommandCapabilities{
		CommandGroups: groups, SearchSubcommands: search, ProhibitedFlags: flags,
		MaxArguments: MaxCommandArguments, MaxArgumentBytes: MaxCommandArgumentBytes,
		MaxArgumentTotal: MaxCommandArgumentTotalBytes, MaxInputBytes: MaxCommandInputBytes,
		RepositoryTokenOnly: false, ProjectSubcommands: ProjectCommandCapabilities(),
	}
}

func (r *CommandRunner) Run(ctx context.Context, request CommandRequest) (process.Result, error) {
	if err := r.validate(request); err != nil {
		return process.Result{}, err
	}
	target := strings.ToLower(strings.TrimSpace(request.Target))
	arguments, err := scopedCommandArguments(target, request.Args)
	if err != nil {
		return process.Result{}, err
	}
	var token string
	if request.Args[0] == "project" {
		if r.Projects == nil {
			return process.Result{}, fault.Error("GitHub Projects authority is not configured")
		}
		projectContext, cancel := context.WithTimeout(ctx, r.Config.Timeout)
		defer cancel()
		prepared, prepareErr := r.Projects.Prepare(projectContext, request)
		if prepareErr != nil {
			return process.Result{}, prepareErr
		}
		arguments, token = prepared.arguments, prepared.token
		if prepared.result != nil {
			result := redactCommandResult(*prepared.result, token)
			if len(result.Output) > r.Config.MaxOutputBytes {
				result.Output = result.Output[:r.Config.MaxOutputBytes]
				result.Truncated = true
			}
			return result, nil
		}
		ctx = projectContext
	} else {
		token, err = r.Tokens.Token(ctx, target)
	}
	if err != nil || token == "" {
		if err != nil && request.Args[0] == "project" {
			return process.Result{}, err
		}
		return process.Result{}, errors.New("GitHub credential is unavailable")
	}
	configDir, err := r.configDirectory()
	if err != nil {
		return process.Result{}, err
	}
	defer os.RemoveAll(configDir)
	supervisor := r.Supervisor
	if supervisor == nil {
		supervisor = process.SystemdSupervisor{}
	}
	result, err := supervisor.Run(ctx, process.Spec{
		Argv:      append([]string{r.Config.Binary}, arguments...),
		CWD:       r.Config.CWD,
		Env:       commandEnvironment(r.Config.Environment, configDir, target, token),
		Identity:  r.Config.Identity,
		Input:     request.Input,
		Timeout:   r.Config.Timeout,
		MaxOutput: r.Config.MaxOutputBytes,
	})
	result = redactCommandResult(result, token)
	token = ""
	if err != nil {
		return result, errors.New("GitHub command could not be started")
	}
	return result, nil
}

// Check verifies the same CLI, directory and delegated identity as Run without
// issuing credentials or making a network request.
func (r *CommandRunner) Check(ctx context.Context) error {
	if r == nil || !filepath.IsAbs(r.Config.Binary) || !filepath.IsAbs(r.Config.CWD) || r.Config.Timeout <= 0 {
		return errors.New("GitHub command runner is not configured")
	}
	configDir, err := r.configDirectory()
	if err != nil {
		return err
	}
	defer os.RemoveAll(configDir)
	supervisor := r.Supervisor
	if supervisor == nil {
		supervisor = process.SystemdSupervisor{}
	}
	result, err := supervisor.Run(ctx, process.Spec{
		Argv: []string{r.Config.Binary, "config", "get", "git_protocol", "--host", "github.com"},
		CWD:  r.Config.CWD, Env: commandEnvironment(r.Config.Environment, configDir, "", ""),
		Identity: r.Config.Identity, Timeout: min(r.Config.Timeout, 5*time.Second), MaxOutput: 4096,
	})
	if err != nil || result.ExitCode != 0 || result.TimedOut || result.Canceled || result.Truncated {
		return errors.New("GitHub CLI environment is not ready")
	}
	return nil
}

func (r *CommandRunner) configDirectory() (string, error) {
	configDir, err := os.MkdirTemp(r.Config.TempDir, "loki-gh-")
	if err != nil {
		return "", errors.New("GitHub command environment is unavailable")
	}
	// The service-owned temporary root carries setgid so children inherit the
	// workspace group. Delegated commands can read configuration without CHOWN.
	if err = os.Chmod(configDir, 0750); err != nil {
		os.RemoveAll(configDir)
		return "", errors.New("GitHub command environment is unavailable")
	}
	return configDir, nil
}

func (r *CommandRunner) validate(request CommandRequest) error {
	if r == nil || r.Tokens == nil || !filepath.IsAbs(r.Config.Binary) || !filepath.IsAbs(r.Config.CWD) ||
		r.Config.Timeout <= 0 || r.Config.Timeout > 10*time.Minute ||
		r.Config.MaxInputBytes <= 0 || r.Config.MaxInputBytes > MaxCommandInputBytes ||
		r.Config.MaxOutputBytes <= 0 || r.Config.MaxOutputBytes > 16<<20 {
		return errors.New("GitHub command runner is not configured")
	}
	if !targetName(strings.ToLower(strings.TrimSpace(request.Target))) {
		return errors.New("GitHub repository target is invalid")
	}
	if len(request.Input) > r.Config.MaxInputBytes {
		return errors.New("GitHub command input is too large")
	}
	if len(request.Args) == 0 || len(request.Args) > MaxCommandArguments || !repositoryCommandGroups[request.Args[0]] {
		return errors.New("GitHub command is not allowed")
	}
	if request.Args[0] == "project" {
		_, err := parseProjectCommand(request)
		return err
	}
	total := 0
	for index, argument := range request.Args {
		total += len(argument)
		if argument == "" || strings.IndexByte(argument, 0) >= 0 || len(argument) > MaxCommandArgumentBytes || total > MaxCommandArgumentTotalBytes {
			return errors.New("GitHub command arguments are invalid")
		}
		if index == 0 {
			continue
		}
		if argument == "--" {
			break
		}
		name := argument
		if before, _, ok := strings.Cut(argument, "="); ok {
			name = before
		}
		if prohibitedCommandFlags[name] || strings.HasPrefix(argument, "-R") && argument != "-R" {
			return errors.New("GitHub command scope cannot be overridden")
		}
	}
	return nil
}

func scopedCommandArguments(target string, arguments []string) ([]string, error) {
	result := append([]string{}, arguments...)
	if len(result) == 0 || result[0] != "search" {
		return result, nil
	}
	if len(result) < 2 || !repositorySearchSubcommands[result[1]] {
		return nil, errors.New("GitHub search command is not repository-scoped")
	}
	return append(result, "--repo", target), nil
}

func commandEnvironment(base []string, configDir, target, token string) []string {
	allowed := map[string]bool{
		"PATH": true, "LANG": true, "LC_ALL": true,
		"HTTP_PROXY": true, "HTTPS_PROXY": true, "NO_PROXY": true,
		"http_proxy": true, "https_proxy": true, "no_proxy": true,
		"GIT_CONFIG_NOSYSTEM": true, "GIT_OPTIONAL_LOCKS": true,
		"GIT_CONFIG_GLOBAL": true, "SSH_AUTH_SOCK": true,
	}
	values := map[string]string{
		"PATH": "/usr/local/bin:/usr/bin:/bin", "LANG": "C.UTF-8", "LC_ALL": "C.UTF-8",
		"HOME": configDir, "GH_CONFIG_DIR": configDir, "GH_HOST": "github.com",
		"GH_TOKEN": token, "GH_REPO": strings.ToLower(strings.TrimSpace(target)),
		"GH_PROMPT_DISABLED": "1", "GH_NO_UPDATE_NOTIFIER": "1", "NO_COLOR": "1",
	}
	for _, entry := range base {
		name, value, ok := strings.Cut(entry, "=")
		if ok && allowed[name] {
			values[name] = value
		}
	}
	names := make([]string, 0, len(values))
	for name := range values {
		names = append(names, name)
	}
	sort.Strings(names)
	environment := make([]string, 0, len(names))
	for _, name := range names {
		environment = append(environment, name+"="+values[name])
	}
	return environment
}

func redactCommandResult(result process.Result, token string) process.Result {
	if token == "" {
		return result
	}
	replacement := []byte("[REDACTED]")
	result.Raw = bytes.ReplaceAll(result.Raw, []byte(token), replacement)
	result.Output = strings.ReplaceAll(result.Output, token, string(replacement))
	return result
}
