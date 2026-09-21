package service

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
	"loki/internal/integrations/sharing/artifacts"
	"loki/internal/audit"
	"loki/internal/auth"
	"loki/internal/config"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/daemon"
	"loki/internal/mcpserver"
	"loki/internal/policy"
	"loki/internal/portguard"
	"loki/internal/integrations/sharing/previews"
	workspacemcp "loki/internal/transport/mcp/workspace"
	"loki/internal/work/jobs"
	"loki/internal/work/workspace"
)

type MCPOptions struct {
	OnAuditError                                         func(error)
	Runtime, PortGuard                                   RuntimeCaller
	Browser                                              BrowserCaller
	Jobs                                                 jobs.Controller
	GitJobs                                              jobs.Runner
	JobToolchains                                        JobToolchainResolver
	RuntimeSocket, BrowserSocket, ExecutorSocket, RGPath string
	PackagedSkillRoot                                    string
	GitTemplateRoots                                     []string
	Environment                                          map[string]string
	Policy                                               controlpolicy.Generation
	Ports                                                portguard.Policy
	Token                                                string
	Access, PreviewAccess                                auth.Verifier
}

// MCPApp owns local command sessions, share stores and pinned workspace roots.
// Runtime and browser daemons own their independent process lifecycles.
type MCPApp struct {
	Server    *mcp.Server
	Artifacts *artifacts.Store
	Previews  *previews.Store
	Claims    *DevtoolsSessionClaims
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
	if c.CloudflareTeamDomain != "" && options.Access == nil {
		return nil, errors.New("MCP Access verifier is required")
	}
	if c.PreviewAccessAudience != "" && options.PreviewAccess == nil {
		return nil, errors.New("preview Access verifier is required")
	}
	if options.GitJobs == nil {
		return nil, errors.New("MCP requires confined Git Job execution")
	}
	if options.JobToolchains == nil {
		return nil, errors.New("MCP requires managed Job toolchain resolution")
	}
	app = &MCPApp{Claims: NewDevtoolsSessionClaims()}
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
		err := runtimeDecode(ctx, options.PortGuard, map[string]any{"operation": "inspect", "port": port}, &result)
		return result, err
	}
	preview := &PreviewController{
		Store: app.Previews, Runtime: options.Runtime, Ports: options.Ports, Inspect: inspect, Jobs: options.Jobs,
	}
	if app.Previews != nil {
		app.preview = previews.NewProxy(app.Previews, preview.RouteAllowed)
	}
	system := &SystemController{Config: c, Policy: options.Policy, Paths: app.files.Policy, Started: time.Now(), RuntimeSocket: options.RuntimeSocket, BrowserSocket: options.BrowserSocket, Artifacts: app.Artifacts != nil, Previews: app.Previews != nil, GitEnvironment: gitEnvironment, InspectPort: func(ctx context.Context, port int) (map[string]any, error) {
		return InspectWorkspacePort(ctx, options.Ports, inspect, options.Runtime, port)
	}}
	handlers := map[string]mcpserver.Handler{
		"system_inspect": SystemHandler(system), "developer_view": DeveloperHandler(app.files),
	}
	coordination := &DevtoolsSessionCoordination{Runtime: options.Runtime, Claims: app.Claims}
	projectContext := &ProjectContextController{Runtime: options.Runtime, Guidance: agentProvider, Git: repository, Claims: app.Claims}
	groups := []map[string]mcpserver.Handler{
		workspacemcp.WorkspaceHandlers(app.files),
		workspacemcp.GitHandlers(repository),
		SecretHandlers(options.Runtime),
		ProjectCoordinationHandlers(options.Runtime, coordination),
		ProjectContextHandlers(projectContext),
		AgentGuidanceHandlers(agentProvider),
		JobHandlers(options.Jobs, options.JobToolchains),
	}
	if app.Artifacts != nil {
		groups = append(groups, ArtifactHandlers(app.files, app.Artifacts))
	}
	if options.Browser != nil {
		groups = append(groups, BrowserHandlers(options.Browser, app.files, app.Artifacts))
	}
	if app.Previews != nil || app.Artifacts != nil {
		groups = append(groups, PreviewHandlers(preview, app.Artifacts))
	}
	if c.GitHubAppID != 0 {
		groups = append(groups, GitHubIssueFieldsHandlers(options.Runtime), GitHubCommandHandlers(options.Runtime))
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
		handlers[name] = auditHandler(log, name, handler, options.OnAuditError)
	}
	app.Server, err = mcpserver.NewConfiguredAvailable(handlers, mcpserver.ResourceOrigins{ArtifactBaseURL: c.ArtifactBaseURL, PreviewDomain: c.PreviewBaseDomain})
	if err != nil {
		return nil, err
	}
	// The configured host allowlist below replaces the SDK's localhost-only
	// default, allowing the explicitly configured reverse-proxy public hosts.
	transport := mcp.NewStreamableHTTPHandler(func(*http.Request) *mcp.Server { return app.Server }, mcpTransportOptions())
	mcpRoute := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/mcp" {
			http.NotFound(w, r)
			return
		}
		transport.ServeHTTP(w, r)
	})
	protected := auth.Gate{Token: options.Token, Access: options.Access}.Handler(auth.HostPolicy(c.Port, c.PublicHosts, mcpRoute))
	app.handler = http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if app.Previews != nil {
			if _, ok := app.Previews.ResolveHost(r.Host); ok {
				if options.PreviewAccess != nil {
					values := r.Header.Values("Cf-Access-Jwt-Assertion")
					if len(values) != 1 || !options.PreviewAccess.Verify(values[0]) {
						auth.Unauthorized(w)
						return
					}
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
			a.Claims.clearAll()
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
