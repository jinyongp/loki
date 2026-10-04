package main

import (
	"context"
	"fmt"
	"io"
	"os"

	"loki/internal/auth"
	"loki/internal/transport/toolproxy"
)

// This private adapter is executed in the exact owned MCP container as its
// non-root role. Its bearer and loopback destination never leave that role.
func runFullMCPConnection(args []string, stderr io.Writer) int {
	if len(args) != 0 || os.Geteuid() != 10000 {
		fmt.Fprintln(stderr, "Full MCP connection requires its non-root service role without arguments")
		return 2
	}
	token, err := auth.LoadToken("/etc/loki/auth/token")
	if err != nil {
		fmt.Fprintln(stderr, "Full MCP service bearer is unavailable")
		return 1
	}
	private := []byte(token)
	defer clear(private)
	transport, cleanup, err := toolproxy.LocalHTTPTransport("http://127.0.0.1:18765/mcp", private)
	if err != nil {
		fmt.Fprintln(stderr, "Full MCP loopback transport is invalid")
		return 1
	}
	defer cleanup()
	err = toolproxy.Run(context.Background(), toolproxy.Options{
		Name: "loki-full-service-connection", Owner: "full", Version: "0.2.1",
		Transport: transport, RootURI: "file:///workspace", ForwardOwnedResources: true, Stderr: stderr,
		// The service's freshly checked public bindings and resource handlers
		// enforce selection; this adapter adds no filesystem or provider access.
		Authorize: func(string) error { return nil }, AuthorizeResource: func() error { return nil },
	})
	if err != nil {
		fmt.Fprintln(stderr, "Full MCP stdio connection failed:", err)
		return 1
	}
	return 0
}
