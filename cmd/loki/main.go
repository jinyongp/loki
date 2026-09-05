package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"strconv"
	"strings"
	"syscall"
	"time"

	"loki/internal/action"
	"loki/internal/bootstrap"
	"loki/internal/buildinfo"
	"loki/internal/contract"
	"loki/internal/rpc"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 && args[0] == "jwks-refresh" {
		return runJWKSRefresh(args[1:], stderr)
	}
	if len(args) > 0 && args[0] == "egress-proxy" {
		return runEgressProxy(args[1:], stderr)
	}
	if len(args) > 0 && args[0] == "mcp" {
		return runMCP(args[1:], stderr)
	}
	if len(args) > 0 && args[0] == "browser-proxy" {
		return runBrowserProxy(args[1:], stderr)
	}
	if len(args) > 0 && args[0] == "browser" {
		return runBrowser(args[1:], stderr)
	}
	if len(args) > 0 && args[0] == "port-guard" {
		return runPortGuard(args[1:], stderr)
	}
	if len(args) > 0 && args[0] == "runtime" {
		return runRuntime(args[1:], stderr)
	}
	if len(args) > 0 && args[0] == "signing-agent" {
		return runSigningAgent(args[1:], stderr)
	}
	if len(args) > 0 && args[0] == "signing-proxy" {
		return runSigning(args[1:], stderr)
	}
	if len(args) >= 2 && args[0] == "internal" && args[1] == "bootstrap" {
		return runBootstrap(args[2:], stdout, stderr)
	}
	if len(args) == 2 && args[0] == "internal" && args[1] == "action-files" {
		if err := action.RunFileOperation(os.Stdin, stdout); err != nil {
			fmt.Fprintln(stderr, "action file operation failed")
			return 125
		}
		return 0
	}
	if len(args) == 2 && args[0] == "internal" && args[1] == "action-exec" {
		if err := action.ExecInSandbox(os.Stdin); err != nil {
			fmt.Fprintln(stderr, action.PublicExecutionError(err))
			return 125
		}
		return 0
	}
	if len(args) >= 2 && args[0] == "contract" && args[1] == "capture" {
		return capture(args[2:], stdout, stderr)
	}
	if len(args) == 1 && (args[0] == "version" || args[0] == "--version") {
		fmt.Fprintln(stdout, buildinfo.String())
		return 0
	}

	fmt.Fprintln(stderr, "usage: loki version | contract capture --output PATH [--url URL --token-file PATH]")
	return 2
}

func runBootstrap(args []string, stdout, stderr io.Writer) int {
	if len(args) != 4 || !filepath.IsAbs(args[0]) {
		fmt.Fprintln(stderr, "bootstrap requires SOCKET EXPECTED_UID CWD WORKFLOW")
		return 2
	}
	uid, err := strconv.ParseUint(args[1], 10, 32)
	if err != nil {
		fmt.Fprintln(stderr, "invalid runtime UID")
		return 2
	}
	expectedUID := uint32(uid)
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGTERM, syscall.SIGINT)
	defer cancel()
	err = bootstrap.Run(ctx, rpc.Client{Socket: args[0], ExpectedUID: &expectedUID}, args[2], args[3], stdout)
	if err == nil {
		return 0
	}
	if ctx.Err() != nil {
		return 143
	}
	var failed bootstrap.ExitError
	if errors.As(err, &failed) && failed.Code > 0 && failed.Code < 256 {
		return failed.Code
	}
	fmt.Fprintln(stderr, "workflow execution failed")
	return 1
}

func capture(args []string, stdout, stderr io.Writer) int {
	flags := flag.NewFlagSet("contract capture", flag.ContinueOnError)
	flags.SetOutput(stderr)
	endpoint := flags.String("url", "http://127.0.0.1:8765/mcp", "loopback MCP URL")
	tokenPath := flags.String("token-file", "/etc/loki/token", "local bearer token file")
	output := flags.String("output", "", "new snapshot path")
	invalidInputs := flags.Bool("invalid-inputs", false, "capture schema-rejected tool responses")
	if err := flags.Parse(args); err != nil {
		return 2
	}
	if *output == "" || flags.NArg() != 0 {
		fmt.Fprintln(stderr, "capture requires --output PATH")
		return 2
	}
	token, err := os.ReadFile(*tokenPath)
	if err != nil {
		fmt.Fprintln(stderr, "cannot read bearer token file")
		return 1
	}
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	snapshot, err := contract.Capture(ctx, *endpoint, strings.TrimSpace(string(token)), *invalidInputs)
	if err != nil {
		fmt.Fprintln(stderr, err)
		return 1
	}
	data, err := json.MarshalIndent(snapshot, "", "  ")
	if err != nil {
		fmt.Fprintln(stderr, "cannot encode snapshot")
		return 1
	}
	file, err := os.OpenFile(*output, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		fmt.Fprintln(stderr, "cannot create snapshot: output must not exist")
		return 1
	}
	_, err = file.Write(append(data, '\n'))
	if err == nil {
		err = file.Sync()
	}
	closeErr := file.Close()
	if err != nil || closeErr != nil {
		fmt.Fprintln(stderr, "cannot save snapshot")
		return 1
	}
	fmt.Fprintf(stdout, "Captured %s: %d tools, %d resources\n", snapshot.Baseline, len(snapshot.Tools), len(snapshot.Resources))
	return 0
}
