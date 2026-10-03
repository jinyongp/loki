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

	appnetwork "loki/internal/app/network"
	"loki/internal/daemon"
	"loki/internal/platform/netguard"
	"loki/internal/rpc"
)

func runBrowserProxy(args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("browser-proxy", flag.ContinueOnError)
	flags.SetOutput(stderr)
	port := flags.Int("port", defaultBrowserProxyPort, "loopback proxy port")
	host := flags.String("host", "127.0.0.1", "proxy listener: loopback or explicit container wildcard")
	contractPath := flags.String("execution-contract", "/usr/share/doc/loki/execution-contract.json", "administrator-owned execution contract")
	socket := flags.String("port-guard-socket", "", "trusted port-guard socket")
	uid := flags.Int64("port-guard-uid", -1, "expected port-guard UID")
	endpoints := flags.String("endpoints-socket", "", "optional protected host endpoint relay")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	listenIP := net.ParseIP(*host)
	if flags.NArg() != 0 || listenIP == nil || !listenIP.IsLoopback() && !listenIP.IsUnspecified() || *port < 1024 || *port > 65535 || !filepath.IsAbs(*contractPath) || !filepath.IsAbs(*socket) || *uid < 0 || *uid > 4294967295 {
		fmt.Fprintln(stderr, "browser-proxy requires a valid port, execution contract, and trusted port-guard socket/UID")
		return 2
	}
	_, contractPort, err := loadExecutionProxy(*contractPath, "browser")
	if err != nil || contractPort != *port {
		fmt.Fprintln(stderr, "browser-proxy port does not match execution contract")
		return 2
	}
	expected := uint32(*uid)
	client := rpc.Client{Socket: *socket, ExpectedUID: &expected}
	policy := netguard.Policy{ValidatePort: func(ctx context.Context, port int) bool {
		var result struct {
			InUse     bool `json:"in_use"`
			Listeners []map[string]any
		}
		raw, err := client.Call(ctx, map[string]any{"operation": "inspect", "port": port})
		return err == nil && json.Unmarshal(raw, &result) == nil && result.InUse && len(result.Listeners) > 0
	}}
	if *endpoints != "" {
		if *endpoints != "/run/loki/endpoints/control.sock" {
			return 2
		}
		dialer := appnetwork.EndpointDialer{Socket: *endpoints, UID: 0}
		policy.DialLocal = dialer.Dial
	}
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: listenIP, Port: *port})
	if err != nil {
		fmt.Fprintln(stderr, "cannot bind browser proxy")
		return 1
	}
	defer listener.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	if err = appnetwork.RunBrowserProxyListener(ctx, listener, policy, listenIP.IsUnspecified(), func() error { return daemon.Notify(os.Getenv("NOTIFY_SOCKET"), "READY=1") }); err != nil {
		fmt.Fprintln(stderr, "browser proxy failed")
		return 1
	}
	return 0
}
