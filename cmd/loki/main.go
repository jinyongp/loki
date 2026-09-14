package main

import (
	"fmt"
	"io"
	"os"

	"loki/internal/buildinfo"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "checkpoint":
			return runCheckpoint(args[1:], stdout, stderr)
		case "secret-process":
			return runSecretProcess(args[1:], stdout, stderr)
		case "secret":
			return runAdministration(args, stdout, stderr)
		}
	}
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
	if len(args) == 1 && (args[0] == "version" || args[0] == "--version") {
		fmt.Fprintln(stdout, buildinfo.String())
		return 0
	}

	fmt.Fprintln(stderr, "usage: loki version | secret-process start|restart [OPTIONS] TARGET")
	return 2
}
