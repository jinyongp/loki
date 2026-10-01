package main

import (
	"fmt"
	"io"
)

func integrationHelpRequested(args []string) bool {
	if len(args) == 1 {
		return args[0] == "--help" || args[0] == "-h" || args[0] == "help"
	}
	if len(args) < 2 || (args[len(args)-1] != "--help" && args[len(args)-1] != "-h") {
		return false
	}
	switch args[0] {
	case "list", "status", "doctor", "enable", "disable", "remove":
		return len(args) == 2
	case "setup", "rotate":
		return len(args) == 2 || len(args) == 3 && (args[1] == "signing" || args[1] == "github")
	}
	return false
}

func printIntegrationUsage(output io.Writer, path ...string) {
	if len(path) == 0 {
		fmt.Fprintln(output, "usage:")
		fmt.Fprintln(output, "  loki integration list [--distribution NAME] [--json]")
		fmt.Fprintln(output, "  loki integration status|doctor [--distribution NAME] [--json] NAME")
		fmt.Fprintln(output, "  loki integration enable|disable|remove [--distribution NAME] [--interrupt-active-jobs] NAME")
		fmt.Fprintln(output, "  loki integration setup|rotate signing [--distribution NAME] [--interrupt-active-jobs] [--identity-name NAME] [--identity-email EMAIL] [--key-file PATH]")
		fmt.Fprintln(output, "  loki integration setup github [--account OWNER] [--account-type user|organization] [--no-browser] [--distribution NAME] [--interrupt-active-jobs]")
		fmt.Fprintln(output, "  loki integration setup|rotate github [--distribution NAME] [--interrupt-active-jobs] [--config-file PATH | --app-id ID --account OWNER --account-type TYPE --installation-id ID --repositories LIST] --private-key-file PATH")
		return
	}
	action := path[0]
	switch action {
	case "list":
		fmt.Fprintln(output, "usage: loki integration list [--distribution NAME] [--json]")
	case "status", "doctor":
		fmt.Fprintf(output, "usage: loki integration %s [--distribution NAME] [--json] NAME\n", action)
		fmt.Fprintln(output, "NAME: browser, signing, or github")
	case "enable", "disable", "remove":
		fmt.Fprintf(output, "usage: loki integration %s [--distribution NAME] [--interrupt-active-jobs] NAME\n", action)
		fmt.Fprintln(output, "NAME: browser, signing, or github")
	case "setup", "rotate":
		if len(path) == 1 {
			printIntegrationUsage(output, action, "signing")
			printIntegrationUsage(output, action, "github")
			return
		}
		switch path[1] {
		case "signing":
			fmt.Fprintf(output, "usage: loki integration %s signing [--distribution NAME] [--interrupt-active-jobs] [--identity-name NAME] [--identity-email EMAIL] [--key-file PATH]\n", action)
		case "github":
			if action == "setup" {
				fmt.Fprintln(output, "usage: loki integration setup github [--account OWNER] [--account-type user|organization] [--no-browser] [--distribution NAME] [--interrupt-active-jobs]")
				fmt.Fprintln(output, "Creates an App installable on personal and organization accounts and continues repository selection in the same browser tab.")
				fmt.Fprintln(output, "Detects the account type from --account unless --account-type is supplied.")
				fmt.Fprintln(output, "Use --manual to enter existing App credentials, or supply import files:")
			}
			fmt.Fprintf(output, "usage: loki integration %s github [--distribution NAME] [--interrupt-active-jobs] [--config-file PATH | --app-id ID --account OWNER --account-type TYPE --installation-id ID --repositories LIST] --private-key-file PATH\n", action)
		}
	}
}
