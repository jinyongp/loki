package main

import (
	"encoding/json"
	"fmt"
	"io"
	"os"

	"loki/internal/buildinfo"
	windowshost "loki/internal/host/windows"
)

func main() {
	os.Exit(run(os.Args[1:], os.Stdout, os.Stderr))
}

func run(args []string, stdout, stderr io.Writer) int {
	if len(args) == 0 {
		printUsage(stderr)
		return 2
	}
	switch args[0] {
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

func printUsage(stderr io.Writer) {
	fmt.Fprintln(stderr, "usage: loki version [--json] | install [OPTIONS] | status [--json] | doctor [--json] | connection [--json] | connect [COMMAND] | update status|prepare|apply [--json] [OPTIONS] | backup [--json] [OPTIONS] | rollback [--json] [OPTIONS] | restore [--json] [OPTIONS] BACKUP_ID | uninstall [OPTIONS]")
}
