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

	"loki/internal/signing"
)

func runSigning(args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("signing-proxy", flag.ContinueOnError)
	flags.SetOutput(stderr)
	private := flags.String("private-socket", "", "root-owned SSH agent socket")
	public := flags.String("public-socket", "", "new restricted SSH agent socket")
	runner := flags.Int64("runner-uid", -1, "authorized runner UID")
	agent := flags.Int64("agent-uid", -1, "expected private agent UID")
	group := flags.Int("socket-gid", -1, "workspace group ID")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || !filepath.IsAbs(*private) || !filepath.IsAbs(*public) || filepath.Clean(*private) == filepath.Clean(*public) || *runner < 0 || *runner > 4294967295 || *agent < 0 || *agent > 4294967295 || *group < 0 {
		fmt.Fprintln(stderr, "signing-proxy requires distinct absolute sockets and explicit runner/agent UID and socket GID")
		return 2
	}
	listener, err := net.ListenUnix("unix", &net.UnixAddr{Name: *public, Net: "unix"})
	if err != nil {
		fmt.Fprintln(stderr, "cannot create signing socket")
		return 1
	}
	defer listener.Close()
	if err := os.Chown(*public, -1, *group); err != nil {
		fmt.Fprintln(stderr, "cannot set signing socket group")
		return 1
	}
	if err := os.Chmod(*public, 0660); err != nil {
		fmt.Fprintln(stderr, "cannot set signing socket permissions")
		return 1
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	proxy := signing.Proxy{PrivateSocket: *private, RunnerUID: uint32(*runner), AgentUID: uint32(*agent)}
	if err := proxy.Serve(ctx, listener); err != nil {
		fmt.Fprintln(stderr, "signing proxy failed")
		return 1
	}
	return 0
}
