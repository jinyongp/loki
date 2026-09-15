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

	"loki/internal/auth"
	"loki/internal/config"
	"loki/internal/daemon"
	"loki/internal/rpc"
	"loki/internal/service"
)

type mcpLayout struct {
	RuntimeSocket, PortGuardSocket, BrowserSocket, RGPath string
	RuntimeUID, PortGuardUID, BrowserUID                  *uint32
	GitTemplateRoots                                      []string
	Environment                                           map[string]string
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
	for _, path := range append([]string{l.RGPath}, l.GitTemplateRoots...) {
		if path != "" && !filepath.IsAbs(path) {
			return service.MCPOptions{}, errors.New("MCP resource paths must be absolute")
		}
	}
	return service.MCPOptions{Runtime: rpc.Client{Socket: l.RuntimeSocket, ExpectedUID: l.RuntimeUID}, PortGuard: rpc.Client{Socket: l.PortGuardSocket, ExpectedUID: l.PortGuardUID}, Browser: service.NewBrowserRPC(l.BrowserSocket, *l.BrowserUID), RuntimeSocket: l.RuntimeSocket, BrowserSocket: l.BrowserSocket, RGPath: l.RGPath, GitTemplateRoots: l.GitTemplateRoots, Environment: l.Environment, Token: token}, nil
}
func runMCP(args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("mcp", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "/etc/loki-go/config.toml", "Loki TOML configuration")
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
	c, err := config.Load(*configPath)
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
