package main

import (
	"fmt"
	"io"
	"strings"
)

type helpEntry struct {
	path, description, usage string
	options, examples, notes []string
	group                    bool
}

var globalHelpOptions = []string{
	"--root PATH              Management directory on the execution host",
	"--host local|wsl|ssh      Override the remembered execution host",
	"--distribution NAME      Existing WSL distribution",
	"--address USER@HOST      SSH destination",
	"--remote-command PATH    Remote manager executable (default: loki)",
	"--remote-user NAME       WSL execution user (default: distribution user)",
	"--json                   Print a structured result (before or after command)",
	"-h, --help               Show help without running the command",
}

var commandHelpEntries = []helpEntry{
	{path: "", description: "Loki — install the CLI, then configure the tools you need.", usage: "loki [HOST OPTIONS] COMMAND", group: true,
		options: globalHelpOptions, examples: []string{"loki setup", "loki status", "loki doctor"},
		notes: []string{"A fresh CLI installation has an empty tool set.", "Run 'loki COMMAND --help' for usage, options and examples.", "Place host options before the command."}},
	{path: "install", description: "Install or publish the native management command.", usage: "loki install [--bin-dir PATH]",
		options: []string{"--bin-dir PATH    Publish the manager into this command directory"}, examples: []string{"loki install --bin-dir /home/user/.local/bin"},
		notes: []string{"Tool installation and client configuration are separate steps."}},
	{path: "version", description: "Show the installed CLI version.", usage: "loki version", examples: []string{"loki version"}},
	{path: "setup", description: "Choose tools and prepare their execution environment.", usage: "loki setup [TOOL...] [OPTIONS]",
		options:  []string{"--tools LIST         Space/comma-separated tool names", "--mode MODE          project-host or full (default: inferred)", "--version VERSION    Stable tool release (default: CLI release)", "--no-start           Install and enable without starting services", "--connect codex      Configure the Codex MCP connection", "--workspace PATH     Absolute project directory for connection", "--catalog FILE       Advanced: reviewed offline catalog"},
		examples: []string{"loki setup", "loki setup workspace git browser", "loki setup browser --workspace /home/user/project --connect codex"}, notes: []string{"With no tool names, setup shows individual choices. Enter cancels.", "Acquires and verifies official release catalogs automatically.", "Full setup prepares the Linux Docker engine and starts selected services."}},
	{path: "upgrade", description: "Check releases and upgrade the CLI after confirmation.", usage: "loki upgrade [OPTIONS]",
		options:  []string{"--version VERSION    Choose a stable release (default: latest)", "--yes, -y            Accept without prompting", "--check              Show versions without installing", "--force              Allow reinstalling or downgrading", "--timeout DURATION   Overall download/install timeout (default: 5m)"},
		examples: []string{"loki upgrade", "loki upgrade --check", "loki upgrade --version 0.2.3 --yes"}, notes: []string{"Shows current and target versions before confirmation. Enter or EOF cancels.", "Updates only the CLI in its current command directory. Installed tools and their settings are retained.", "Downloads official GitHub release assets and verifies SHA-256 before publication."}},
	{path: "status", description: "Show management state and installed tool status.", usage: "loki status", examples: []string{"loki status"}},
	{path: "doctor", description: "Check installation health and selected tool resources.", usage: "loki doctor", examples: []string{"loki doctor"}},
	{path: "backup", description: "Create a consistent backup of selected programs, settings and owned data.", usage: "loki backup", examples: []string{"loki backup"}, notes: []string{"Services pause during copying and resume afterward. Active jobs block maintenance.", "Backups include credentials and stay in private storage on the execution host."}},
	{path: "backups", description: "List or remove retained private backups.", usage: "loki backups COMMAND", group: true, examples: []string{"loki backups list", "loki backups remove BACKUP-ID"}},
	{path: "backups list", description: "List retained backups without exposing their contents.", usage: "loki backups list"},
	{path: "backups remove", description: "Remove one retained private backup.", usage: "loki backups remove BACKUP-ID"},
	{path: "restore", description: "Restore a backup, retaining current data in a safety backup first.", usage: "loki restore BACKUP-ID", examples: []string{"loki backups list", "loki restore BACKUP-ID"}, notes: []string{"Restores the same execution host and management root. Interrupted restores resume with the same ID."}},
	{path: "rollback", description: "Restore the previous complete tool installation while keeping data.", usage: "loki rollback", examples: []string{"loki rollback"}, notes: []string{"Previous program generations must still exist. Use upgrade --version VERSION --force to change the CLI itself."}},
	{path: "uninstall", description: "Remove selected tool programs while retaining the CLI and user data.", usage: "loki uninstall [--purge-data]", options: []string{"--purge-data  Also remove tool-owned data and integration credentials; retain backups"}, examples: []string{"loki uninstall", "loki uninstall --purge-data"}},
	{path: "connections", description: "Configure client connections and managed OpenAI tunnels.", usage: "loki connections COMMAND", group: true, examples: []string{"loki connections list", "loki connections setup codex", "loki connections setup openai"}},
	{path: "connections list", description: "List available connection providers.", usage: "loki connections list"},
	{path: "connections setup", description: "Configure a Codex client or OpenAI tunnel.", usage: "loki connections setup codex|openai [OPTIONS]", group: true},
	{path: "connections setup codex", description: "Configure Codex on this frontend for the selected execution host.", usage: "loki connections setup codex [--workspace PATH] [--config FILE] [--remote]", options: []string{"--workspace PATH  Project directory on the execution host (browser only)", "--config FILE     Local Codex config.toml", "--remote          Use the Codex remote executor"}},
	{path: "connections setup openai", description: "Configure an OpenAI Secure MCP Tunnel on Windows.", usage: "loki connections setup openai --tunnel-id ID [--runtime-key-stdin]", options: []string{"--tunnel-id ID          OpenAI tunnel ID", "--runtime-key-stdin    Read the runtime key from stdin instead of hidden terminal input"}, notes: []string{"Runtime credentials stay in Windows Credential Manager. Helpers are downloaded and verified only when this provider is used.", "The owned logon task restores enabled connections and WSL keepalive without opening a terminal window."}},
	{path: "connections start", description: "Start a configured managed connection and restore its execution host.", usage: "loki connections start openai"},
	{path: "connections stop", description: "Stop a managed connection and disable automatic restoration.", usage: "loki connections stop openai"},
	{path: "connections remove", description: "Remove managed connection configuration and credentials.", usage: "loki connections remove openai"},
	{path: "connections status", description: "Inspect managed connection state and health.", usage: "loki connections status openai"},
	{path: "connections show", description: "Inspect managed connection state and health.", usage: "loki connections show openai"},
	{path: "connections doctor", description: "Check managed connection authorization and readiness.", usage: "loki connections doctor openai"},
	{path: "connections restore", description: "Restore enabled managed connections after login.", usage: "loki connections restore"},
	{path: "hosts", description: "Select and remember where tools execute.", usage: "loki hosts COMMAND", group: true, examples: []string{"loki hosts show", "loki hosts use wsl --distribution Ubuntu"}},
	{path: "hosts show", description: "Show the selected execution host.", usage: "loki hosts show", examples: []string{"loki hosts show"}},
	{path: "hosts prepare", description: "Prepare and remember a managed Linux, WSL or SSH execution host.", usage: "loki hosts prepare local|wsl|ssh [OPTIONS]", options: []string{"--distribution loki-tools  Dedicated managed WSL host", "--address USER@HOST        Existing SSH access to a Linux host", "--root PATH                Optional remote management directory"}, examples: []string{"loki hosts prepare local", "loki hosts prepare wsl --distribution loki-tools", "loki hosts prepare ssh --address user@host"}},
	{path: "hosts remove", description: "Detach the selected execution host, or remove its owned host and data.", usage: "loki hosts remove [--purge]", options: []string{"--purge  Remove this installation's owned WSL or Linux system host and its data"}, notes: []string{"External SSH and user-managed WSL hosts can be detached; they are never deleted.", "Shared Docker/WSL prerequisites and the frontend CLI are retained."}},
	{path: "hosts use", description: "Check and remember a local, WSL or SSH execution host.", usage: "loki hosts use local|wsl|ssh [OPTIONS]",
		options:  []string{"--distribution NAME    Existing WSL distribution", "--address USER@HOST    SSH destination", "--root PATH            Management directory on the remote host", "--command PATH         Remote manager executable (default: loki)", "--user NAME            WSL execution user"},
		examples: []string{"loki hosts use local", "loki hosts use wsl --distribution Ubuntu", "loki hosts use ssh --address user@host"}, notes: []string{"Subsequent status, setup, tools and integrations commands use this selection.", "CLI version and upgrade remain on the frontend; explicit global --host overrides the remembered host."}},
	{path: "tools", description: "Configure, install and run individual tool groups.", usage: "loki tools COMMAND", group: true,
		examples: []string{"loki setup workspace git browser", "loki tools install github", "loki tools enable github"},
		notes:    []string{"Tool groups: browser, workspace, execution, git, github, secrets, sharing, coordination.", "Setup installs, enables and prepares selected tools. Install alone keeps activation separate.", "Official release catalogs are acquired and verified automatically."}},
	{path: "tools configure", description: "Choose the execution mode for this host.", usage: "loki tools configure --mode MODE",
		options: []string{"--mode project-host|full    Execution mode (required)"}, examples: []string{"loki tools configure --mode full"}, notes: []string{"Full mode requires Linux and Docker Engine. project-host supports the standalone browser."}},
	{path: "tools list", description: "List installed tools and their selection state.", usage: "loki tools list", examples: []string{"loki tools list"}},
	{path: "tools status", description: "Show selected tools and runtime readiness.", usage: "loki tools status", examples: []string{"loki tools status"}},
	{path: "tools doctor", description: "Check selected tools and report resource problems.", usage: "loki tools doctor", examples: []string{"loki tools doctor"}},
	{path: "tools install", description: "Download and install the tool groups you choose.", usage: "loki tools install TOOL... [OPTIONS]",
		options:  []string{"--version VERSION    Stable tool release (default: CLI release)", "--catalog FILE       Advanced: reviewed offline catalog", "--archives PATH      Directory of verified offline module archives"},
		examples: []string{"loki tools install workspace git", "loki tools install browser"}, notes: []string{"Official catalogs and checksums are acquired automatically.", "Installing a tool leaves activation to 'loki tools enable TOOL'. Use 'loki setup' for guided installation and activation."}},
	{path: "tools update", description: "Update installed tools from a trusted release.", usage: "loki tools update [OPTIONS]",
		options: []string{"--version VERSION    Stable tool release (default: CLI release)", "--catalog FILE       Advanced: reviewed offline catalog", "--archives PATH      Directory of verified offline module archives"}, examples: []string{"loki tools update", "loki tools update --version 0.2.5"}},
	{path: "tools enable", description: "Enable an installed tool and optional capabilities.", usage: "loki tools enable TOOL [--capabilities LIST]",
		options: []string{"--capabilities LIST    Comma-separated capability names; an empty value clears them"}, examples: []string{"loki tools enable git"}},
	{path: "tools disable", description: "Disable a tool while keeping its installed files.", usage: "loki tools disable TOOL [--capabilities LIST]",
		options: []string{"--capabilities LIST    Replace optional capabilities with this comma-separated list"}, examples: []string{"loki tools disable git"}},
	{path: "tools remove", description: "Remove an installed tool group.", usage: "loki tools remove TOOL", examples: []string{"loki tools remove browser"}},
	{path: "tools prune", description: "Remove unused generations of an installed tool.", usage: "loki tools prune TOOL [--keep COUNT]",
		options: []string{"--keep COUNT    Extra inactive generations to retain (default: 1)"}, examples: []string{"loki tools prune browser --keep 1"}},
	{path: "tools recover", description: "Recover interrupted tool management operations.", usage: "loki tools recover", examples: []string{"loki tools recover"}},
	{path: "tools start", description: "Start the selected persistent services in full mode.", usage: "loki tools start", examples: []string{"loki tools start"}},
	{path: "tools stop", description: "Stop owned full-mode services and their jobs.", usage: "loki tools stop", examples: []string{"loki tools stop"}},
	{path: "tools restart", description: "Restart selected full-mode services and refresh runtime credentials.", usage: "loki tools restart", examples: []string{"loki tools restart"}},
	{path: "tools serve", description: "Run selected tools as an MCP server over standard input/output.", usage: "loki tools serve\n       loki tools serve browser --workspace PATH [--engine ENGINE]",
		options:  []string{"--workspace PATH    Project directory for the standalone browser (required)", "--engine ENGINE     playwright, devtools or both (default: playwright)"},
		examples: []string{"loki tools serve", "loki tools serve browser --workspace /home/user/project --engine both"}, notes: []string{"Full mode uses 'tools serve' without a tool name. project-host uses 'tools serve browser'.", "Standard output is reserved for MCP protocol messages."}},
	{path: "tools connect", description: "Add the selected tools to your Codex MCP configuration.", usage: "loki tools connect --workspace PATH [OPTIONS] codex",
		options:  []string{"--workspace PATH    Absolute project directory", "--config FILE       Codex config file (default: CODEX_HOME/config.toml or ~/.codex/config.toml)", "--remote            Launch through the Codex remote executor"},
		examples: []string{"loki tools connect --workspace /home/user/project codex", "loki tools connect --workspace /home/user/project --remote codex"}, notes: []string{"Install and enable tools first. Existing unrelated Codex settings are preserved."}},
	{path: "tools plan", description: "Preview the selected full-mode deployment plan.", usage: "loki tools plan", examples: []string{"loki tools plan"}},
	{path: "tools resources", description: "Inspect resources for the selected full-mode deployment.", usage: "loki tools resources", examples: []string{"loki tools resources"}},
	{path: "tools topology", description: "Inspect selected module and service relationships.", usage: "loki tools topology", examples: []string{"loki tools topology"}},
	{path: "tools layouts", description: "Inspect storage and runtime layouts for selected tools.", usage: "loki tools layouts", examples: []string{"loki tools layouts"}},
	{path: "integrations", description: "Configure Git signing and GitHub access for enabled tools.", usage: "loki integrations ACTION git|github", group: true,
		examples: []string{"loki integrations setup git --help", "loki integrations setup github --help"}, notes: []string{"Integrations use enabled git/github tools on a Linux full-mode execution host."}},
	{path: "integrations setup", description: "Configure an integration and its credentials.", usage: "loki integrations setup git|github [OPTIONS]", group: true},
	{path: "integrations status", description: "Show an integration's configuration and readiness.", usage: "loki integrations status git|github", group: true},
	{path: "integrations doctor", description: "Check integration credentials and runtime access.", usage: "loki integrations doctor git|github", group: true},
	{path: "integrations refresh", description: "Refresh cached GitHub installation-token permissions.", usage: "loki integrations refresh github", group: true},
	{path: "integrations setup git", description: "Configure Git identity and protected SSH signing.", usage: "loki integrations setup git [OPTIONS]",
		options:  []string{"--identity-name NAME     Git author/signing name", "--identity-email EMAIL   Git author/signing email", "--key-file FILE          Owner-only unencrypted Ed25519 SSH key", "--key-stdin              Read the SSH private key from standard input"},
		examples: []string{"loki integrations setup git --identity-name \"Your Name\" --identity-email you@example.com"}, notes: []string{"Omit key options to retain an existing key or generate one. Use one key input source."}},
	{path: "integrations setup github", description: "Configure GitHub App access for repositories and organization Projects.", usage: "loki integrations setup github [OPTIONS]",
		options:  []string{"--config-file FILE      Public GitHub App/installation TOML configuration", "--private-key-file FILE Owner-only GitHub App PEM key", "--stdin                 Read config/private_key fields from a private JSON envelope", "--personal-projects     Also authorize optional account-owned Projects via Device Flow", "--no-browser            Print the optional authorization URL instead of opening it"},
		examples: []string{"loki integrations setup github", "loki integrations setup github --config-file /path/to/github.toml --private-key-file /path/to/app.pem"}, notes: []string{"Account-owned Projects are optional. Repository and organization access use App installations."}},
	{path: "integrations status git", description: "Show Git signing identity and configuration state.", usage: "loki integrations status git", examples: []string{"loki integrations status git"}},
	{path: "integrations status github", description: "Show GitHub configuration and authorization state.", usage: "loki integrations status github", examples: []string{"loki integrations status github"}},
	{path: "integrations doctor git", description: "Check protected Git execution and signing access.", usage: "loki integrations doctor git", examples: []string{"loki integrations doctor git"}},
	{path: "integrations doctor github", description: "Check GitHub credentials and runtime access.", usage: "loki integrations doctor github", examples: []string{"loki integrations doctor github"}},
	{path: "integrations refresh github", description: "Refresh installation tokens after GitHub App permission changes.", usage: "loki integrations refresh github", examples: []string{"loki integrations refresh github"}},
	{path: "help", description: "Show help for a command or command group.", usage: "loki help [COMMAND...]", examples: []string{"loki help tools install", "loki help integrations setup github"}},
}

func helpFor(path string) (helpEntry, bool) {
	for _, entry := range commandHelpEntries {
		if entry.path == path {
			return entry, true
		}
	}
	return helpEntry{}, false
}

func printHelp(out io.Writer, entry helpEntry) {
	fmt.Fprintln(out, entry.description)
	fmt.Fprintf(out, "\nUsage: %s\n", entry.usage)
	if entry.group {
		fmt.Fprintln(out, "\nCommands:")
		prefix := entry.path
		if prefix != "" {
			prefix += " "
		}
		for _, child := range commandHelpEntries {
			if child.path == "" || !strings.HasPrefix(child.path, prefix) {
				continue
			}
			name := strings.TrimPrefix(child.path, prefix)
			if name == "" || strings.Contains(name, " ") {
				continue
			}
			fmt.Fprintf(out, "  %-14s %s\n", name, child.description)
		}
	}
	if len(entry.options) > 0 {
		fmt.Fprintln(out, "\nOptions:")
		for _, option := range entry.options {
			fmt.Fprintln(out, "  "+option)
		}
	}
	if len(entry.examples) > 0 {
		fmt.Fprintln(out, "\nExamples:")
		for _, example := range entry.examples {
			fmt.Fprintln(out, "  "+example)
		}
	}
	if len(entry.notes) > 0 {
		fmt.Fprintln(out)
		for _, note := range entry.notes {
			fmt.Fprintln(out, note)
		}
	}
	if entry.path != "" {
		if entry.path == "tools serve" {
			fmt.Fprintln(out, "\nOutput: MCP protocol stream. Management --json does not apply.")
		} else {
			fmt.Fprintln(out, "\nOutput: readable text by default; add --json for a structured result.")
		}
		fmt.Fprintln(out, "\nHost options: loki --help")
	}
}

// Help is resolved before opening state, reading credentials or contacting a
// remote host. Flag values and tokens after -- are never treated as help flags.
func contextualHelp(args []string, out io.Writer) (bool, error) {
	requested := len(args) == 0
	if len(args) > 0 && args[0] == "help" {
		requested = true
		args = args[1:]
	}
	var positionals []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positionals = append(positionals, args[i+1:]...)
			break
		}
		if arg == "--help" || arg == "-h" {
			requested = true
			continue
		}
		if strings.HasPrefix(arg, "-") {
			if !strings.Contains(arg, "=") && helpValueOption(arg) {
				i++
			}
			continue
		}
		positionals = append(positionals, arg)
	}
	path := strings.Join(positionals, " ")
	entry, exact := helpFor(path)
	if !requested {
		if !exact || !entry.group || len(args) != len(positionals) {
			return false, nil
		}
	} else if !exact {
		// Tool names and client names are positional arguments to leaf commands.
		for len(positionals) > 1 {
			positionals = positionals[:len(positionals)-1]
			candidate, exists := helpFor(strings.Join(positionals, " "))
			if exists && !candidate.group {
				entry, exact = candidate, true
				break
			}
		}
		if !exact {
			return true, fmt.Errorf("unknown help topic %q; run 'loki --help' to list commands", path)
		}
	}
	printHelp(out, entry)
	return true, nil
}

func helpValueOption(option string) bool {
	option = "--" + strings.TrimLeft(option, "-")
	switch option {
	case "--root", "--host", "--distribution", "--address", "--remote-command", "--remote-user", "--version", "--timeout", "--mode", "--bin-dir", "--catalog", "--archives", "--capabilities", "--keep", "--workspace", "--engine", "--config", "--config-file", "--private-key-file", "--identity-name", "--identity-email", "--key-file", "--tools", "--connect", "--command", "--tunnel-id", "--user":
		return true
	}
	return false
}
