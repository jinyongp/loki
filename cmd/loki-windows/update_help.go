package main

import (
	"fmt"
	"io"
)

func updateHelpRequested(args []string) bool {
	if len(args) == 1 && (args[0] == "--help" || args[0] == "-h" || args[0] == "help") {
		return true
	}
	if len(args) == 2 && (args[1] == "--help" || args[1] == "-h") {
		switch args[0] {
		case "status", "prepare", "apply":
			return true
		}
	}
	return false
}

func printUpdateHelp(output io.Writer) {
	fmt.Fprintln(output, "usage:")
	fmt.Fprintln(output, "  loki update")
	fmt.Fprintln(output, "  loki update status [--distribution NAME] [--json]")
	fmt.Fprintln(output, "  loki update prepare [--distribution NAME] [--json]")
	fmt.Fprintln(output, "  loki update apply [--distribution NAME] [--json] [--approve] [--interrupt-active-jobs]")
	fmt.Fprintln(output)
	fmt.Fprintln(output, "Bare 'loki update' updates the Windows frontend and converges the appliance release.")
	fmt.Fprintln(output, "The status, prepare, and apply subcommands expose the advanced two-phase appliance lifecycle.")
}
