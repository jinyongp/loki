package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"path/filepath"
	"syscall"

	"loki/internal/audit"
	"loki/internal/daemon"
	"loki/internal/egress"
	"loki/internal/service"
)

func runEgressProxy(args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("egress-proxy", flag.ContinueOnError)
	flags.SetOutput(stderr)
	port := flags.Int("port", 8766, "loopback proxy port")
	policyPath := flags.String("policy", "", "administrator-owned egress policy")
	profile := flags.String("profile", "", "egress policy profile")
	auditPath := flags.String("audit", "", "private egress audit log")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *port < 1024 || *port > 65535 || *profile == "" || !filepath.IsAbs(*policyPath) || !filepath.IsAbs(*auditPath) {
		fmt.Fprintln(stderr, "egress-proxy requires port, policy, profile, and audit path")
		return 2
	}
	var policy egress.Policy
	if err := daemon.ReadJSON(*policyPath, &policy); err != nil {
		fmt.Fprintf(stderr, "invalid egress policy: %v\n", err)
		return 2
	}
	if err := policy.Validate(); err != nil {
		fmt.Fprintf(stderr, "invalid egress policy: %v\n", err)
		return 2
	}
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: net.ParseIP("127.0.0.1"), Port: *port})
	if err != nil {
		fmt.Fprintln(stderr, "cannot bind egress proxy")
		return 1
	}
	defer listener.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	log := &audit.Log{Path: *auditPath}
	if err = service.RunEgressProxy(ctx, listener, policy, *profile, log, func() error { return daemon.Notify(os.Getenv("NOTIFY_SOCKET"), "READY=1") }, func(error) { fmt.Fprintln(stderr, "egress audit write failed") }); err != nil {
		fmt.Fprintln(stderr, "egress proxy failed")
		return 1
	}
	return 0
}
