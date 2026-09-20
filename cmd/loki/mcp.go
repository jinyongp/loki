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
	"syscall"
	"time"

	"loki/internal/auth"
	"loki/internal/config"
	"loki/internal/daemon"
	hostpolicy "loki/internal/host/policy"
	"loki/internal/rpc"
	"loki/internal/service"
	jobsremote "loki/internal/work/jobs/remote"
)

type mcpLayout struct {
	RuntimeSocket, PortGuardSocket, BrowserSocket, ExecutorSocket, RGPath string
	ExecutionContract, PackagedSkillRoot                                  string
	RuntimeUID, PortGuardUID, BrowserUID, ExecutorUID                     *uint32
	GitTemplateRoots                                                      []string
	Environment                                                           map[string]string
}

func (l mcpLayout) options(token string) (service.MCPOptions, error) {
	for _, path := range []string{l.RuntimeSocket, l.PortGuardSocket, l.BrowserSocket} {
		if !filepath.IsAbs(path) {
			return service.MCPOptions{}, errors.New("MCP socket paths must be absolute")
		}
	}
	if l.RuntimeUID == nil || l.PortGuardUID == nil || l.BrowserUID == nil {
		return service.MCPOptions{}, errors.New("MCP layout requires explicit service UIDs")
	}
	if !filepath.IsAbs(l.ExecutionContract) {
		return service.MCPOptions{}, errors.New("MCP execution contract path must be absolute")
	}
	if !filepath.IsAbs(l.PackagedSkillRoot) {
		return service.MCPOptions{}, errors.New("MCP packaged Skill root must be absolute")
	}
	for _, path := range append([]string{l.RGPath, l.ExecutionContract, l.PackagedSkillRoot}, l.GitTemplateRoots...) {
		if path != "" && !filepath.IsAbs(path) {
			return service.MCPOptions{}, errors.New("MCP resource paths must be absolute")
		}
	}

	options := service.MCPOptions{
		Runtime:       rpc.Client{Socket: l.RuntimeSocket, ExpectedUID: l.RuntimeUID},
		PortGuard:     rpc.Client{Socket: l.PortGuardSocket, ExpectedUID: l.PortGuardUID},
		Browser:       service.NewBrowserRPC(l.BrowserSocket, *l.BrowserUID),
		RuntimeSocket: l.RuntimeSocket, BrowserSocket: l.BrowserSocket,
		RGPath: l.RGPath, PackagedSkillRoot: l.PackagedSkillRoot,
		GitTemplateRoots: l.GitTemplateRoots, Environment: l.Environment, Token: token,
	}
	hasExecutorSocket := l.ExecutorSocket != ""
	hasExecutorUID := l.ExecutorUID != nil
	if hasExecutorSocket != hasExecutorUID {
		return service.MCPOptions{}, errors.New("MCP executor socket and UID must be configured together")
	}
	if hasExecutorSocket {
		if !filepath.IsAbs(l.ExecutorSocket) || filepath.Clean(l.ExecutorSocket) != l.ExecutorSocket ||
			l.ExecutorSocket == string(filepath.Separator) || *l.ExecutorUID == 0 {
			return service.MCPOptions{}, errors.New("MCP executor peer configuration is invalid")
		}
		executor, err := jobsremote.NewExecutor(jobsremote.ExecutorOptions{
			Socket: l.ExecutorSocket, ExpectedUID: l.ExecutorUID, Timeout: 30 * time.Second,
		})
		if err != nil {
			return service.MCPOptions{}, err
		}
		options.Jobs = executor
		options.ExecutorSocket = l.ExecutorSocket
	}
	return options, nil
}

func runMCP(args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("mcp", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "/etc/loki-go/config.toml", "Loki TOML configuration")
	githubConfigPath := flags.String("github-config", "", "deployment-provided public GitHub TOML configuration")
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
	c, err := config.LoadWithGitHub(*configPath, *githubConfigPath)
	if err != nil {
		fmt.Fprintln(stderr, "cannot load MCP configuration")
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
	options.Ports, err = service.ProtectedPortPolicy(c.Port, contract)
	if err != nil {
		fmt.Fprintln(stderr, "invalid MCP protected-port policy")
		return 2
	}
	if c.CloudflareTeamDomain != "" {
		options.Access = auth.Access{TeamDomain: c.CloudflareTeamDomain, Audience: c.CloudflareAudience, JWKSPath: *jwksPath}
	}
	if c.PreviewAccessAudience != "" {
		options.PreviewAccess = auth.Access{TeamDomain: c.CloudflareTeamDomain, Audience: c.PreviewAccessAudience, JWKSPath: *jwksPath}
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
	if err = service.RunMCP(ctx, c, options, listener, func() error { return daemon.Notify(os.Getenv("NOTIFY_SOCKET"), "READY=1") }); err != nil {
		fmt.Fprintln(stderr, "MCP service failed:", err)
		return 1
	}
	return 0
}
