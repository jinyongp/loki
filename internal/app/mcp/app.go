package mcpapp

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"
	"loki/internal/agentcontext"
	"loki/internal/audit"
	"loki/internal/auth"
	"loki/internal/config"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/daemon"
	"loki/internal/mcpserver"
	"loki/internal/policy"
	"loki/internal/portguard"
	"loki/internal/rpc"
	mcptransport "loki/internal/transport/mcp"
	workspacemcp "loki/internal/transport/mcp/workspace"
	"loki/internal/transport/toolproxy"
	"loki/modules/execution/jobs"
	gitops "loki/modules/git"
	"loki/modules/sharing/artifacts"
	"loki/modules/sharing/previews"
	"loki/modules/workspace"
)

type MCPOptions struct {
	BrowserEngines                                       []toolproxy.Options
	AuthorizeTool                                        func(context.Context, string, string) error
	EndpointDialContext                                  func(context.Context, string, string) (net.Conn, error)
	Tools                                                []string
	OnAuditError                                         func(error)
	Runtime, PortGuard                                   rpc.Caller
	Browser                                              mcptransport.BrowserCaller
	Jobs                                                 jobs.Controller
	GitJobs                                              jobs.Runner
	JobToolchains                                        mcptransport.JobToolchainResolver
	RuntimeSocket, BrowserSocket, ExecutorSocket, RGPath string
	PackagedSkillRoot                                    string
	GitTemplateRoots                                     []string
	GitBinary                                            string
	Environment                                          map[string]string
	IngressHosts                                         []string
	Policy                                               controlpolicy.Generation
	Ports                                                portguard.Policy
	Token                                                string
	ExternalAuth, PreviewExternalAuth                    auth.RequestVerifier
	RequireExternalAuth, RequirePreviewExternalAuth      bool
}

// MCPApp owns local command sessions, share stores and pinned workspace roots.
// Runtime and browser daemons own their independent process lifecycles.
type MCPApp struct {
	Server          *mcp.Server
	Artifacts       *artifacts.Store
	Previews        *previews.Store
	Claims          *mcptransport.DevtoolsSessionClaims
	files           *workspace.Files
	roots           []*policy.Workspace
	preview         *previews.Proxy
	browserSessions *browserSessionPool
	stopDiscovery   func()
	handler         http.Handler
	closed          atomic.Bool
	once            sync.Once
}

func NewMCP(c config.Config, options MCPOptions) (app *MCPApp, err error) {
	selected, err := config.ToolSelection(options.Tools)
	if err != nil {
		return nil, err
	}
	if len(options.Token) < 43 || strings.ContainsAny(options.Token, "\r\n") {
		return nil, errors.New("MCP requires a valid bearer token")
	}
	if (selected["secrets"] || selected["github"] || selected["coordination"]) && options.Runtime == nil {
		return nil, errors.New("selected tools require the runtime service client")
	}
	if selected["execution"] && (options.Jobs == nil || options.PortGuard == nil || options.Runtime == nil) {
		return nil, errors.New("execution requires runtime, port-guard and executor Job clients")
	}
	if !options.Policy.Valid() {
		return nil, errors.New("MCP requires a valid effective policy generation")
	}
	if !errors.Is(options.Ports.Validate(c.Port), portguard.ErrProtected) {
		return nil, errors.New("MCP protected-port policy does not include listener")
	}
	if options.RequireExternalAuth && options.ExternalAuth == nil {
		return nil, errors.New("MCP external request verifier is required")
	}
	if options.RequirePreviewExternalAuth && options.PreviewExternalAuth == nil {
		return nil, errors.New("preview external request verifier is required")
	}
	if selected["git"] && options.GitJobs == nil {
		return nil, errors.New("MCP requires confined Git Job execution")
	}
	if selected["execution"] && options.JobToolchains == nil {
		return nil, errors.New("MCP requires managed Job toolchain resolution")
	}
	app = &MCPApp{}
	if selected["coordination"] {
		app.Claims = mcptransport.NewDevtoolsSessionClaims()
	}
	owned := app
	defer func() {
		if err != nil {
			owned.Close()
		}
	}()
	needsFiles := selected["workspace"] || selected["git"] || selected["sharing"] || selected["coordination"] || selected["browser"] && len(options.BrowserEngines) == 0
	if needsFiles {
		app.files, err = workspace.New(c)
		if err != nil {
			return nil, err
		}
		if options.RGPath != "" {
			app.files.RGPath = options.RGPath
		}
	}
	for _, path := range options.GitTemplateRoots {
		if !selected["git"] {
			break
		}
		root, e := policy.New(path)
		if e != nil {
			return nil, e
		}
		app.roots = append(app.roots, root)
	}
	gitEnvironment := toolEnvironment(options.Environment)
	signingSocket := "/run/loki/signing/agent.sock"
	if value := strings.TrimSpace(options.Environment["SSH_AUTH_SOCK"]); selected["git"] && value != "" {
		if !filepath.IsAbs(value) {
			return nil, errors.New("MCP SSH_AUTH_SOCK must be absolute")
		}
		signingSocket = filepath.Clean(value)
	}
	var repository *gitops.Repository
	if selected["git"] {
		repository, err = gitops.NewRepository(app.files.Policy, c, options.GitJobs, gitEnvironment, app.roots)
		if err != nil {
			return nil, err
		}
		if options.GitBinary != "" {
			if err := repository.SetBinary(options.GitBinary); err != nil {
				return nil, err
			}
		}
		if err = app.files.AttachRepository(repository); err != nil {
			return nil, err
		}
	}
	userHome := options.Environment["HOME"]
	if userHome != "" && !filepath.IsAbs(userHome) {
		return nil, errors.New("MCP HOME must be absolute")
	}
	if options.PackagedSkillRoot != "" && !filepath.IsAbs(options.PackagedSkillRoot) {
		return nil, errors.New("MCP packaged Skill root must be absolute")
	}
	var paths *policy.Workspace
	if app.files != nil {
		paths = app.files.Policy
	}
	agentProvider := &agentcontext.Provider{Paths: paths, UserHome: userHome, PackagedSkills: options.PackagedSkillRoot}
	if repository != nil {
		agentProvider.Git = repository
	}
	if selected["sharing"] && c.ArtifactBaseURL != "" {
		hosts := append([]string{"127.0.0.1", "127.0.0.1:" + strconv.Itoa(c.Port), "localhost", "localhost:" + strconv.Itoa(c.Port)}, c.PublicHosts...)
		app.Artifacts = artifacts.New(artifacts.Options{BaseURL: c.ArtifactBaseURL, AllowedHosts: hosts})
	}
	if selected["sharing"] && c.PreviewBaseDomain != "" {
		if options.Runtime == nil || options.PortGuard == nil || options.Jobs == nil {
			return nil, errors.New("preview sharing requires runtime, port-guard and endpoint ownership clients")
		}
		app.Previews = previews.New(c.PreviewBaseDomain, 0, nil)
	}
	inspect := func(ctx context.Context, port int) (map[string]any, error) {
		var result map[string]any
		err := rpc.DecodeCall(ctx, options.PortGuard, map[string]any{"operation": "inspect", "port": port}, &result)
		return result, err
	}
	preview := &mcptransport.PreviewController{
		Store: app.Previews, Runtime: options.Runtime, Ports: options.Ports, Inspect: inspect, Jobs: options.Jobs,
	}
	if app.Previews != nil {
		app.preview = previews.NewProxy(app.Previews, preview.RouteAllowed)
		app.preview.SetEndpointDialer(options.EndpointDialContext)
	}
	browserAvailable := func() bool {
		return selected["browser"] && (len(options.BrowserEngines) > 0 || mcptransport.BrowserToolsAvailable(options.Browser, options.BrowserSocket))
	}
	system := &mcptransport.SystemController{
		Config: c, Policy: options.Policy, Paths: paths, Started: time.Now(),
		RuntimeSocket: options.RuntimeSocket, BrowserSocket: options.BrowserSocket, SigningSocket: signingSocket,
		Runtime: options.Runtime, BrowserAvailable: browserAvailable,
		Artifacts: app.Artifacts != nil, Previews: app.Previews != nil, GitEnvironment: gitEnvironment,
		InspectPort: func(ctx context.Context, port int) (map[string]any, error) {
			return mcptransport.InspectWorkspacePort(ctx, options.Ports, inspect, options.Runtime, port)
		},
	}
	handlers := map[string]mcpserver.Handler{}
	toolOwners := map[string]string{"developer_view": "workspace"}
	if options.Runtime != nil && paths != nil {
		handlers["system_inspect"] = mcptransport.SystemHandler(system)
	}
	coordination := &mcptransport.DevtoolsSessionCoordination{Runtime: options.Runtime, Claims: app.Claims}
	projectContext := &mcptransport.ProjectContextController{Runtime: options.Runtime, Guidance: agentProvider, Git: repository, Claims: app.Claims}
	groups := []map[string]mcpserver.Handler{}
	addGroups := func(module string, additions ...map[string]mcpserver.Handler) {
		for _, group := range additions {
			for name := range group {
				toolOwners[name] = module
			}
		}
		groups = append(groups, additions...)
	}
	if selected["workspace"] {
		handlers["developer_view"] = mcptransport.DeveloperHandler(app.files)
		files := workspacemcp.WorkspaceHandlers(app.files)
		if !selected["git"] {
			delete(files, "remove_tracked_file")
		}
		addGroups("workspace", files, mcptransport.AgentGuidanceHandlers(agentProvider))
	}
	if selected["git"] {
		addGroups("git", workspacemcp.GitHandlers(repository))
	}
	if selected["secrets"] {
		addGroups("secrets", mcptransport.SecretHandlers(options.Runtime))
	}
	if selected["coordination"] {
		addGroups("coordination", mcptransport.ProjectCoordinationHandlers(options.Runtime, coordination))
		if selected["git"] {
			addGroups("coordination", mcptransport.ProjectContextHandlers(projectContext))
		}
	}
	if selected["execution"] {
		addGroups("execution", mcptransport.JobHandlers(options.Jobs, options.JobToolchains))
	}
	if app.Artifacts != nil {
		addGroups("sharing", mcptransport.ArtifactHandlers(app.files, app.Artifacts))
	}
	var uploads mcptransport.BrowserUploadStager
	if options.BrowserSocket != "" {
		uploads = mcptransport.SocketBrowserUploadStager{Socket: options.BrowserSocket}
	}
	if selected["browser"] && len(options.BrowserEngines) == 0 {
		addGroups("browser", mcptransport.BrowserHandlers(options.Browser, uploads, app.files, app.Artifacts))
	}
	if app.Previews != nil || app.Artifacts != nil {
		addGroups("sharing", mcptransport.PreviewHandlers(preview, app.Artifacts))
	}
	if selected["github"] && c.GitHubAppID != 0 {
		addGroups("github",
			mcptransport.GitHubProviderHandlers(options.Runtime),
			mcptransport.GitHubIssueFieldsHandlers(options.Runtime),
			mcptransport.GitHubCommandHandlers(options.Runtime),
		)
	} else if selected["github"] {
		addGroups("github", mcptransport.GitHubUnavailableHandlers())
	}
	for _, group := range groups {
		for name, handler := range group {
			if handlers[name] != nil {
				return nil, fmt.Errorf("duplicate MCP handler: %s", name)
			}
			handlers[name] = handler
		}
	}
	system.ToolNames = make([]string, 0, len(handlers))
	for name := range handlers {
		system.ToolNames = append(system.ToolNames, name)
	}
	if !filepath.IsAbs(c.AuditLog) {
		return nil, errors.New("MCP audit path must be absolute")
	}
	if err = daemon.PrivateDirectory(filepath.Dir(c.AuditLog)); err != nil {
		return nil, err
	}
	log := &audit.Log{Path: c.AuditLog}
	if _, err = log.Read(1); err != nil {
		return nil, err
	}
	system.Audit = log
	for name, handler := range handlers {
		if module := toolOwners[name]; module != "" && options.AuthorizeTool != nil {
			base := handler
			handler = func(ctx context.Context, args map[string]any) (*mcp.CallToolResult, error) {
				if err := options.AuthorizeTool(ctx, module, name); err != nil {
					return nil, err
				}
				if strings.HasPrefix(name, "project_context") {
					if err := options.AuthorizeTool(ctx, "git", name); err != nil {
						return nil, err
					}
				}
				return base(ctx, args)
			}
		}
		handlers[name] = mcptransport.AuditHandler(log, name, handler, options.OnAuditError)
	}
	origins := mcpserver.ResourceOrigins{ArtifactBaseURL: c.ArtifactBaseURL, PreviewDomain: c.PreviewBaseDomain}
	instructions := mcptransport.InstanceInstructions(c, browserAvailable())
	available := func(name string) bool {
		module := toolOwners[name]
		if module == "" || options.AuthorizeTool == nil {
			return true
		}
		if options.AuthorizeTool(context.Background(), module, name) != nil {
			return false
		}
		return !strings.HasPrefix(name, "project_context") || options.AuthorizeTool(context.Background(), "git", name) == nil
	}
	app.Server, err = mcpserver.NewConfiguredAvailableWithInstructions(handlers, origins, instructions)
	if err != nil {
		return nil, err
	}
	if selected["browser"] && len(options.BrowserEngines) > 0 {
		if err := browserSessionOptions(options.BrowserEngines); err != nil {
			return nil, err
		}
		app.browserSessions = newBrowserSessionPool(app.Server, func(ctx context.Context) (*mcp.Server, *toolproxy.Group, func(), error) {
			var group *toolproxy.Group
			server, err := mcpserver.NewConfiguredAvailableWithInstructions(handlers, origins, instructions, &mcp.ServerOptions{
				InitializedHandler:      func(ctx context.Context, request *mcp.InitializedRequest) { group.SyncRoots(ctx, request.Session) },
				RootsListChangedHandler: func(ctx context.Context, request *mcp.RootsListChangedRequest) { group.SyncRoots(ctx, request.Session) },
			})
			if err != nil {
				return nil, nil, nil, err
			}
			group, err = toolproxy.AttachMany(ctx, server, options.BrowserEngines, system.ToolNames)
			if err != nil {
				if diagnostics := options.BrowserEngines[0].Stderr; diagnostics != nil {
					fmt.Fprintln(diagnostics, "Protected browser session initialization failed:", err)
				}
				return nil, nil, nil, err
			}
			stop, err := mcpserver.WatchAvailable(ctx, server, handlers, available)
			if err != nil {
				group.Close()
				return nil, nil, nil, err
			}
			server.AddReceivingMiddleware(rejectModernDiscovery)
			server.AddReceivingMiddleware(mcptransport.BrowserAvailabilityMiddleware(browserAvailable))
			return server, group, stop, nil
		})
	}
	if options.AuthorizeTool != nil {
		app.stopDiscovery, err = mcpserver.WatchAvailable(context.Background(), app.Server, handlers, available)
		if err != nil {
			return nil, err
		}
	}
	app.Server.AddReceivingMiddleware(rejectModernDiscovery)
	app.Server.AddReceivingMiddleware(mcptransport.BrowserAvailabilityMiddleware(browserAvailable))
	listenerHosts, err := config.NormalizePublicHosts(append(append([]string(nil), c.PublicHosts...), options.IngressHosts...))
	if err != nil {
		return nil, errors.New("MCP ingress Host allowlist is invalid")
	}
	// The configured host allowlist below replaces the SDK's localhost-only
	// default, allowing release-configured and operator-configured ingress hosts.
	getServer := func(*http.Request) *mcp.Server { return app.Server }
	transportOptions := mcpTransportOptions()
	if app.browserSessions != nil {
		getServer = app.browserSessions.server
		transportOptions.SessionTimeout = 20 * time.Minute
	}
	var transport http.Handler = mcp.NewStreamableHTTPHandler(getServer, transportOptions)
	if app.browserSessions != nil {
		transport = app.browserSessions.handler(transport)
	}
	mcpRoute := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		transport.ServeHTTP(w, r)
	})
	ingress := auth.HostPolicy(c.Port, listenerHosts, mcpRoute)
	protected := auth.Gate{Token: options.Token, External: options.ExternalAuth}.Handler(ingress)
	app.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// tunnel-client treats 404 from both PRMD candidates as the supported
		// signal that this bearer-token MCP does not expose OAuth discovery.
		// Keep these probes outside the bearer gate so absence stays a 404.
		if r.URL.Path == "/.well-known/oauth-protected-resource/mcp" ||
			r.URL.Path == "/.well-known/oauth-protected-resource" {
			ingress.ServeHTTP(w, r)
			return
		}
		if app.Previews != nil {
			if _, ok := app.Previews.ResolveHost(r.Host); ok {
				if options.PreviewExternalAuth != nil && !options.PreviewExternalAuth.VerifyRequest(r) {
					auth.Unauthorized(w)
					return
				}
				app.preview.ServeHTTP(w, r)
				return
			}
		}
		if strings.HasPrefix(r.URL.Path, "/artifacts/") {
			if app.Artifacts == nil {
				w.Header().Set("Cache-Control", "no-store")
				http.NotFound(w, r)
			} else {
				app.Artifacts.ServeHTTP(w, r)
			}
			return
		}
		protected.ServeHTTP(w, r)
	})
	return app, nil
}
func (a *MCPApp) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	if a.closed.Load() {
		http.Error(w, "service is shutting down", http.StatusServiceUnavailable)
		return
	}
	a.handler.ServeHTTP(w, r)
}

func (a *MCPApp) Close() {
	a.once.Do(func() {
		a.closed.Store(true)
		if a.stopDiscovery != nil {
			a.stopDiscovery()
		}
		if a.browserSessions != nil {
			a.browserSessions.close()
		}
		if a.preview != nil {
			a.preview.Close()
		}
		if a.Artifacts != nil {
			a.Artifacts.Clear()
		}
		if a.Previews != nil {
			a.Previews.Clear()
		}
		if a.Claims != nil {
			a.Claims.ClearAll()
		}
		for _, root := range a.roots {
			root.Close()
		}
		if a.files != nil {
			a.files.Close()
		}
	})
}

const mcpSessionTimeout = 24 * time.Hour

func mcpTransportOptions() *mcp.StreamableHTTPOptions {
	return &mcp.StreamableHTTPOptions{
		Stateless:                  false,
		JSONResponse:               true,
		MaxRequestBodyBytes:        16777216,
		SessionTimeout:             mcpSessionTimeout,
		DisableLocalhostProtection: true,
	}
}
