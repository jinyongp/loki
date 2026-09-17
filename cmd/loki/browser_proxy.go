package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"loki/internal/browsernet"
	"loki/internal/daemon"
	"loki/internal/rpc"
	"loki/internal/service"
)

func runBrowserProxy(args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("browser-proxy", flag.ContinueOnError)
	flags.SetOutput(stderr)
	port := flags.Int("port", defaultBrowserProxyPort, "loopback proxy port")
	socket := flags.String("port-guard-socket", "", "trusted port-guard socket")
	uid := flags.Int64("port-guard-uid", -1, "expected port-guard UID")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *port < 1024 || *port > 65535 || !filepath.IsAbs(*socket) || *uid < 0 || *uid > 4294967295 {
		fmt.Fprintln(stderr, "browser-proxy requires a valid port and trusted port-guard socket/UID")
		return 2
	}
	expected := uint32(*uid)
	client := rpc.Client{Socket: *socket, ExpectedUID: &expected}
	policy := browsernet.Policy{ValidatePort: func(ctx context.Context, port int) bool {
		var result struct {
			InUse     bool `json:"in_use"`
			Listeners []map[string]any
		}
		raw, err := client.Call(ctx, map[string]any{"operation": "inspect", "port": port})
		return err == nil && json.Unmarshal(raw, &result) == nil && result.InUse && len(result.Listeners) > 0
	}}
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: *port})
	if err != nil {
		fmt.Fprintln(stderr, "cannot bind browser proxy")
		return 1
	}
	defer listener.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	if err = service.RunBrowserProxy(ctx, listener, policy, func() error { return daemon.Notify(os.Getenv("NOTIFY_SOCKET"), "READY=1") }); err != nil {
		fmt.Fprintln(stderr, "browser proxy failed")
		return 1
	}
	return 0
}
