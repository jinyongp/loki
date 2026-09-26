package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os/signal"
	"syscall"

	"loki/internal/config"
	cloudflareaccess "loki/internal/integrations/access/cloudflare"
)

func runJWKSRefresh(args []string, stderr io.Writer) int {
	flags := flag.NewFlagSet("jwks-refresh", flag.ContinueOnError)
	flags.SetOutput(stderr)
	configPath := flags.String("config", "/etc/loki-go/config.toml", "Loki TOML configuration")
	output := flags.String("output", "/etc/loki-go/cloudflare-jwks.json", "JWKS output file")
	gid := flags.Int("gid", -1, "workspace group ID")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if flags.NArg() != 0 || *gid < 0 {
		fmt.Fprintln(stderr, "jwks-refresh requires --gid")
		return 2
	}
	c, err := config.Load(*configPath)
	if err != nil {
		fmt.Fprintln(stderr, "cannot load JWKS configuration")
		return 1
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	if err = cloudflareaccess.RefreshKeys(ctx, c.CloudflareTeamDomain, *output, *gid, nil); err != nil {
		fmt.Fprintln(stderr, "JWKS refresh failed:", err)
		return 1
	}
	return 0
}
