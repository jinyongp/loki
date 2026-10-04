package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"regexp"
	"syscall"
	"time"

	appnetwork "loki/internal/app/network"
	"loki/internal/config"
	"loki/internal/daemon"
	"loki/internal/rpc"
	"loki/internal/tools"
)

func runFullEndpoints(args []string, diagnostics io.Writer) int {
	f := flag.NewFlagSet("owned-endpoints", flag.ContinueOnError)
	f.SetOutput(diagnostics)
	socket := f.String("socket", "", "protected host endpoint socket")
	activation := f.String("tools-config", "", "administrator-owned full activation")
	if f.Parse(args) != nil {
		return 2
	}
	if f.NArg() != 0 || os.Geteuid() != 0 || *socket != "/run/loki/endpoints/control.sock" || *activation != "/etc/loki/activation/state.json" {
		fmt.Fprintln(diagnostics, "owned endpoints require their protected host role")
		return 2
	}
	gate := config.ToolGate{Path: *activation, Release: tools.Release, Mode: tools.Full, Snapshot: true}
	enabled := func() error {
		if _, err := gate.Selection("execution"); err == nil {
			return nil
		}
		if _, err := gate.Selection("sharing"); err == nil {
			return nil
		}
		return fmt.Errorf("managed endpoints require enabled execution or sharing")
	}
	uid := uint32(0)
	client := rpc.Client{Socket: "/run/loki/launcher/control.sock", ExpectedUID: &uid, Limits: rpc.Limits{Timeout: 2 * time.Second}}
	identity := regexp.MustCompile(`^[a-f0-9]{32}$`)
	validate := func(ctx context.Context, port int) (string, error) {
		raw, err := client.Call(ctx, map[string]any{"operation": "endpoint_resolve", "port": port})
		if err != nil {
			return "", err
		}
		var lease struct {
			Lease string `json:"lease"`
			Job   string `json:"job_id"`
			Port  int    `json:"port"`
		}
		if json.Unmarshal(raw, &lease) != nil || lease.Port != port || !identity.MatchString(lease.Lease) || !identity.MatchString(lease.Job) {
			return "", fmt.Errorf("invalid protected endpoint authority")
		}
		return lease.Job + ":" + lease.Lease, nil
	}
	listener, err := daemon.Listen(*socket, 10001)
	if err != nil {
		fmt.Fprintln(diagnostics, "cannot bind protected endpoint socket")
		return 1
	}
	defer listener.Close()
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	if err := appnetwork.RunOwnedEndpoints(ctx, listener, []uint32{10000, 10005}, validate, enabled, nil); err != nil {
		fmt.Fprintln(diagnostics, "owned endpoint service failed")
		return 1
	}
	return 0
}
