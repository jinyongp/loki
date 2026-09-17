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
	host := flags.String("host", "127.0.0.1", "proxy listen address")
	port := flags.Int("port", defaultEgressProxyPort, "loopback proxy port")
	policyPath := flags.String("policy", "", "administrator-owned egress policy")
	profile := flags.String("profile", "", "egress policy profile")
	auditPath := flags.String("audit", "", "private egress audit log")
	forwardHost := flags.String("forward-host", "", "optional TCP forward listen address")
	forwardPort := flags.Int("forward-port", 0, "optional TCP forward listen port")
	forwardTarget := flags.String("forward-target", "", "optional TCP forward target")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	listenIP := net.ParseIP(*host)
	if flags.NArg() != 0 || listenIP == nil || !listenIP.IsUnspecified() && !listenIP.IsLoopback() || *port < 1024 || *port > 65535 || *profile == "" || !filepath.IsAbs(*policyPath) || !filepath.IsAbs(*auditPath) {
		fmt.Fprintln(stderr, "egress-proxy requires port, policy, profile, and audit path")
		return 2
	}
	var forwardListener *net.TCPListener
	if *forwardHost != "" || *forwardPort != 0 || *forwardTarget != "" {
		forwardIP := net.ParseIP(*forwardHost)
		targetHost, targetPort, targetErr := net.SplitHostPort(*forwardTarget)
		if forwardIP == nil || !forwardIP.IsUnspecified() && !forwardIP.IsLoopback() || *forwardPort < 1024 || *forwardPort > 65535 || targetErr != nil || targetHost == "" || targetPort == "" {
			fmt.Fprintln(stderr, "TCP forward requires a listen address, port, and target")
			return 2
		}
		var listenErr error
		forwardListener, listenErr = net.ListenTCP("tcp4", &net.TCPAddr{IP: forwardIP, Port: *forwardPort})
		if listenErr != nil {
			fmt.Fprintln(stderr, "cannot bind TCP forward")
			return 1
		}
		defer forwardListener.Close()
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
	listener, err := net.ListenTCP("tcp4", &net.TCPAddr{IP: listenIP, Port: *port})
	if err != nil {
		fmt.Fprintln(stderr, "cannot bind egress proxy")
		return 1
	}
	defer listener.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	log := &audit.Log{Path: *auditPath}
	var forwardDone chan error
	if forwardListener != nil {
		forwardDone = make(chan error, 1)
		go func() {
			forwardDone <- service.RunTCPForward(ctx, forwardListener, *forwardTarget)
			cancel()
		}()
	}
	err = service.RunEgressProxy(ctx, listener, policy, *profile, log, func() error { return daemon.Notify(os.Getenv("NOTIFY_SOCKET"), "READY=1") }, func(error) { fmt.Fprintln(stderr, "egress audit write failed") })
	cancel()
	if forwardDone != nil {
		if forwardErr := <-forwardDone; forwardErr != nil {
			fmt.Fprintln(stderr, "TCP forward failed")
			return 1
		}
	}
	if err != nil {
		fmt.Fprintln(stderr, "egress proxy failed")
		return 1
	}
	return 0
}
