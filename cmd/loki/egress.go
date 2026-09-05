package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"syscall"

	"loki/internal/daemon"
	"loki/internal/service"
)

func runEgressProxy(args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("egress-proxy", flag.ContinueOnError)
	flags.SetOutput(stderr)
	port := flags.Int("port", 8766, "loopback proxy port")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *port < 1024 || *port > 65535 {
		fmt.Fprintln(stderr, "egress-proxy requires a port from 1024 to 65535")
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
	if err = service.RunEgressProxy(ctx, listener, func() error { return daemon.Notify(os.Getenv("NOTIFY_SOCKET"), "READY=1") }); err != nil {
		fmt.Fprintln(stderr, "egress proxy failed")
		return 1
	}
	return 0
}
