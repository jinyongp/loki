package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"time"

	"golang.org/x/sys/unix"

	"loki/internal/config"
	"loki/internal/host/lifecycle"
	githubapp "loki/internal/integrations/github"
)

const maxGitHubConfigImportBytes = 1 << 20

type managedGitHubCandidate struct {
	ConfigRaw []byte
	KeyRaw    []byte
	Config    config.Config
}

func runHostGitHubSetup(action string, args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("host integration "+action+" github", flag.ContinueOnError)
	flags.SetOutput(stderr)
	system := flags.Bool("system", false, "operate on the system-wide host installation")
	stateRoot := flags.String("state-root", "", "host lifecycle state root")
	launcherLayout := flags.String("launcher-layout", "", "launcher service layout")
	jsonOutput := flags.Bool("json", false, "emit machine-readable JSON")
	interrupt := flags.Bool("interrupt-active-jobs", false, "explicitly approve interrupting active jobs")
	configFile := flags.String("config-file", "", "public GitHub App TOML configuration")
	privateKeyFile := flags.String("private-key-file", "", "GitHub App RSA private key PEM")
	if err := flags.Parse(args); err != nil || flags.NArg() != 1 || flags.Arg(0) != "github" {
		fmt.Fprintf(stderr, "usage: loki host integration %s [OPTIONS] github\n", action)
		return 2
	}
	if strings.TrimSpace(*configFile) == "" || strings.TrimSpace(*privateKeyFile) == "" {
		fmt.Fprintln(stderr, "--config-file and --private-key-file are required")
		return 2
	}
	options, err := resolveHostIntegrationOptions(hostIntegrationOptions{
		System: *system, StateRoot: strings.TrimSpace(*stateRoot),
		LauncherLayout: strings.TrimSpace(*launcherLayout), JSON: *jsonOutput, InterruptJobs: *interrupt,
	})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if options.System && os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "system integration setup requires root")
		return 1
	}
	store, err := lifecycle.OpenFileStore(options.StateRoot)
	if err != nil {
		fmt.Fprintln(stderr, "Loki is not installed for this scope.")
		return 1
	}
	current, err := store.ReadManagedIntegrations(context.Background())
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	switch action {
	case "setup":
		if current.GitHub.Configured {
			fmt.Fprintln(stderr, "GitHub integration is already configured; use rotate")
			return 1
		}
	case "rotate":
		if !current.GitHub.Configured {
			fmt.Fprintln(stderr, "GitHub integration is not configured; run setup first")
			return 1
		}
	default:
		fmt.Fprintln(stderr, "GitHub integration action is invalid")
		return 2
	}

	candidate, err := loadManagedGitHubCandidate(context.Background(), strings.TrimSpace(*configFile), strings.TrimSpace(*privateKeyFile))
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	defer clear(candidate.KeyRaw)
	if err = validateManagedGitHubCandidate(context.Background(), candidate, nil); err != nil {
		fmt.Fprintln(stderr, "GitHub App validation failed:", err)
		return 1
	}

	backend, err := newHostComposeBackend(store)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	engine := &lifecycle.TransactionEngine{Store: store, Backend: backend, Now: lifecycleTimeNow}
	manager := lifecycle.Manager{Store: store, Jobs: backend, Maintainer: engine, Now: lifecycleTimeNow}
	enabled := true
	if action == "rotate" {
		enabled = current.GitHub.Enabled
	}
	err = manager.UpdateManagedIntegration(context.Background(), "github", func(ctx context.Context, store *lifecycle.FileStore) error {
		configDigest, writeErr := store.WriteManagedIntegrationFile(ctx, lifecycle.ManagedGitHubConfigFile, candidate.ConfigRaw)
		if writeErr != nil {
			return writeErr
		}
		credentialDigest, writeErr := store.WriteManagedIntegrationFile(ctx, lifecycle.ManagedGitHubCredentialFile, candidate.KeyRaw)
		if writeErr != nil {
			return writeErr
		}
		state, readErr := store.ReadManagedIntegrations(ctx)
		if readErr != nil {
			return readErr
		}
		state.GitHub = lifecycle.ManagedIntegrationToggle{
			Configured: true, Enabled: enabled,
			CredentialSHA256: credentialDigest, ConfigSHA256: configDigest,
		}
		return store.CommitManagedIntegrations(ctx, state, action+"-github", lifecycleTimeNow())
	}, lifecycle.MutationOptions{InterruptActiveJobs: options.InterruptJobs})
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	if *jsonOutput {
		fmt.Fprintf(stdout, "{\"configured\":true,\"enabled\":%t,\"app_id\":%d,\"target_count\":%d}\n",
			enabled, candidate.Config.GitHubAppID, len(candidate.Config.GitHubTargets))
	} else {
		fmt.Fprintln(stdout, "GitHub App integration is configured.")
		fmt.Fprintf(stdout, "  App ID: %d\n", candidate.Config.GitHubAppID)
		fmt.Fprintf(stdout, "  Repositories: %d\n", len(candidate.Config.GitHubTargets))
		fmt.Fprintf(stdout, "  Enabled: %t\n", enabled)
	}
	return 0
}

func loadManagedGitHubCandidate(ctx context.Context, configPath, keyPath string) (managedGitHubCandidate, error) {
	configRaw, err := readGitHubImportFile(ctx, configPath, maxGitHubConfigImportBytes, false)
	if err != nil {
		return managedGitHubCandidate{}, fmt.Errorf("read GitHub config: %w", err)
	}
	parsed, err := config.ParseGitHubFragment(configRaw)
	if err != nil || parsed.GitHubAppID <= 0 || len(parsed.GitHubTargets) == 0 {
		return managedGitHubCandidate{}, errors.New("GitHub App configuration is invalid")
	}
	keyRaw, err := readGitHubImportFile(ctx, keyPath, githubapp.MaxPrivateKeyBytes, true)
	if err != nil {
		return managedGitHubCandidate{}, fmt.Errorf("read GitHub App private key: %w", err)
	}
	if err = githubapp.ValidatePrivateKey(string(keyRaw)); err != nil {
		clear(keyRaw)
		return managedGitHubCandidate{}, errors.New("GitHub App private key is invalid")
	}
	return managedGitHubCandidate{ConfigRaw: configRaw, KeyRaw: keyRaw, Config: parsed}, nil
}

func readGitHubImportFile(ctx context.Context, path string, maxBytes int, private bool) ([]byte, error) {
	path = filepath.Clean(strings.TrimSpace(path))
	if !filepath.IsAbs(path) || path == string(filepath.Separator) || strings.ContainsRune(path, 0) {
		return nil, errors.New("import path must be a clean absolute non-root path")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "github-integration-import")
	defer file.Close()
	info, err := file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() <= 0 || info.Size() > int64(maxBytes) {
		return nil, errors.New("import file is invalid")
	}
	var stat unix.Stat_t
	if err = unix.Fstat(fd, &stat); err != nil {
		return nil, err
	}
	if private && (info.Mode().Perm() != 0600 || int(stat.Uid) != os.Geteuid()) {
		return nil, errors.New("private-key import must be a service-owned 0600 regular file")
	}
	raw, err := io.ReadAll(io.LimitReader(file, int64(maxBytes)+1))
	if err != nil || len(raw) == 0 || len(raw) > maxBytes {
		clear(raw)
		return nil, errors.New("import file exceeds the supported size")
	}
	return raw, nil
}

func managedGitHubTargets(c config.Config) map[string]githubapp.Target {
	result := make(map[string]githubapp.Target, len(c.GitHubTargets))
	for _, installation := range c.GitHubInstallations {
		for _, repository := range installation.Repositories {
			target := installation.Account + "/" + repository
			result[target] = githubapp.Target{
				InstallationID: installation.InstallationID,
				Repository:     repository,
			}
		}
	}
	return result
}

func validateManagedGitHubCandidate(ctx context.Context, candidate managedGitHubCandidate, client *http.Client) error {
	if candidate.Config.GitHubAppID <= 0 || len(candidate.Config.GitHubTargets) == 0 {
		return errors.New("GitHub App configuration has no repository targets")
	}
	if err := githubapp.ValidatePrivateKey(string(candidate.KeyRaw)); err != nil {
		return errors.New("GitHub App private key is invalid")
	}
	if client == nil {
		client = &http.Client{Timeout: 30 * time.Second}
	}
	validationCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	broker := &githubapp.Broker{
		Config: githubapp.BrokerConfig{
			AppID: candidate.Config.GitHubAppID, APIVersion: candidate.Config.GitHubAPIVersion,
			MaxResponseBytes: candidate.Config.GitHubMaxResponseBytes,
			Targets:          managedGitHubTargets(candidate.Config),
		},
		Client: client,
		PrivateKey: func(context.Context) (string, error) {
			return string(candidate.KeyRaw), nil
		},
	}
	provider := &githubapp.Provider{
		Config: githubapp.ProviderConfig{
			APIVersion:       candidate.Config.GitHubAPIVersion,
			Targets:          append([]string(nil), candidate.Config.GitHubTargets...),
			MaxResponseBytes: candidate.Config.GitHubMaxResponseBytes,
			MaxPages:         candidate.Config.GitHubMaxPages,
		},
		HTTP: client, Tokens: broker,
	}
	target := candidate.Config.GitHubTargets[0]
	result, err := provider.Read(validationCtx, githubapp.ProviderReadRequest{
		Target: target, Action: githubapp.ProviderReadRepository,
	})
	if err != nil {
		return err
	}
	if result.Target != target || result.Repository == nil {
		return errors.New("GitHub repository validation returned an invalid result")
	}
	return nil
}

func toggleManagedGitHub(
	ctx context.Context,
	manager lifecycle.Manager,
	store *lifecycle.FileStore,
	enabled bool,
	options lifecycle.MutationOptions,
) error {
	state, err := store.ReadManagedIntegrations(ctx)
	if err != nil {
		return err
	}
	if !state.GitHub.Configured {
		return errors.New("GitHub integration is not configured")
	}
	if state.GitHub.Enabled == enabled {
		return nil
	}
	if enabled {
		candidate, loadErr := loadManagedGitHubFromStore(ctx, store)
		if loadErr != nil {
			return loadErr
		}
		defer clear(candidate.KeyRaw)
		if loadErr = validateManagedGitHubCandidate(ctx, candidate, nil); loadErr != nil {
			return fmt.Errorf("GitHub App validation failed: %w", loadErr)
		}
	}
	return manager.UpdateManagedIntegration(ctx, "github", func(ctx context.Context, store *lifecycle.FileStore) error {
		current, readErr := store.ReadManagedIntegrations(ctx)
		if readErr != nil {
			return readErr
		}
		current.GitHub.Enabled = enabled
		return store.CommitManagedIntegrations(ctx, current, "github-enabled", lifecycleTimeNow())
	}, options)
}

func removeManagedGitHub(
	ctx context.Context,
	manager lifecycle.Manager,
	store *lifecycle.FileStore,
	options lifecycle.MutationOptions,
) error {
	state, err := store.ReadManagedIntegrations(ctx)
	if err != nil {
		return err
	}
	if !state.GitHub.Configured {
		return nil
	}
	return manager.UpdateManagedIntegration(ctx, "github", func(ctx context.Context, store *lifecycle.FileStore) error {
		for _, path := range []string{lifecycle.ManagedGitHubConfigFile, lifecycle.ManagedGitHubCredentialFile} {
			if removeErr := store.RemoveManagedIntegrationFile(ctx, path); removeErr != nil {
				return removeErr
			}
		}
		current, readErr := store.ReadManagedIntegrations(ctx)
		if readErr != nil {
			return readErr
		}
		current.GitHub = lifecycle.ManagedIntegrationToggle{}
		return store.CommitManagedIntegrations(ctx, current, "remove-github", lifecycleTimeNow())
	}, options)
}

func loadManagedGitHubPublicConfig(ctx context.Context, store *lifecycle.FileStore) ([]byte, config.Config, error) {
	configRaw, err := store.ReadManagedIntegrationFile(ctx, lifecycle.ManagedGitHubConfigFile, true)
	if err != nil {
		return nil, config.Config{}, err
	}
	parsed, err := config.ParseGitHubFragment(configRaw)
	if err != nil || parsed.GitHubAppID <= 0 || len(parsed.GitHubTargets) == 0 {
		return nil, config.Config{}, errors.New("managed GitHub App configuration is invalid")
	}
	return configRaw, parsed, nil
}

func loadManagedGitHubFromStore(ctx context.Context, store *lifecycle.FileStore) (managedGitHubCandidate, error) {
	configRaw, parsed, err := loadManagedGitHubPublicConfig(ctx, store)
	if err != nil {
		return managedGitHubCandidate{}, err
	}
	keyRaw, err := store.ReadManagedIntegrationFile(ctx, lifecycle.ManagedGitHubCredentialFile, true)
	if err != nil {
		return managedGitHubCandidate{}, err
	}
	return managedGitHubCandidate{ConfigRaw: configRaw, KeyRaw: keyRaw, Config: parsed}, nil
}
