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
	"loki/internal/dockerproxy"
	"loki/internal/execution"
	"loki/internal/portguard"
	"loki/internal/process"
	"loki/internal/rpc"
	"loki/modules/coordination"
	"loki/modules/github"
	"loki/modules/secrets"
)

// RuntimeOptions is administrator-owned role configuration, never tool input.
type RuntimeOptions struct {
	PersonalProjects                                  bool
	ToolGate                                          *config.ToolGate
	Tools                                             []string
	Socket, StateDirectory, InboxDirectory, AuditPath string
	SecretStateDirectory, GitHubStateDirectory        string
	CoordinationStateDirectory                        string
	AgentUID                                          uint32
	PortInspectorUID                                  *uint32
	SocketGID                                         int
	DevtoolsBinary                                    string
	Workspace, DockerSocket, SnapshotDirectory        string
	GitHubProxy, GitHubBinary, GitHubPrivateKeyFile   string
	GitHubTempDirectory                               string
	RunnerUID, RunnerGID                              uint32
	verifyDevtools                                    func(context.Context, *devtools.Client) (devtools.Candidate, error)
}

func RunRuntime(ctx context.Context, o RuntimeOptions, c config.Config, contract execution.Contract, generation controlpolicy.Generation, ready func() error, onAuditError func(error)) error {
	selected, err := runtimeSelection(o.Tools)
	if err != nil {
		return err
	}
	if o.PortInspectorUID != nil && (*o.PortInspectorUID == 0 || *o.PortInspectorUID == o.AgentUID || !selected["browser"]) {
		return errors.New("port inspector requires the browser resource and a distinct non-root UID")
	}
	if o.ToolGate != nil && o.ToolGate.Revision() == "unavailable" {
		return errors.New("runtime activation snapshot is unavailable")
	}
	if !generation.Valid() {
		return errors.New("runtime requires a valid effective policy generation")
	}
	if err := contract.Validate(); err != nil {
		return err
	}
	paths := []string{o.Socket, o.StateDirectory, o.AuditPath}
	needsRunner := selected["coordination"] || selected["execution"] || selected["sharing"] || selected["github"] && c.GitHubAppID != 0
	needsWorkspace := needsRunner || selected["browser"]
	if needsWorkspace {
		paths = append(paths, o.Workspace)
	}
	if needsRunner {
		paths = append(paths, o.SnapshotDirectory)
	}
	if selected["secrets"] {
		paths = append(paths, o.InboxDirectory)
		if o.SecretStateDirectory != "" {
			paths = append(paths, o.SecretStateDirectory)
		}
	}
	if selected["coordination"] {
		paths = append(paths, o.DevtoolsBinary)
		if o.CoordinationStateDirectory != "" {
			paths = append(paths, o.CoordinationStateDirectory)
		}
	}
	if selected["sharing"] || selected["execution"] {
		paths = append(paths, o.DockerSocket)
	}
	if selected["github"] && c.GitHubAppID != 0 {
		paths = append(paths, o.GitHubTempDirectory)
		if o.GitHubStateDirectory != "" {
			paths = append(paths, o.GitHubStateDirectory)
		}
	}
	for _, path := range paths {
		if !filepath.IsAbs(path) {
			return errors.New("runtime role paths must be absolute")
		}
	}
	if o.RunnerUID != o.AgentUID {
		return errors.New("runtime runner identity does not match authorized agent")
	}
	var workspace string
	if needsWorkspace {
		workspace, err = filepath.EvalSymlinks(o.Workspace)
		if err != nil {
			return errors.New("runtime workspace cannot be resolved")
		}
	}
	privateDirectories := []string{o.StateDirectory, filepath.Dir(o.AuditPath)}
	if selected["secrets"] {
		privateDirectories = append(privateDirectories, o.InboxDirectory)
	}
	for _, path := range privateDirectories {
		if err := daemon.PrivateDirectory(path); err != nil {
			return err
		}
	}
	if selected["github"] && c.GitHubAppID != 0 {
		if err := daemon.PrivateDirectory(o.GitHubTempDirectory); err != nil {
			return fmt.Errorf("validate GitHub temporary directory: %w", err)
		}
	}
	ports, err := ProtectedPortPolicy(c.Port, contract)
	if err != nil {
		return err
	}
	runnerState := contract.Directories["runner-state"].Path
	runnerCache := contract.Directories["runner-cache"].Path
	runnerTemp := contract.Directories["runner-temp"].Path
	if needsRunner && o.SnapshotDirectory != contract.Directories["snapshots"].Path {
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
		if !needsRunner {
			break
		}
		if err := daemon.OwnedPrivateDirectory(path, o.RunnerUID, o.RunnerGID); err != nil {
			return fmt.Errorf("validate runner directory %q: %w", path, err)
		}
	}
	environment, err := contract.EnvironmentList()
	if err != nil {
		return err
	}
	secretState, githubState, coordinationState := runtimeDataDirectories(o)
	controller := secret.Controller{StateDirectory: secretState, InboxDirectory: o.InboxDirectory}
	if selected["secrets"] {
		if _, err := controller.Initialize(ctx); err != nil {
			return fmt.Errorf("initialize selected application-secret owner: %w", err)
		}
	}
	githubCredentials := githubapp.Credentials{StateDirectory: githubState}
	var devtoolsClient *devtools.Client
	groups := []uint32{o.RunnerGID}
	if workspaceGroup := uint32(o.SocketGID); workspaceGroup != o.RunnerGID {
		groups = append(groups, workspaceGroup)
	}
	var devtoolsCandidate devtools.Candidate
	var contextJournal *agentcontext.ContextJournal
	if selected["coordination"] {
		devtoolsClient, err = devtools.NewClient(o.DevtoolsBinary, o.Workspace, environment)
		if err != nil {
			return err
		}
		defer devtoolsClient.Close()
		devtoolsClient.Identity = &process.Identity{UID: o.RunnerUID, GID: o.RunnerGID, Groups: groups}
		if o.verifyDevtools != nil {
			devtoolsCandidate, err = o.verifyDevtools(ctx, devtoolsClient)
		} else {
			devtoolsCandidate, err = devtoolsClient.Verify(ctx)
		}
		if err != nil {
			return fmt.Errorf("verify devtools candidate: %w", err)
		}
		contextJournal, err = agentcontext.NewContextJournal(coordinationState, agentcontext.ContextJournalLimits{})
		if err != nil {
			return fmt.Errorf("initialize context journal: %w", err)
		}
	}
	devtoolsBroker := devtools.Broker{
		Client: devtoolsClient,
		ResolveSecrets: func(ctx context.Context, profile string, names []string) ([]string, []string, error) {
			if !selected["secrets"] {
				return nil, nil, errors.New("application-secret delivery is unavailable; select secrets explicitly")
			}
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
	var githubUsers *githubapp.UserAuthorization
	var githubBroker *githubapp.Broker
	var githubSetupClient *http.Client
	if selected["github"] {
		proxyURL, parseErr := url.Parse(o.GitHubProxy)
		if parseErr != nil || proxyURL.Scheme != "http" || proxyURL.Host == "" || proxyURL.User != nil || proxyURL.Path != "" {
			return errors.New("GitHub egress proxy is invalid")
		}
		githubSetupClient = &http.Client{Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)}}
	}
	if selected["github"] && c.GitHubAppID != 0 {
		if !filepath.IsAbs(o.GitHubBinary) || o.GitHubPrivateKeyFile != "" && !filepath.IsAbs(o.GitHubPrivateKeyFile) {
			return errors.New("GitHub runtime paths must be absolute")
		}
		httpClient := githubSetupClient
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
			return githubCredentials.Get(ctx, githubapp.AppPrivateKey)
		}
		broker := &githubapp.Broker{
			Config: githubapp.BrokerConfig{AppID: c.GitHubAppID, APIVersion: c.GitHubAPIVersion, MaxResponseBytes: c.GitHubMaxResponseBytes, Targets: targets},
			Client: httpClient, PrivateKey: privateKey,
		}
		githubBroker = broker
		accountTypes := map[string]string{}
		personalAccounts := []string{}
		for _, installation := range c.GitHubInstallations {
			accountTypes[installation.Account] = installation.AccountType
			if installation.AccountType == "user" {
				personalAccounts = append(personalAccounts, installation.Account)
			}
		}
		if o.PersonalProjects {
			githubUsers = &githubapp.UserAuthorization{
				AppID: c.GitHubAppID, Accounts: personalAccounts, HTTP: httpClient, PrivateKey: privateKey,
				Load: func(ctx context.Context) (string, error) {
					configured, err := githubCredentials.Configured(ctx, githubapp.PersonalUserTokens)
					if err != nil || !configured {
						return "", err
					}
					return githubCredentials.Get(ctx, githubapp.PersonalUserTokens)
				},
				Save: func(ctx context.Context, value string) error {
					_, err := githubCredentials.Set(ctx, githubapp.PersonalUserTokens, value)
					return err
				},
			}
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
			Projects: &githubapp.ProjectAuthority{
				Targets: append([]string(nil), c.GitHubTargets...), AccountTypes: accountTypes,
				Repositories: broker, Users: githubUsers, HTTP: httpClient,
				PersonalProjects: o.PersonalProjects,
				AuthorizePersonalProjects: func(context.Context) error {
					if o.ToolGate == nil {
						return nil
					}
					choice, err := o.ToolGate.Selection("github")
					if err != nil {
						return err
					}
					for _, capability := range choice.Capabilities {
						if capability == "personal-projects" {
							return nil
						}
					}
					return errors.New("GitHub personal-projects capability is disabled")
				},
			},
		}
	}
	log := &audit.Log{Path: o.AuditPath}
	ops := make(map[string]rpc.Operation)
	ops["status"] = rpc.Operation{Grant: controlpolicy.Agent, Handle: func(ctx context.Context, _ json.RawMessage) (any, error) {
		currentSelected := selected
		if o.ToolGate != nil {
			currentSelected = map[string]bool{}
			for module, initialized := range selected {
				if initialized {
					_, err := o.ToolGate.Selection(module)
					currentSelected[module] = err == nil
				}
			}
		}
		count := 0
		var revision uint64
		if currentSelected["secrets"] {
			profiles, err := controller.Profiles(ctx)
			if err != nil {
				return nil, err
			}
			items, ok := profiles["profiles"].([]map[string]any)
			if !ok {
				return nil, errors.New("invalid vault profile response")
			}
			count = len(items)
			revision, ok = profiles["revision"].(uint64)
			if !ok {
				return nil, errors.New("invalid vault revision response")
			}
		}
		credentialSource := "disabled"
		credentialAvailable := false
		if currentSelected["github"] && c.GitHubAppID != 0 {
			credentialSource = "provider-store"
			if o.GitHubPrivateKeyFile != "" {
				credentialSource = "file"
				info, statErr := os.Stat(o.GitHubPrivateKeyFile)
				credentialAvailable = statErr == nil && info.Mode().IsRegular()
			} else {
				var err error
				credentialAvailable, err = githubCredentials.Configured(ctx, githubapp.AppPrivateKey)
				if err != nil {
					return nil, err
				}
			}
		}
		return map[string]any{
			"initialized": true, "profiles": count, "revision": revision, "policy_generation": generation.Metadata(),
			"tools":    currentSelected,
			"devtools": devtoolsCandidate,
			"github": map[string]any{
				"configured": currentSelected["github"] && c.GitHubAppID != 0, "installation_count": len(c.GitHubInstallations),
				"target_count": len(c.GitHubTargets), "credential_source": credentialSource,
				"credential_available": credentialAvailable,
			},
		}, nil
	}}
	operationGroups := []map[string]rpc.Operation{AuditOperations(log)}
	addOperations := func(module string, groups ...map[string]rpc.Operation) {
		for _, group := range groups {
			operationGroups = append(operationGroups, gateOperations(o.ToolGate, module, group))
		}
	}
	if selected["secrets"] {
		addOperations("secrets", SecretOperations(controller))
	}
	if selected["coordination"] {
		addOperations("coordination",
			ContextJournalOperations(contextJournal), DevtoolsMetadataOperations(devtoolsClient),
			DevtoolsCoordinationOperations(devtoolsClient), DevtoolsCoordinationMutationOperations(devtoolsClient), DevtoolsOperations(devtoolsBroker))
	}
	if selected["github"] {
		addOperations("github", GitHubOperations(githubCredentials),
			GitHubSetupOperations(githubapp.Setup{Credentials: githubCredentials, Configuration: c, Client: githubSetupClient}),
			GitHubRefreshOperations(githubBroker),
			GitHubIssueFieldsOperations(issueFields), GitHubProviderOperations(githubProvider), GitHubCommandOperations(githubCommands))
		if o.PersonalProjects {
			addOperations("personal-projects", GitHubUserOperations(githubUsers))
		}
	}
	if selected["browser"] || selected["execution"] || selected["sharing"] {
		portOperations := PortOperations(&portguard.Guard{Root: workspace, UID: o.AgentUID, Ports: ports})
		if !selected["execution"] && !selected["sharing"] {
			delete(portOperations, "stop")
		}
		addOperations("ports", portOperations)
	}
	if selected["execution"] || selected["sharing"] {
		addOperations("workloads", DockerOperations(dockerproxy.Inspector{Workspace: o.Workspace, SnapshotRoot: o.SnapshotDirectory, Socket: "unix://" + o.DockerSocket, Ports: ports}))
	}
	for _, group := range operationGroups {
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
	server := rpc.Server{Principals: identity.UnixResolver{AgentUID: o.AgentUID, PortInspectorUID: o.PortInspectorUID}, Operations: ops, Audit: AuditSink(log, onAuditError)}
	return server.Serve(ctx, listener)
}
