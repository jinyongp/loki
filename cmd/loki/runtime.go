package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"loki/internal/config"
	"loki/internal/daemon"
	"loki/internal/service"
)

func runRuntime(args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("runtime", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "/etc/loki/config.toml", "Loki TOML configuration")
	layoutPath := flags.String("layout", "", "administrator-owned runtime JSON layout")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *layoutPath == "" {
		fmt.Fprintln(stderr, "runtime requires --layout PATH")
		return 2
	}
	c, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, "cannot load runtime configuration")
		return 1
	}
	var options service.RuntimeOptions
	if err = daemon.ReadJSON(*layoutPath, &options); err != nil {
		fmt.Fprintln(stderr, "invalid runtime layout")
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	err = service.RunRuntime(ctx, c, options, func() error { return daemon.Notify(os.Getenv("NOTIFY_SOCKET"), "READY=1") }, func(error) { fmt.Fprintln(stderr, "runtime audit write failed") })
	if err != nil {
		fmt.Fprintln(stderr, "runtime service failed:", err)
		return 1
	}
	return 0
}
