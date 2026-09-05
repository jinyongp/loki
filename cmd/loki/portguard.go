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

	"loki/internal/daemon"
	"loki/internal/portguard"
	"loki/internal/rpc"
	"loki/internal/service"
)

func runPortGuard(args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("port-guard", flag.ContinueOnError)
	flags.SetOutput(stderr)
	workspace := flags.String("workspace", "", "trusted workspace root")
	socket := flags.String("socket", "", "new port-guard socket")
	uid := flags.Int64("agent-uid", -1, "authorized workspace UID")
	gid := flags.Int("socket-gid", -1, "socket group ID")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || !filepath.IsAbs(*workspace) || !filepath.IsAbs(*socket) || *uid < 0 || *uid > 4294967295 || *gid < 0 {
		fmt.Fprintln(stderr, "port-guard requires absolute workspace/socket paths and agent UID/socket GID")
		return 2
	}
	root, err := filepath.EvalSymlinks(*workspace)
	if err != nil {
		fmt.Fprintln(stderr, "cannot resolve port-guard workspace")
		return 1
	}
	listener, err := daemon.Listen(*socket, *gid)
	if err != nil {
		fmt.Fprintln(stderr, "cannot create port-guard socket")
		return 1
	}
	defer listener.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	guard := &portguard.Guard{Root: root, UID: uint32(*uid)}
	server := rpc.Server{AgentUID: uint32(*uid), Operations: service.PortOperations(guard)}
	if err = daemon.Notify(os.Getenv("NOTIFY_SOCKET"), "READY=1"); err != nil {
		fmt.Fprintln(stderr, "port-guard readiness notification failed")
		return 1
	}
	if err = server.Serve(ctx, listener); err != nil {
		fmt.Fprintln(stderr, "port-guard service failed")
		return 1
	}
	return 0
}
