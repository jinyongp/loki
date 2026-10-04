package main

import (
	"context"
	"errors"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strconv"
	"syscall"
	"time"

	appbrowser "loki/internal/app/browser"
	mcpapp "loki/internal/app/mcp"
	appnetwork "loki/internal/app/network"
	appruntime "loki/internal/app/runtime"
	"loki/internal/auth"
	"loki/internal/config"
	"loki/internal/daemon"
	hostpolicy "loki/internal/host/policy"
	cloudflareaccess "loki/internal/integrations/access/cloudflare"
	"loki/internal/rpc"
	"loki/internal/tools"
	"loki/internal/transport/toolproxy"
	projectbrowser "loki/modules/browser"
	jobsremote "loki/modules/execution/jobs/remote"
)

type mcpLayout struct {
	EndpointSocket                                                        string
	Tools                                                                 []string
	BrowserProtocol                                                       string
	BrowserCapabilities                                                   []string
	ToolsConfigPath                                                       string
	ToolsConfigSnapshot                                                   bool
	RuntimeSocket, PortGuardSocket, BrowserSocket, ExecutorSocket, RGPath string
	ExecutionContract, PackagedSkillRoot                                  string
	ToolchainStore, ToolchainCatalog                                      string
	RuntimeUID, PortGuardUID, BrowserUID, ExecutorUID                     *uint32
	GitTemplateRoots                                                      []string
	GitBinary                                                             string
	Environment                                                           map[string]string
}

func (l mcpLayout) options(token string) (mcpapp.MCPOptions, error) {
	selected, err := config.ToolSelection(l.Tools)
	if err != nil {
		return mcpapp.MCPOptions{}, err
	}
	needsRuntime := selected["secrets"] || selected["github"] || selected["coordination"] || selected["execution"] || selected["sharing"]
	needsPorts := selected["execution"] || selected["sharing"]
	for _, peer := range []struct {
		socket   string
		uid      *uint32
		required bool
	}{
		{l.RuntimeSocket, l.RuntimeUID, needsRuntime}, {l.PortGuardSocket, l.PortGuardUID, needsPorts},
	} {
		if peer.required || peer.socket != "" || peer.uid != nil {
			if !filepath.IsAbs(peer.socket) || peer.uid == nil {
				return mcpapp.MCPOptions{}, errors.New("selected MCP service requires an absolute core socket and explicit expected UID")
			}
		}
	}
	hasBrowserSocket := l.BrowserSocket != ""
	hasBrowserUID := l.BrowserUID != nil
	if hasBrowserSocket != hasBrowserUID {
		return mcpapp.MCPOptions{}, errors.New("MCP browser socket and UID must be configured together")
	}
	if hasBrowserSocket && (!filepath.IsAbs(l.BrowserSocket) || filepath.Clean(l.BrowserSocket) != l.BrowserSocket) {
		return mcpapp.MCPOptions{}, errors.New("MCP browser peer configuration is invalid")
	}
	if !filepath.IsAbs(l.ExecutionContract) {
		return mcpapp.MCPOptions{}, errors.New("MCP execution contract path must be absolute")
	}
	if selected["workspace"] && !filepath.IsAbs(l.PackagedSkillRoot) {
		return mcpapp.MCPOptions{}, errors.New("MCP packaged Skill root must be absolute")
	}
	for _, path := range append([]string{l.GitBinary, l.RGPath, l.ExecutionContract, l.PackagedSkillRoot, l.ToolchainStore, l.ToolchainCatalog}, l.GitTemplateRoots...) {
		if path != "" && !filepath.IsAbs(path) {
			return mcpapp.MCPOptions{}, errors.New("MCP resource paths must be absolute")
		}
	}

	options := mcpapp.MCPOptions{
		Tools:         l.Tools,
		RuntimeSocket: l.RuntimeSocket,
		RGPath:        l.RGPath, PackagedSkillRoot: l.PackagedSkillRoot,
		GitTemplateRoots: l.GitTemplateRoots, GitBinary: l.GitBinary, Environment: l.Environment, Token: token,
	}
	var gate *config.ToolGate
	if l.ToolsConfigPath != "" {
		gate = &config.ToolGate{Path: l.ToolsConfigPath, Release: tools.Release, Mode: tools.Full, Snapshot: l.ToolsConfigSnapshot}
		if gate.Revision() == "unavailable" {
			return mcpapp.MCPOptions{}, errors.New("host-published tool selection is unavailable")
		}
		for module, enabled := range selected {
			if enabled {
				if _, err := gate.Selection(module); err != nil {
					return mcpapp.MCPOptions{}, fmt.Errorf("MCP layout differs from enabled tools: %w", err)
				}
			}
		}
	}
	if l.RuntimeSocket != "" {
		options.Runtime = rpc.Client{Socket: l.RuntimeSocket, ExpectedUID: l.RuntimeUID}
	}
	if l.PortGuardSocket != "" {
		options.PortGuard = rpc.Client{Socket: l.PortGuardSocket, ExpectedUID: l.PortGuardUID}
	}
	if hasBrowserSocket {
		// Optional browser authority is dynamic. Construct the RPC client even
		// when the socket is absent so a later lifecycle enable becomes usable
		// without restarting the MCP process.
		if l.BrowserProtocol == "official" {
			options.BrowserEngines = []toolproxy.Options{{
				Name: "loki-protected-browser", Owner: "browser/protected", Version: "0.2.2",
				Transport: toolproxy.ProtectedBrowserTransport{Socket: l.BrowserSocket, ExpectedUID: *l.BrowserUID},
				RootURI:   "file:///var/lib/loki/browser/work",
				Authorize: func(name string) error {
					caps := l.BrowserCapabilities
					if gate != nil {
						current, err := gate.Selection("browser")
						if err != nil {
							return err
						}
						caps = current.Capabilities
						if !slices.Equal(caps, l.BrowserCapabilities) {
							return errors.New("browser capabilities changed; restart the protected service and reconnect")
						}
					}
					if !selected["browser"] || !projectbrowser.Allowed(name, caps) {
						return errors.New("browser is disabled or requires an explicit capability")
					}
					return nil
				},
				AuthorizeResource: func() error {
					if gate != nil {
						_, err := gate.Selection("browser")
						return err
					}
					return nil
				},
				ForwardOwnedResources: true,
			}}
			if gate != nil {
				options.BrowserEngines[0].Revision = gate.Revision
			}
		} else if l.BrowserProtocol == "" || l.BrowserProtocol == "rpc" {
			options.Browser = appbrowser.NewBrowserRPC(l.BrowserSocket, *l.BrowserUID)
		} else {
			return mcpapp.MCPOptions{}, errors.New("unknown browser service protocol")
		}
		options.BrowserSocket = l.BrowserSocket
	}
	hasExecutorSocket := l.ExecutorSocket != ""
	hasExecutorUID := l.ExecutorUID != nil
	if hasExecutorSocket != hasExecutorUID {
		return mcpapp.MCPOptions{}, errors.New("MCP executor socket and UID must be configured together")
	}
	if hasExecutorSocket && (selected["git"] || selected["execution"] || selected["sharing"]) {
		if !filepath.IsAbs(l.ExecutorSocket) || filepath.Clean(l.ExecutorSocket) != l.ExecutorSocket ||
			l.ExecutorSocket == string(filepath.Separator) || *l.ExecutorUID == 0 {
			return mcpapp.MCPOptions{}, errors.New("MCP executor peer configuration is invalid")
		}
		executor, err := jobsremote.NewExecutor(jobsremote.ExecutorOptions{
			Socket: l.ExecutorSocket, ExpectedUID: l.ExecutorUID, Timeout: 30 * time.Second,
		})
		if err != nil {
			return mcpapp.MCPOptions{}, err
		}
		options.Jobs = executor
		options.GitJobs = executor
		options.ExecutorSocket = l.ExecutorSocket
	}
	if gate != nil {
		readiness := &mcpPeerReadiness{}
		var executor rpc.Caller
		if hasExecutorSocket {
			executor = rpc.Client{Socket: l.ExecutorSocket, ExpectedUID: l.ExecutorUID, Limits: rpc.Limits{Timeout: 2 * time.Second}}
		}
		options.AuthorizeTool = func(ctx context.Context, module, _ string) error {
			if _, err := gate.Selection(module); err != nil {
				return err
			}
			if err := readiness.authorize(ctx, module, options.Runtime, executor); err != nil {
				return err
			}
			native := ""
			if module == "git" {
				native = l.GitBinary
			}
			if module == "workspace" {
				native = l.RGPath
			}
			if err := readiness.native(ctx, module, native); err != nil {
				return err
			}
			_, err := gate.Selection(module)
			return err
		}
	}
	if l.EndpointSocket != "" {
		if l.EndpointSocket != "/run/loki/endpoints/control.sock" || !selected["sharing"] {
			return mcpapp.MCPOptions{}, errors.New("MCP host endpoint relay requires selected sharing")
		}
		dialer := appnetwork.EndpointDialer{Socket: l.EndpointSocket, UID: 0}
		options.EndpointDialContext = func(ctx context.Context, network, target string) (net.Conn, error) {
			host, text, err := net.SplitHostPort(target)
			if err != nil || network != "tcp" || host != "127.0.0.1" {
				return nil, errors.New("only owned loopback endpoints are allowed")
			}
			port, err := strconv.Atoi(text)
			if err != nil {
				return nil, err
			}
			if gate != nil {
				if _, err := gate.Selection("sharing"); err != nil {
					return nil, err
				}
			}
			return dialer.Dial(ctx, port)
		}
	}
	return options, nil
}

func runMCP(args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("mcp", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "/etc/loki-go/config.toml", "Loki TOML configuration")
	githubConfigPath := flags.String("github-config", "", "deployment-provided public GitHub TOML configuration")
	ingressConfigPath := flags.String("ingress-config", "", "operator-owned MCP ingress Host allowlist")
	layoutPath := flags.String("layout", "", "administrator-owned MCP JSON layout")
	tokenPath := flags.String("token-file", "/etc/loki-go/token", "MCP bearer token file")
	jwksPath := flags.String("jwks-file", "/etc/loki-go/cloudflare-jwks.json", "Cloudflare verification keys")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *layoutPath == "" {
		fmt.Fprintln(stderr, "mcp requires --layout PATH")
		return 2
	}
	if *ingressConfigPath == "" {
		if _, statErr := os.Stat("/etc/loki/ingress.toml"); statErr == nil {
			*ingressConfigPath = "/etc/loki/ingress.toml"
		} else if !errors.Is(statErr, os.ErrNotExist) {
			fmt.Fprintln(stderr, "cannot inspect MCP ingress configuration")
			return 1
		}
	}
	c, err := config.LoadWithGitHub(*configPath, *githubConfigPath)
	if err != nil {
		fmt.Fprintln(stderr, "cannot load MCP configuration")
		return 1
	}
	ingressHosts, err := config.LoadIngressPublicHosts(*ingressConfigPath)
	if err != nil {
		fmt.Fprintln(stderr, "cannot load MCP ingress configuration")
		return 1
	}
	var layout mcpLayout
	if err = daemon.ReadJSON(*layoutPath, &layout); err != nil {
		fmt.Fprintln(stderr, "invalid MCP layout")
		return 2
	}
	token, err := auth.LoadToken(*tokenPath)
	if err != nil {
		fmt.Fprintln(stderr, "cannot load MCP bearer token")
		return 1
	}
	options, err := layout.options(token)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	options.IngressHosts = ingressHosts
	selected, err := config.ToolSelection(layout.Tools)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 2
	}
	if selected["execution"] {
		options.JobToolchains, err = newMCPToolchainResolver(c.Root, layout.ToolchainStore, layout.ToolchainCatalog)
		if err != nil {
			fmt.Fprintln(stderr, "invalid MCP toolchain configuration")
			return 2
		}
	}
	contract, err := loadExecutionContract(layout.ExecutionContract)
	if err != nil {
		fmt.Fprintln(stderr, "invalid MCP execution contract")
		return 2
	}
	options.Policy, err = hostpolicy.Compile(c, contract)
	if err != nil {
		fmt.Fprintln(stderr, "invalid MCP effective policy")
		return 2
	}
	options.Ports, err = appruntime.ProtectedPortPolicy(c.Port, contract)
	if err != nil {
		fmt.Fprintln(stderr, "invalid MCP protected-port policy")
		return 2
	}
	if c.CloudflareTeamDomain != "" {
		options.RequireExternalAuth = true
		options.ExternalAuth = cloudflareaccess.Access{TeamDomain: c.CloudflareTeamDomain, Audience: c.CloudflareAudience, JWKSPath: *jwksPath}
	}
	if c.PreviewAccessAudience != "" {
		options.RequirePreviewExternalAuth = true
		options.PreviewExternalAuth = cloudflareaccess.Access{TeamDomain: c.CloudflareTeamDomain, Audience: c.PreviewAccessAudience, JWKSPath: *jwksPath}
	}
	options.OnAuditError = func(error) { fmt.Fprintln(stderr, "MCP audit write failed") }
	listenIP := net.ParseIP(c.Host)
	network := "tcp6"
	if listenIP.To4() != nil {
		network = "tcp4"
	}
	listener, err := net.ListenTCP(network, &net.TCPAddr{IP: listenIP, Port: c.Port})
	if err != nil {
		fmt.Fprintln(stderr, "cannot bind MCP listener")
		return 1
	}
	defer listener.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	if err = mcpapp.RunMCP(ctx, c, options, listener, func() error { return daemon.Notify(os.Getenv("NOTIFY_SOCKET"), "READY=1") }); err != nil {
		fmt.Fprintln(stderr, "MCP service failed:", err)
		return 1
	}
	return 0
}
