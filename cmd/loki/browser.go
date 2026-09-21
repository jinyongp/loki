package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"loki/internal/config"
	"loki/internal/daemon"
	"loki/internal/integrations/browser"
	"loki/internal/service"
)

func runBrowser(args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("browser", flag.ContinueOnError)
	flags.SetOutput(stderr)
	socket := flags.String("socket", "", "new browser socket")
	uid := flags.Int64("agent-uid", -1, "authorized MCP UID")
	gid := flags.Int("socket-gid", -1, "socket group ID")
	binary := flags.String("chrome", "", "installed Chromium binary")
	profile := flags.String("profile", "", "private persistent browser profile")
	downloads := flags.String("downloads", "", "sandboxed downloads directory")
	configPath := flags.String("config", "/etc/loki-go/config.toml", "Loki public configuration")
	contractPath := flags.String("execution-contract", "/usr/share/doc/loki/execution-contract.json", "administrator-owned execution contract")
	proxy := flags.String("proxy", defaultBrowserProxyURL, "confined browser proxy")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *uid < 0 || *uid > 4294967295 || *gid < 0 || !filepath.IsAbs(*contractPath) || !filepath.IsAbs(*configPath) {
		fmt.Fprintln(stderr, "browser requires agent UID, socket GID, public config, and execution contract")
		return 2
	}
	contractProxy, _, err := loadExecutionProxy(*contractPath, "browser")
	if err != nil || contractProxy != *proxy {
		fmt.Fprintln(stderr, "browser proxy does not match execution contract")
		return 2
	}
	configuration, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, "browser configuration is invalid:", err)
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	options := service.BrowserOptions{
		Socket: *socket, AgentUID: uint32(*uid), SocketGID: *gid,
		Browser: browser.Options{
			Binary: *binary, Profile: *profile, Downloads: *downloads, Proxy: *proxy,
			UploadInbox:    filepath.Join(filepath.Dir(*socket), "uploads"),
			UploadOwnerUID: uint32(*uid),
			MaxUploadFiles: configuration.BrowserMaxUploadFiles,
			MaxUploadBytes: configuration.BrowserMaxUploadBytes,
		},
	}
	if err := service.RunBrowser(ctx, options, func() error { return daemon.Notify(os.Getenv("NOTIFY_SOCKET"), "READY=1") }); err != nil {
		fmt.Fprintln(stderr, "browser service failed:", err)
		return 1
	}
	return 0
}
