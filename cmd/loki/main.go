package main

import (
	"fmt"
	"io"
	"os"

	"loki/internal/buildinfo"
)

func main() {
	if command := toolchainShimCommand(os.Args[0]); command != "" {
		os.Exit(runToolchainShim(command, os.Args[1:], os.Stderr))
	}
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		switch args[0] {
		case "checkpoint":
			return runCheckpoint(args[1:], stdout, stderr)
		case "health":
			return runHealth(args[1:], stderr)
		case "host":
			return runHost(args[1:], stdout, stderr)
		case "secret-process":
			return runSecretProcess(args[1:], stdout, stderr)
		case "secret", "github":
			return runAdministration(args, stdout, stderr)
		case "runner-exec":
			return runRunnerExec(args[1:], stderr)
		case "toolchain":
			return runToolchain(args[1:], stdout, stderr)
		case "migrate-vault":
			return runMigrateVault(args[1:], stdout, stderr)
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

	fmt.Fprintln(stderr, "usage: loki version | host install|status|connection|doctor|backup|restore|rollback|enable|disable|uninstall ... | host update status|prepare|apply [OPTIONS] | github fields|values|app-key ... | migrate-vault import|restore [OPTIONS] | secret-process start|restart [OPTIONS] TARGET")
	return 2
}
