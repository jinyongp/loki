package mcpapp

import (
	"context"
	"errors"
	"fmt"
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
	"loki/internal/integrations/sharing/artifacts"
	"loki/internal/integrations/sharing/previews"
	"loki/internal/mcpserver"
	"loki/internal/policy"
	"loki/internal/portguard"
	"loki/internal/rpc"
	mcptransport "loki/internal/transport/mcp"
	workspacemcp "loki/internal/transport/mcp/workspace"
	"loki/internal/work/jobs"
	"loki/internal/work/workspace"
)

type MCPOptions struct {
	OnAuditError                                         func(error)
	Runtime, PortGuard                                   rpc.Caller
	Browser                                              mcptransport.BrowserCaller
	Jobs                                                 jobs.Controller
	GitJobs                                              jobs.Runner
	JobToolchains                                        mcptransport.JobToolchainResolver
	RuntimeSocket, BrowserSocket, ExecutorSocket, RGPath string
	PackagedSkillRoot                                    string
	GitTemplateRoots                                     []string
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
	Server    *mcp.Server
	Artifacts *artifacts.Store
	Previews  *previews.Store
	Claims    *mcptransport.DevtoolsSessionClaims
	files     *workspace.Files
	roots     []*policy.Workspace
	preview   *previews.Proxy
	handler   http.Handler
	closed    atomic.Bool
	once      sync.Once
}

func NewMCP(c config.Config, options MCPOptions) (app *MCPApp, err error) {
	if len(options.Token) < 43 || strings.ContainsAny(options.Token, "\r\n") {
		return nil, errors.New("MCP requires a valid bearer token")
	}
	if options.Runtime == nil || options.PortGuard == nil || options.Jobs == nil {
		return nil, errors.New("MCP requires runtime, port-guard and executor Job clients")
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
	if options.GitJobs == nil {
		return nil, errors.New("MCP requires confined Git Job execution")
	}
	if options.JobToolchains == nil {
		return nil, errors.New("MCP requires managed Job toolchain resolution")
	}
	app = &MCPApp{Claims: mcptransport.NewDevtoolsSessionClaims()}
	owned := app
	defer func() {
		if err != nil {
			owned.Close()
		}
	}()
	app.files, err = workspace.New(c)
	if err != nil {
		return nil, err
	}
	if options.RGPath != "" {
		app.files.RGPath = options.RGPath
	}
	for _, path := range options.GitTemplateRoots {
		root, e := policy.New(path)
		if e != nil {
			return nil, e
		}
		app.roots = append(app.roots, root)
	}
	gitEnvironment := toolEnvironment(options.Environment)
	repository, err := workspace.NewRepository(app.files.Policy, c, options.GitJobs, gitEnvironment, app.roots)
	if err != nil {
		return nil, err
	}
	if err = app.files.AttachRepository(repository); err != nil {
		return nil, err
	}
	userHome := options.Environment["HOME"]
	if userHome != "" && !filepath.IsAbs(userHome) {
		return nil, errors.New("MCP HOME must be absolute")
	}
	if options.PackagedSkillRoot != "" && !filepath.IsAbs(options.PackagedSkillRoot) {
		return nil, errors.New("MCP packaged Skill root must be absolute")
	}
	agentProvider := &agentcontext.Provider{
		Paths: app.files.Policy, Git: repository, UserHome: userHome, PackagedSkills: options.PackagedSkillRoot,
	}
	if c.ArtifactBaseURL != "" {
		hosts := append([]string{"127.0.0.1", "127.0.0.1:" + strconv.Itoa(c.Port), "localhost", "localhost:" + strconv.Itoa(c.Port)}, c.PublicHosts...)
		app.Artifacts = artifacts.New(artifacts.Options{BaseURL: c.ArtifactBaseURL, AllowedHosts: hosts})
	}
	if c.PreviewBaseDomain != "" {
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
	}
	system := &mcptransport.SystemController{Config: c, Policy: options.Policy, Paths: app.files.Policy, Started: time.Now(), RuntimeSocket: options.RuntimeSocket, BrowserSocket: options.BrowserSocket, Artifacts: app.Artifacts != nil, Previews: app.Previews != nil, GitEnvironment: gitEnvironment, InspectPort: func(ctx context.Context, port int) (map[string]any, error) {
		return mcptransport.InspectWorkspacePort(ctx, options.Ports, inspect, options.Runtime, port)
	}}
	handlers := map[string]mcpserver.Handler{
		"system_inspect": mcptransport.SystemHandler(system), "developer_view": mcptransport.DeveloperHandler(app.files),
	}
	coordination := &mcptransport.DevtoolsSessionCoordination{Runtime: options.Runtime, Claims: app.Claims}
	projectContext := &mcptransport.ProjectContextController{Runtime: options.Runtime, Guidance: agentProvider, Git: repository, Claims: app.Claims}
	groups := []map[string]mcpserver.Handler{
		workspacemcp.WorkspaceHandlers(app.files),
		workspacemcp.GitHandlers(repository),
		mcptransport.SecretHandlers(options.Runtime),
		mcptransport.ProjectCoordinationHandlers(options.Runtime, coordination),
		mcptransport.ProjectContextHandlers(projectContext),
		mcptransport.AgentGuidanceHandlers(agentProvider),
		mcptransport.JobHandlers(options.Jobs, options.JobToolchains),
	}
	if app.Artifacts != nil {
		groups = append(groups, mcptransport.ArtifactHandlers(app.files, app.Artifacts))
	}
	if options.Browser != nil {
		var uploads mcptransport.BrowserUploadStager
		if options.BrowserSocket != "" {
			uploads = mcptransport.SocketBrowserUploadStager{Socket: options.BrowserSocket}
		}
		groups = append(groups, mcptransport.BrowserHandlers(options.Browser, uploads, app.files, app.Artifacts))
	}
	if app.Previews != nil || app.Artifacts != nil {
		groups = append(groups, mcptransport.PreviewHandlers(preview, app.Artifacts))
	}
	if c.GitHubAppID != 0 {
		groups = append(groups,
			mcptransport.GitHubProviderHandlers(options.Runtime),
			mcptransport.GitHubIssueFieldsHandlers(options.Runtime),
			mcptransport.GitHubCommandHandlers(options.Runtime),
		)
	}
	for _, group := range groups {
		for name, handler := range group {
			if handlers[name] != nil {
				return nil, fmt.Errorf("duplicate MCP handler: %s", name)
			}
			handlers[name] = handler
		}
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
		handlers[name] = mcptransport.AuditHandler(log, name, handler, options.OnAuditError)
	}
	app.Server, err = mcpserver.NewConfiguredAvailable(handlers, mcpserver.ResourceOrigins{ArtifactBaseURL: c.ArtifactBaseURL, PreviewDomain: c.PreviewBaseDomain})
	if err != nil {
		return nil, err
	}
	listenerHosts, err := config.NormalizePublicHosts(append(append([]string(nil), c.PublicHosts...), options.IngressHosts...))
	if err != nil {
		return nil, errors.New("MCP ingress Host allowlist is invalid")
	}
	// The configured host allowlist below replaces the SDK's localhost-only
	// default, allowing release-configured and operator-configured ingress hosts.
	transport := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return app.Server }, mcpTransportOptions())
	mcpRoute := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		transport.ServeHTTP(w, r)
	})
	protected := auth.Gate{Token: options.Token, External: options.ExternalAuth}.Handler(auth.HostPolicy(c.Port, listenerHosts, mcpRoute))
	app.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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
