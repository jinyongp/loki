package runtime

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"time"

	"loki/internal/agentcontext"
	"loki/internal/audit"
	"loki/internal/config"
	"loki/internal/control/identity"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/daemon"
	"loki/internal/devtools"
	"loki/internal/dockerproxy"
	"loki/internal/execution"
	"loki/internal/integrations/github"
	"loki/internal/portguard"
	"loki/internal/process"
	"loki/internal/rpc"
	"loki/internal/secret"
)

// RuntimeOptions is administrator-owned role configuration, never tool input.
type RuntimeOptions struct {
	Socket, StateDirectory, InboxDirectory, AuditPath string
	AgentUID                                          uint32
	SocketGID                                         int
	DevtoolsBinary                                    string
	Workspace, DockerSocket, SnapshotDirectory        string
	GitHubProxy, GitHubBinary, GitHubPrivateKeyFile   string
	GitHubTempDirectory                               string
	RunnerUID, RunnerGID                              uint32
	verifyDevtools                                    func(context.Context, *devtools.Client) (devtools.Candidate, error)
}

func RunRuntime(ctx context.Context, o RuntimeOptions, c config.Config, contract execution.Contract, generation controlpolicy.Generation, ready func() error, onAuditError func(error)) error {
	if !generation.Valid() {
		return errors.New("runtime requires a valid effective policy generation")
	}
	if err := contract.Validate(); err != nil {
		return err
	}
	for _, path := range []string{o.Socket, o.StateDirectory, o.InboxDirectory, o.AuditPath, o.DevtoolsBinary, o.Workspace, o.DockerSocket, o.SnapshotDirectory, o.GitHubTempDirectory} {
		if !filepath.IsAbs(path) {
			return errors.New("runtime role paths must be absolute")
		}
	}
	if o.RunnerUID != o.AgentUID {
		return errors.New("runtime runner identity does not match authorized agent")
	}
	workspace, err := filepath.EvalSymlinks(o.Workspace)
	if err != nil {
		return errors.New("runtime workspace cannot be resolved")
	}
	for _, path := range []string{o.StateDirectory, o.InboxDirectory, filepath.Dir(o.AuditPath)} {
		if err := daemon.PrivateDirectory(path); err != nil {
			return err
		}
	}
	if err := daemon.PrivateDirectory(o.GitHubTempDirectory); err != nil {
		return fmt.Errorf("validate GitHub temporary directory: %w", err)
	}
	ports, err := ProtectedPortPolicy(c.Port, contract)
	if err != nil {
		return err
	}
	runnerState := contract.Directories["runner-state"].Path
	runnerCache := contract.Directories["runner-cache"].Path
	runnerTemp := contract.Directories["runner-temp"].Path
	if o.SnapshotDirectory != contract.Directories["snapshots"].Path {
		return errors.New("snapshot directory does not match execution contract")
	}
	runnerDirectories := []string{
		runnerState,
		runnerCache,
		runnerTemp,
		o.SnapshotDirectory,
		contract.Environment["XDG_CONFIG_HOME"],
		contract.Environment["GH_CONFIG_DIR"],
		contract.Environment["XDG_DATA_HOME"],
		contract.Environment["XDG_STATE_HOME"],
		contract.Environment["NPM_CONFIG_CACHE"],
		contract.Environment["npm_config_store_dir"],
		contract.Environment["PLAYWRIGHT_BROWSERS_PATH"],
		contract.Environment["GOCACHE"],
		contract.Environment["GOMODCACHE"],
		contract.Environment["PIP_CACHE_DIR"],
	}
	for _, path := range runnerDirectories {
		if err := daemon.OwnedPrivateDirectory(path, o.RunnerUID, o.RunnerGID); err != nil {
			return fmt.Errorf("validate runner directory %q: %w", path, err)
		}
	}
	environment, err := contract.EnvironmentList()
	if err != nil {
		return err
	}
	controller := secret.Controller{StateDirectory: o.StateDirectory, InboxDirectory: o.InboxDirectory}
	devtoolsClient, err := devtools.NewClient(o.DevtoolsBinary, o.Workspace, environment)
	if err != nil {
		return err
	}
	defer devtoolsClient.Close()
	groups := []uint32{o.RunnerGID}
	if workspaceGroup := uint32(o.SocketGID); workspaceGroup != o.RunnerGID {
		groups = append(groups, workspaceGroup)
	}
	devtoolsClient.Identity = &process.Identity{UID: o.RunnerUID, GID: o.RunnerGID, Groups: groups}
	var devtoolsCandidate devtools.Candidate
	if o.verifyDevtools != nil {
		devtoolsCandidate, err = o.verifyDevtools(ctx, devtoolsClient)
	} else {
		devtoolsCandidate, err = devtoolsClient.Verify(ctx)
	}
	if err != nil {
		return fmt.Errorf("verify devtools candidate: %w", err)
	}
	contextJournal, err := agentcontext.NewContextJournal(filepath.Join(o.StateDirectory, "context"), agentcontext.ContextJournalLimits{})
	if err != nil {
		return fmt.Errorf("initialize context journal: %w", err)
	}
	devtoolsBroker := devtools.Broker{
		Client: devtoolsClient,
		ResolveSecrets: func(ctx context.Context, profile string, names []string) ([]string, []string, error) {
			plan, resolveErr := controller.ResolveEnvironment(ctx, profile, names)
			if resolveErr != nil {
				return nil, nil, resolveErr
			}
			return plan.Entries(), plan.RedactionValues(), nil
		},
	}
	var issueFields IssueFieldsClient
	var githubProvider GitHubProvider
	var githubCommands GitHubCommandRunner
	if c.GitHubAppID != 0 {
		if !filepath.IsAbs(o.GitHubBinary) || o.GitHubPrivateKeyFile != "" && !filepath.IsAbs(o.GitHubPrivateKeyFile) {
			return errors.New("GitHub runtime paths must be absolute")
		}
		proxyURL, parseErr := url.Parse(o.GitHubProxy)
		if parseErr != nil || proxyURL.Scheme != "http" || proxyURL.Host == "" || proxyURL.User != nil || proxyURL.Path != "" {
			return errors.New("GitHub egress proxy is invalid")
		}
		httpClient := &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
		targets := make(map[string]githubapp.Target, len(c.GitHubTargets))
		issueFieldTargets := make([]string, 0, len(c.GitHubTargets))
		for _, installation := range c.GitHubInstallations {
			for _, repository := range installation.Repositories {
				target := installation.Account + "/" + repository
				targets[target] = githubapp.Target{InstallationID: installation.InstallationID, Repository: repository}
				if installation.AccountType == "organization" {
					issueFieldTargets = append(issueFieldTargets, target)
				}
			}
		}
		privateKey := func(ctx context.Context) (string, error) {
			if o.GitHubPrivateKeyFile != "" {
				return githubapp.LoadPrivateKeyFile(ctx, o.GitHubPrivateKeyFile)
			}
			return controller.ManagedCredentials().Get(ctx, secret.ManagedGitHubAppPrivateKey)
		}
		broker := &githubapp.Broker{
			Config: githubapp.BrokerConfig{AppID: c.GitHubAppID, APIVersion: c.GitHubAPIVersion, MaxResponseBytes: c.GitHubMaxResponseBytes, Targets: targets},
			Client: httpClient, PrivateKey: privateKey,
		}
		issueFields = &githubapp.Client{
			Config: githubapp.ClientConfig{APIVersion: c.GitHubAPIVersion, Targets: issueFieldTargets, MaxResponseBytes: c.GitHubMaxResponseBytes, MaxPages: c.GitHubMaxPages},
			HTTP:   httpClient, Tokens: broker,
		}
		githubProvider = &githubapp.Provider{
			Config: githubapp.ProviderConfig{
				APIVersion: c.GitHubAPIVersion, Targets: append([]string(nil), c.GitHubTargets...),
				MaxResponseBytes: c.GitHubMaxResponseBytes, MaxPages: c.GitHubMaxPages,
			},
			HTTP: httpClient, Tokens: broker,
		}
		githubEnvironment := append([]string{}, environment...)
		githubEnvironment = append(githubEnvironment,
			"HTTPS_PROXY="+o.GitHubProxy, "HTTP_PROXY="+o.GitHubProxy,
			"https_proxy="+o.GitHubProxy, "http_proxy="+o.GitHubProxy,
			"NO_PROXY=127.0.0.1,localhost", "no_proxy=127.0.0.1,localhost",
		)
		identity := &process.Identity{UID: o.RunnerUID, GID: o.RunnerGID, Groups: groups}
		githubCommands = &githubapp.CommandRunner{
			Config: githubapp.CommandConfig{
				Binary: o.GitHubBinary, CWD: workspace, Environment: githubEnvironment, Identity: identity,
				TempDir:       o.GitHubTempDirectory,
				Timeout:       time.Duration(c.GitHubCommandTimeoutSeconds) * time.Second,
				MaxInputBytes: c.GitHubMaxInputBytes, MaxOutputBytes: c.GitHubMaxOutputBytes,
			},
			Tokens: broker,
		}
	}
	log := &audit.Log{Path: o.AuditPath}
	ops := SecretOperations(controller)
	ops["status"] = rpc.Operation{Grant: controlpolicy.Agent, Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
		profiles, err := controller.Profiles(ctx)
		if err != nil {
			return nil, err
		}
		items, ok := profiles["profiles"].([]map[string]any)
		if !ok {
			return nil, errors.New("invalid vault profile response")
		}
		credentialSource := "disabled"
		credentialAvailable := false
		if c.GitHubAppID != 0 {
			credentialSource = "vault"
			if o.GitHubPrivateKeyFile != "" {
				credentialSource = "file"
				info, statErr := os.Stat(o.GitHubPrivateKeyFile)
				credentialAvailable = statErr == nil && info.Mode().IsRegular()
			} else {
				credentialAvailable, err = controller.ManagedCredentials().Configured(ctx, secret.ManagedGitHubAppPrivateKey)
				if err != nil {
					return nil, err
				}
			}
		}
		revision, ok := profiles["revision"].(uint64)
		if !ok {
			return nil, errors.New("invalid vault revision response")
		}
		return map[string]any{
			"initialized": true, "profiles": len(items), "revision": revision, "policy_generation": generation.Metadata(),
			"devtools": devtoolsCandidate,
			"github": map[string]any{
				"configured": c.GitHubAppID != 0, "installation_count": len(c.GitHubInstallations),
				"target_count": len(c.GitHubTargets), "credential_source": credentialSource,
				"credential_available": credentialAvailable,
			},
		}, nil
	}}
	for _, group := range []map[string]rpc.Operation{
		ContextJournalOperations(contextJournal),
		DevtoolsMetadataOperations(devtoolsClient),
		DevtoolsCoordinationOperations(devtoolsClient),
		DevtoolsCoordinationMutationOperations(devtoolsClient),
		DevtoolsOperations(devtoolsBroker),
		GitHubOperations(controller),
		GitHubIssueFieldsOperations(issueFields),
		GitHubProviderOperations(githubProvider),
		GitHubCommandOperations(githubCommands),
		AuditOperations(log),
		PortOperations(&portguard.Guard{Root: workspace, UID: o.AgentUID, Ports: ports}),
		DockerOperations(dockerproxy.Inspector{Workspace: o.Workspace, SnapshotRoot: o.SnapshotDirectory, Socket: "unix://" + o.DockerSocket, Ports: ports}),
	} {
		for name, op := range group {
			if _, exists := ops[name]; exists {
				return errors.New("duplicate runtime operation: " + name)
			}
			ops[name] = op
		}
	}
	listener, err := daemon.Listen(o.Socket, o.SocketGID)
	if err != nil {
		return err
	}
	defer listener.Close()
	if ready != nil {
		if err = ready(); err != nil {
			return err
		}
	}
	server := rpc.Server{Principals: identity.UnixResolver{AgentUID: o.AgentUID}, Operations: ops, Audit: AuditSink(log, onAuditError)}
	return server.Serve(ctx, listener)
}
