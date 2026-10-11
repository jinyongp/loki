package main

import (
	"fmt"
	"slices"
)

// Require an explicit scope before the router can reach either removal path.
func uninstallArguments(args []string) ([]string, error) {
	if len(args) == 2 && slices.Contains(publicToolNames, args[1]) {
		return []string{"tools", "uninstall", args[1]}, nil
	}
	if len(args) == 2 && args[1] == "--all" {
		return args, nil
	}
	if len(args) == 3 && (args[1] == "--all" && args[2] == "--purge-data" || args[2] == "--all" && args[1] == "--purge-data") {
		return []string{"uninstall", "--all", "--purge-data"}, nil
	}
	return nil, fmt.Errorf("choose what to uninstall: 'loki uninstall TOOL' or 'loki uninstall --all'; the CLI and user data are retained (see 'loki uninstall --help')")
}
