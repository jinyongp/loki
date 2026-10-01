package main

import (
	"fmt"
	"io"
)

func connectionHelpRequested(args []string) bool {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		return true
	}
	if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
		switch args[0] {
		case "list", "show", "setup", "start", "stop", "remove":
			return true
		}
	}
	return false
}

func printConnectionHelp(output io.Writer, path ...string) {
	if len(path) != 0 {
		action := path[0]
		switch action {
		case "list":
			fmt.Fprintln(output, "usage: loki connection list [--distribution NAME] [--json]")
		case "show":
			fmt.Fprintln(output, "usage: loki connection show [--distribution NAME] [--json] NAME")
			fmt.Fprintln(output, "NAME: local or a managed provider from 'loki connection list'")
		case "setup":
			fmt.Fprintln(output, "usage: loki connection setup [--distribution NAME] [--tunnel-id ID] [--runtime-key-env NAME | --runtime-key-credential TARGET] PROVIDER")
		case "start", "stop", "remove":
			fmt.Fprintf(output, "usage: loki connection %s [--distribution NAME] PROVIDER\n", action)
		}
		return
	}
	fmt.Fprintln(output, "usage:")
	fmt.Fprintln(output, "  loki connection [--distribution NAME] [--json]")
	fmt.Fprintln(output, "  loki connection list [--distribution NAME] [--json]")
	fmt.Fprintln(output, "  loki connection show [--distribution NAME] [--json] NAME")
	fmt.Fprintln(output, "  loki connection setup [OPTIONS] PROVIDER")
	fmt.Fprintln(output, "  loki connection start|stop|remove [--distribution NAME] PROVIDER")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Connection names:")
	fmt.Fprintln(output, "  local       Local loopback MCP endpoint")
	fmt.Fprintln(output, "  PROVIDER    Managed remote connection provider; discover with 'loki connection list'")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Bare 'loki connection' is the same as 'loki connection list'.")
}
