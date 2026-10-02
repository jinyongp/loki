package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"loki/internal/buildinfo"
	windowshost "loki/internal/host/windows"
	"loki/internal/progress"
)

func main() {
	os.Exit(run(os.Args[1:], consoleOutputWriter(os.Stdout), consoleOutputWriter(os.Stderr)))
}

func run(args []string, stdout, stderr io.Writer) int {
	args, stderr = progress.CLIArguments(args, stderr)
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	switch args[0] {
	case "--help", "-h", "help":
		if len(args) != 1 {
			printUsage(stderr)
			return 2
		}
		printUsage(stdout)
		return 0
	case "version":
		if len(args) == 1 {
			fmt.Fprintln(stdout, buildinfo.String())
			return 0
		}
		if len(args) == 2 && args[1] == "--json" {
			binding, err := windowshost.CurrentReleaseBinding()
			if err != nil {
				fmt.Fprintln(stderr, err)
				return 1
			}
			payload := struct {
				Version string                     `json:"version"`
				Commit  string                     `json:"commit"`
				Date    string                     `json:"date"`
				Binding windowshost.ReleaseBinding `json:"release_binding"`
			}{
				Version: buildinfo.Version,
				Commit:  buildinfo.Commit,
				Date:    buildinfo.Date,
				Binding: binding,
			}
			if err = json.NewEncoder(stdout).Encode(payload); err != nil {
				fmt.Fprintln(stderr, "cannot encode Windows frontend version")
				return 1
			}
			return 0
		}
		fmt.Fprintln(stderr, "usage: loki version [--json]")
		return 2
	default:
		return runWindowsCommand(args, stdout, stderr)
	}
}

func printUsage(output io.Writer) {
	fmt.Fprint(output, `Usage:
  loki [--verbose] <command> [options]

Global options:
  --verbose    Show detailed progress (before the command)

Commands:
  version      Show the installed frontend version
  install      Install the Loki WSL appliance
  status       Show installation and appliance status
  doctor       Diagnose appliance health
  connection   Manage local and remote MCP connections
  integration  Manage browser, GitHub, and signing integrations
  update       Update the frontend or manage appliance updates
  backup       Create an appliance backup
  rollback     Roll back the appliance lifecycle state
  restore      Restore an appliance backup
  uninstall    Remove the Loki WSL appliance

Examples:
  loki status
  loki update --help
  loki connection --help
`)
}
