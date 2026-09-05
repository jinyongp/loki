package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"loki/internal/browser"
	"loki/internal/daemon"
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
	proxy := flags.String("proxy", "http://127.0.0.1:8767", "confined browser proxy")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *uid < 0 || *uid > 4294967295 || *gid < 0 {
		fmt.Fprintln(stderr, "browser requires agent UID and socket GID")
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	options := service.BrowserOptions{Socket: *socket, AgentUID: uint32(*uid), SocketGID: *gid, Browser: browser.Options{Binary: *binary, Profile: *profile, Downloads: *downloads, Proxy: *proxy}}
	if err := service.RunBrowser(ctx, options, func() error { return daemon.Notify(os.Getenv("NOTIFY_SOCKET"), "READY=1") }); err != nil {
		fmt.Fprintln(stderr, "browser service failed:", err)
		return 1
	}
	return 0
}
