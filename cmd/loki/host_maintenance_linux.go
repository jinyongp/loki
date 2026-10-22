//go:build linux

package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"loki/internal/fault"
	"loki/modules/execution/jobs/remote"
)

// Host transaction adapters use this finite bridge inside the launcher
// namespace. It carries operation intent only, never arbitrary commands.
func runHostAdmission(args []string, stdout, stderr io.Writer) int {
	if os.Geteuid() != 0 {
		fmt.Fprintln(stderr, "maintenance requires the host administrator")
		return 1
	}
	if len(args) == 0 {
		return 2
	}
	action := args[0]
	flags := flag.NewFlagSet("host maintenance", flag.ContinueOnError)
	flags.SetOutput(stderr)
	socket := flags.String("socket", "/run/loki-go/launcher/control.sock", "root-owned launcher channel")
	operation := flags.String("operation-id", "", "original host transaction UUID")
	interrupt := flags.Bool("interrupt", false, "explicitly cancel and verify owned Job cleanup")
	if flags.Parse(args[1:]) != nil || flags.NArg() != 0 || action != "begin" && action != "end" && action != "status" || action != "begin" && *interrupt || (action == "status") != (*operation == "") {
		return 2
	}
	client, err := remote.NewHostMaintenance(*socket)
	if err != nil {
		return 2
	}
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()
	var result any
	switch action {
	case "begin":
		result, err = client.Begin(ctx, *operation, *interrupt)
	case "end":
		result, err = client.End(ctx, *operation)
	case "status":
		result, err = client.Status(ctx)
	}
	if err != nil {
		fmt.Fprintln(stderr, "loki:", fault.Public(err))
		return 1
	}
	if json.NewEncoder(stdout).Encode(result) != nil {
		return 1
	}
	return 0
}
