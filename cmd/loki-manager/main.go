// loki-manager is the portable 0.2 management entrypoint. Release packaging
// installs it as loki independently of the Linux appliance worker commands.
package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"loki/internal/management"
	"loki/internal/tools"
	"loki/internal/transport/toolproxy"
	"loki/modules/browser"
	"os"
	"os/signal"
	"path/filepath"
	"slices"
	"strings"
)

func main() {
	ctx, cancel := signal.NotifyContext(context.Background(), shutdownSignals()...)
	defer cancel()
	if err := run(ctx, os.Args[1:], os.Stdout, os.Stderr); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return
		}
		fmt.Fprintln(os.Stderr, "loki:", err)
		os.Exit(1)
	}
}

func run(ctx context.Context, args []string, out, diagnostics io.Writer) error {
	var structured bool
	var err error
	args, structured, err = outputArguments(args)
	if err != nil {
		return err
	}
	flags := flag.NewFlagSet("loki", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	flags.Usage = func() {
		entry, _ := helpFor("")
		printHelp(out, entry)
	}
	root := flags.String("root", "", "0.2 management directory on this execution host")
	hostKind := flags.String("host", "local", "registered host name, or local/wsl/ssh transport")
	hostID := flags.String("host-id", "", "fixed registered host ID")
	distribution := flags.String("distribution", "", "existing WSL distribution")
	address := flags.String("address", "", "SSH destination")
	remoteCommand := flags.String("remote-command", "loki", "manager executable on the execution host")
	remoteUser := flags.String("remote-user", "", "execution user on a WSL host")
	systemSocket := flags.String("system-socket", "", "owned Linux management socket")
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return nil
		}
		return err
	}
	executionArgs := slices.Clone(args[:len(args)-len(flags.Args())])
	args = flags.Args()
	systemArgs := args
	if structured && len(args) > 0 && args[0] == "_system-relay" {
		systemArgs = append(slices.Clone(args), "--json")
	}
	if handled, err := systemCommand(ctx, *root, systemArgs, os.Stdin, out, diagnostics); handled {
		return err
	}
	if len(args) == 0 || args[0] != "_upgrade-apply" {
		if handled, err := contextualHelp(args, out); handled {
			return err
		}
	}
	if len(args) > 0 && !strings.HasPrefix(args[0], "_") {
		if _, exists := helpFor(args[0]); !exists {
			return fmt.Errorf("unknown command %q; run 'loki --help' to list commands", args[0])
		}
	}
	out = &commandOutput{Writer: out, json: structured}
	if len(args) >= 3 && args[0] == "tools" && args[1] == "uninstall" && (args[2] == "--all" || args[2] == "--purge-data") {
		args = append([]string{"uninstall"}, args[2:]...)
	}
	if len(args) > 0 && args[0] == "uninstall" {
		args, err = uninstallArguments(args)
		if err != nil {
			return err
		}
	}
	if structured && len(args) >= 2 && args[0] == "tools" && args[1] == "serve" {
		return fmt.Errorf("tools serve uses MCP protocol output; --json applies to management commands")
	}
	frontendRoot := *root
	if (*hostKind == "wsl" || *hostKind == "ssh") || *systemSocket != "" {
		frontendRoot = ""
	}
	if frontendRoot == "" {
		frontendRoot, err = management.DefaultRoot()
		if err != nil {
			return err
		}
	}
	frontend := management.Store{Root: frontendRoot}
	if len(args) > 0 && args[0] == "_upgrade-apply" {
		return runUpgradeApply(ctx, frontend, args[1:], out, diagnostics)
	}
	if len(args) > 0 && args[0] == "hosts" {
		return runHosts(ctx, frontend, args[1:], out, diagnostics)
	}
	if len(args) >= 1 && args[0] == "_connection-bridge" {
		if len(args) == 2 {
			return runConnectionBridgeFor(ctx, frontend, args[1], diagnostics)
		}
		if len(args) == 1 {
			return runConnectionBridge(ctx, frontend, diagnostics)
		}
		return fmt.Errorf("invalid connection bridge target")
	}
	explicitHost := false
	flags.Visit(func(option *flag.Flag) {
		if option.Name == "host-id" || option.Name == "host" || option.Name == "distribution" || option.Name == "address" || option.Name == "remote-user" || option.Name == "remote-command" || option.Name == "system-socket" {
			explicitHost = true
		}
	})
	var selected *management.ExecutionSelection
	frontendCommand := len(args) > 0 && (args[0] == "version" || args[0] == "upgrade" || args[0] == "install")
	if !explicitHost && !frontendCommand {
		selected, err = frontend.ExecutionSelection()
		if err != nil {
			return err
		}
		if selected != nil {
			*hostKind, *distribution, *address = selected.Host.Kind, selected.Host.Distribution, selected.Host.Address
			*remoteCommand = selected.Command
			if selected.Remote() {
				*root = selected.Root
			}
		}
	}
	registeredID := *hostID
	if registeredID == "" && *hostKind != "local" && *hostKind != "wsl" && *hostKind != "ssh" {
		registeredID = *hostKind
	}
	if registeredID != "" {
		conflicting := false
		flags.Visit(func(option *flag.Flag) {
			if slices.Contains([]string{"distribution", "address", "remote-user", "remote-command", "system-socket"}, option.Name) {
				conflicting = true
			}
		})
		if conflicting {
			return fmt.Errorf("a registered host cannot be combined with raw transport options")
		}
		record, lookupErr := frontend.RegisteredHost(registeredID)
		if lookupErr != nil {
			return lookupErr
		}
		selected = &record.Target
		registeredID = record.ID
		*hostKind, *distribution, *address = selected.Host.Kind, selected.Host.Distribution, selected.Host.Address
		*remoteCommand, *remoteUser = selected.Command, selected.User
		if selected.Remote() {
			*root = selected.Root
		}
	}
	host := tools.Host{Kind: *hostKind, Distribution: *distribution, Address: *address}
	if err := host.Validate(); err != nil {
		return err
	}
	if explicitHost && registeredID == "" {
		selected = &management.ExecutionSelection{Schema: 1, Host: host, Root: *root, Command: *remoteCommand, User: *remoteUser}
		if *systemSocket != "" {
			selected.System, selected.Owned, selected.Socket = true, true, *systemSocket
		}
		if host.Kind == "local" && !selected.System {
			selected.Root = ""
		}
		if err := selected.Validate(); err != nil {
			return err
		}
	}
	if registeredID != "" && len(args) > 0 && slices.Contains([]string{"hosts", "install"}, args[0]) {
		return fmt.Errorf("this command operates on frontend configuration; do not select a target")
	}
	if len(args) > 0 && (args[0] == "status" || args[0] == "doctor") && slices.Contains(args[1:], "--all-hosts") {
		if len(args) != 2 {
			return fmt.Errorf("usage: loki %s --all-hosts", args[0])
		}
		if explicitHost {
			return fmt.Errorf("choose --host or --all-hosts")
		}
		return statusAllHosts(ctx, frontend, out, diagnostics, args[0] == "doctor")
	}
	if len(args) > 0 && args[0] == "upgrade" && slices.Contains(args[1:], "--all-hosts") {
		if explicitHost {
			return fmt.Errorf("choose --host or --all-hosts")
		}
		return upgradeAllHosts(ctx, frontend, args[1:], os.Stdin, out, diagnostics)
	}
	if len(args) > 0 && args[0] == "upgrade" {
		deps := defaultUpgradeDependencies()
		deps.plan = func(ctx context.Context, store management.Store, target string, diagnostics io.Writer) (*upgradeToolsPlan, error) {
			plan, err := planUpgradeTools(ctx, store, selected, false, target, diagnostics, deps.client)
			if plan != nil && registeredID != "" {
				plan.HostID = registeredID
			}
			return plan, err
		}
		return runUpgrade(ctx, frontend, args[1:], os.Stdin, out, diagnostics, deps)
	}
	prepareSelectedHost := func() error {
		if selected == nil || !selected.Remote() {
			return nil
		}
		before := *selected
		prepared, err := ensureSetupExecutionHost(ctx, frontend, before, !explicitHost && registeredID == "", diagnostics)
		if err != nil {
			return err
		}
		if registeredID != "" && prepared != before {
			if err := frontend.UpdateHostTarget(registeredID, before, prepared); err != nil {
				return err
			}
		}
		selected = &prepared
		host = prepared.Host
		*root, *remoteCommand = prepared.Root, prepared.Command
		if explicitHost {
			executionArgs = setupBoundArguments(frontend, prepared)
		}
		return nil
	}
	if len(args) == 2 && args[0] == "tools" && args[1] == "manage" {
		if err := prepareSelectedHost(); err != nil {
			return err
		}
	}
	if len(args) >= 2 && args[0] == "tools" && args[1] == "install" {
		if _, err := fullInstallationRequest(args[2:]); err != nil {
			return err
		}
		if err := prepareSelectedHost(); err != nil {
			return err
		}
	}
	if len(args) >= 2 && args[0] == "tools" && args[1] == "update" {
		if err := validateToolUpdateRequest(args[2:]); err != nil {
			return err
		}
		if err := prepareSelectedHost(); err != nil {
			return err
		}
	}
	if len(args) > 0 && args[0] == "connections" {
		if handled, err := codexConnectionCommand(frontend, selected, args[1:], out); handled {
			return err
		}
		if len(args) >= 3 && args[1] == "setup" && args[2] == "codex" {
			if selected != nil && selected.Remote() {
				return connectCodexOnHost(ctx, frontend, selected, args[2:], out, diagnostics)
			}
			return connectCodex(frontend, args[2:], out, diagnostics)
		}
		connectionArgs := args[1:]
		if selected != nil && selected.Remote() && (len(args) > 2 && args[2] == "openai" || len(args) > 1 && args[1] == "restore") {
			if h, e := frontend.HostForTarget(*selected); e == nil {
				connectionArgs = append(slices.Clone(connectionArgs), "--host-id", h.ID)
			}
		}
		return runConnections(ctx, frontend, connectionArgs, os.Stdin, out, diagnostics)
	}
	if len(args) == 1 && (args[0] == "status" || args[0] == "doctor") || len(args) == 2 && args[0] == "tools" && slices.Contains([]string{"status", "doctor", "list"}, args[1]) {
		localRoot := *root
		if localRoot == "" || selected != nil && selected.Remote() {
			localRoot = frontend.Root
		}
		return operatorStatus(ctx, management.Store{Root: localRoot}, selected, out, diagnostics, args[len(args)-1] == "doctor")
	}
	if len(args) == 2 && args[0] == "tools" && args[1] == "manage" {
		return runToolsMenu(ctx, executionArgs, setupMenuHost{frontend, selected, explicitHost}, os.Stdin, out, diagnostics)
	}
	if len(args) >= 2 && args[0] == "tools" && args[1] == "connect" {
		if selected != nil && selected.Remote() {
			return connectCodexOnHost(ctx, frontend, selected, args[2:], out, diagnostics)
		}
		localRoot := *root
		if localRoot == "" {
			localRoot = frontend.Root
		}
		return connectCodex(management.Store{Root: localRoot}, args[2:], out, diagnostics)
	}
	if len(args) > 0 && (args[0] == "uninstall" || len(args) >= 2 && args[0] == "tools" && (args[1] == "uninstall" || args[1] == "remove")) {
		fmt.Fprintln(diagnostics, "Uninstall target:", setupHostLabel(selected))
	}
	if len(args) > 0 && args[0] == "setup" {
		if len(args) == 1 {
			return runSetupMenu(ctx, executionArgs, setupMenuHost{frontend, selected, explicitHost}, os.Stdin, out, diagnostics)
		}
		options, err := parseSetup(ctx, args[1:], os.Stdin, diagnostics)
		if err != nil {
			return err
		}
		if len(options.selected) == 0 {
			return runSetup(ctx, frontend, options, os.Stdin, out, diagnostics)
		}
		if err := prepareSelectedHost(); err != nil {
			return err
		}
		if !explicitHost && !localAdministrator() && host.Kind == "local" && (selected == nil || !selected.System) && options.mode == tools.Full {
			selected, err = selectFullSetupHost(ctx, frontend, os.Stdin, diagnostics)
			if err != nil {
				return err
			}
			if selected != nil {
				host = selected.Host
				*root, *remoteCommand = selected.Root, selected.Command
			}
		}
		if host.Kind != "local" || selected != nil && selected.Remote() {
			if selected == nil {
				selected = &management.ExecutionSelection{Schema: 1, Host: host, Root: *root, Command: *remoteCommand}
			}
			return runRemoteSetup(ctx, frontend, *selected, options, os.Stdin, out, diagnostics)
		}
		return runSetup(ctx, frontend, options, os.Stdin, out, diagnostics)
	}
	if !explicitHost && !localAdministrator() && host.Kind == "local" && (selected == nil || !selected.System) && len(args) >= 2 && args[0] == "tools" && args[1] == "install" {
		full, err := fullInstallationRequest(args[2:])
		if err != nil {
			return err
		}
		if full {
			selected, err = selectFullSetupHost(ctx, frontend, os.Stdin, diagnostics)
			if err != nil {
				return err
			}
			if selected != nil {
				host = selected.Host
				*root, *remoteCommand = selected.Root, selected.Command
			}
		}
	}
	if host.Kind != "local" || selected != nil && selected.Remote() {
		if selected == nil {
			selected = &management.ExecutionSelection{Schema: 1, Host: host, Root: *root, Command: *remoteCommand}
		}
		if len(args) >= 2 && args[0] == "tools" && args[1] == "install" {
			full, err := fullInstallationRequest(args[2:])
			if err != nil {
				return err
			}
			if full {
				if err := prepareRemoteFullHost(ctx, *selected, os.Stdin, diagnostics); err != nil {
					return err
				}
			} else if err := prepareRemoteBrowserHost(ctx, *selected, os.Stdin, diagnostics, defaultRemotePreparationDependencies().run); err != nil {
				return err
			}
		}
		if len(args) >= 2 && args[0] == "tools" && args[1] == "update" {
			if err := prepareInstalledBrowserHost(ctx, setupMenuHost{frontend: frontend, selected: selected}, os.Stdin, diagnostics); err != nil {
				return err
			}
		}
		if remoteGitHubWizard(args) {
			return runSelectedGitHubWizard(ctx, *selected, args, os.Stdin, out, diagnostics)
		}
		remoteArgs := args
		if len(args) >= 2 && args[0] == "tools" && (args[1] == "install" || args[1] == "update") {
			explicitRelease := false
			for _, arg := range args[2:] {
				name, _, _ := strings.Cut(strings.TrimLeft(arg, "-"), "=")
				if name == "version" || name == "catalog" {
					explicitRelease = true
				}
			}
			if !explicitRelease {
				remoteArgs = append(append(slices.Clone(args[:2]), "--version", management.ManagerRelease), args[2:]...)
			}
		}
		remoteArgs, relayInput, cleanup, err := remoteIntegrationInput(remoteArgs, os.Stdin)
		if err != nil {
			return err
		}
		defer cleanup()
		if *root != "" {
			remoteArgs = append([]string{"--root", *root}, remoteArgs...)
		}
		if structured {
			remoteArgs = append([]string{"--json"}, remoteArgs...)
		}
		relay, err := management.RelaySelection(ctx, *selected, remoteArgs)
		if err != nil {
			return err
		}
		relay.Stdin = relayInput
		relay.Stdout = out
		relay.Stderr = diagnostics
		return relay.Run()
	}
	if *root == "" {
		var err error
		*root, err = management.DefaultRoot()
		if err != nil {
			return err
		}
	}
	store := management.Store{Root: *root}
	if len(args) == 1 && (args[0] == "_browser-environment" || args[0] == "_prepare-browser-environment") {
		state, err := store.Load()
		if err != nil {
			return err
		}
		if state.Config.Mode == tools.Full {
			return json.NewEncoder(out).Encode(browserEnvironmentReport{Schema: 1, Missing: []string{}})
		}
		if args[0] == "_browser-environment" {
			report, err := inspectBrowserEnvironment(ctx, defaultBrowserEnvironmentDependencies())
			if err != nil {
				return err
			}
			return json.NewEncoder(out).Encode(report)
		}
		password, err := io.ReadAll(io.LimitReader(os.Stdin, 4097))
		defer clear(password)
		if err != nil || len(password) > 4096 {
			return fmt.Errorf("invalid bounded browser administrator input")
		}
		return prepareBrowserEnvironment(ctx, password, diagnostics, defaultBrowserEnvironmentDependencies())
	}
	if len(args) > 0 && slices.Contains([]string{"backup", "backups", "restore", "rollback", "uninstall"}, args[0]) {
		return runMaintenance(ctx, store, args, os.Stdin, out, diagnostics)
	}
	if len(args) > 0 && args[0] == "_connection-state" {
		f := flag.NewFlagSet("_connection-state", flag.ContinueOnError)
		workspace := f.String("workspace", "", "execution-host project")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if f.NArg() != 0 {
			return fmt.Errorf("invalid connection probe")
		}
		report, err := store.Status()
		if err != nil {
			return err
		}
		if report.Target.Mode == tools.ProjectHost && *workspace != "" {
			info, err := os.Stat(*workspace)
			if !filepath.IsAbs(*workspace) || err != nil || !info.IsDir() {
				return fmt.Errorf("browser connection needs an existing absolute workspace on the execution host")
			}
		}
		return json.NewEncoder(out).Encode(report)
	}
	if len(args) == 1 && args[0] == "_prepare-environment" {
		return management.PrepareEnvironment(ctx, os.Stdin, diagnostics)
	}
	if len(args) == 1 && args[0] == "_github-setup-relay" {
		return runGitHubSetupRelay(ctx, store, os.Stdin, out, diagnostics)
	}
	if len(args) == 1 && args[0] == "version" {
		if structured {
			return result(out, "Loki", map[string]any{"version": management.ManagerRelease})
		}
		_, err := fmt.Fprintln(out, "loki", management.ManagerRelease)
		return err
	}
	if len(args) > 0 && args[0] == "install" {
		f := flag.NewFlagSet("install", flag.ContinueOnError)
		f.SetOutput(diagnostics)
		binDirectory := f.String("bin-dir", "", "publish the native manager into this existing or new command directory")
		if err := f.Parse(args[1:]); err != nil {
			return err
		}
		if f.NArg() != 0 {
			return fmt.Errorf("usage: loki install [--bin-dir PATH]")
		}
		if *binDirectory != "" {
			source, err := os.Executable()
			if err != nil {
				return err
			}
			source, err = filepath.EvalSymlinks(source)
			if err != nil {
				return err
			}
			record, err := store.InstallManager(ctx, source, *binDirectory)
			if err != nil {
				return err
			}
			return success(out, "Loki management installed: "+record.Executable+"\nAdd its directory to PATH, then run 'loki setup' to choose a host and tools.", map[string]any{"manager": record})
		}
		unlock, err := store.Lock()
		if err != nil {
			return err
		}
		defer unlock()
		if err := store.RequireMutable(); err != nil {
			return err
		}
		state, err := store.Load()
		if err != nil {
			return err
		}
		if err := store.Save(state); err != nil {
			return err
		}
		return success(out, "Loki management installed. Run 'loki setup' to choose a host, tools and client connection.", nil)
	}
	if len(args) == 1 && (args[0] == "status" || args[0] == "doctor") {
		return status(ctx, store, out, diagnostics, args[0] == "doctor")
	}
	if len(args) > 0 && args[0] == "integrations" {
		return runIntegrations(ctx, store, args[1:], os.Stdin, out, diagnostics)
	}
	if len(args) < 2 || args[0] != "tools" {
		return fmt.Errorf("unknown command %q; run 'loki --help' to list commands", args[0])
	}
	switch args[1] {
	case "connect":
		return connectCodex(store, args[2:], out, diagnostics)
	case "start", "stop", "restart":
		if len(args) != 2 {
			return fmt.Errorf("usage: loki tools %s", args[1])
		}
		state, err := store.Load()
		if err != nil {
			return err
		}
		if state.Config.Mode != tools.Full {
			return fmt.Errorf("persistent services use full mode; project-host browser uses loki tools serve")
		}
		if args[1] != "stop" {
			if err := management.PrepareEnvironment(ctx, os.Stdin, diagnostics); err != nil {
				return err
			}
		}
		backend, err := management.NewFullBackend(store, diagnostics)
		if err != nil {
			return err
		}
		if args[1] == "stop" {
			fmt.Fprintln(diagnostics, "Stopping owned full services and their jobs...")
			if err := store.StopFull(ctx, backend); err != nil {
				return err
			}
			return success(out, "Full services stopped. Owned tool data is retained.", map[string]any{"state": "stopped"})
		}
		if args[1] == "restart" {
			fmt.Fprintln(diagnostics, "Restarting selected full services...")
			if err := store.StopFull(ctx, backend); err != nil {
				return err
			}
		}
		fmt.Fprintln(diagnostics, "Preparing selected full services...")
		report, err := store.ReconcileFull(ctx, backend)
		if encodeErr := result(out, "Full services", report); encodeErr != nil {
			return encodeErr
		}
		return err
	case "topology":
		if len(args) != 2 {
			return fmt.Errorf("usage: loki tools topology")
		}
		topology, err := store.FullTopology()
		if err != nil {
			return err
		}
		return result(out, "Full tool topology", topology)
	case "layouts":
		if len(args) != 2 {
			return fmt.Errorf("usage: loki tools layouts")
		}
		resources, err := store.FullResources()
		if err != nil {
			return err
		}
		layouts, err := resources.Layouts()
		if err != nil {
			return err
		}
		return result(out, "Full tool layouts", layouts)
	case "resources":
		if len(args) != 2 {
			return fmt.Errorf("usage: loki tools resources")
		}
		resources, err := store.FullResources()
		if err != nil {
			return err
		}
		return result(out, "Full tool resources", resources)
	case "plan":
		if len(args) != 2 {
			return fmt.Errorf("usage: loki tools plan")
		}
		plan, err := store.PlanFull()
		if err != nil {
			return err
		}
		return result(out, "Full tool plan", plan)
	case "configure":
		f := flag.NewFlagSet("tools configure", flag.ContinueOnError)
		f.SetOutput(diagnostics)
		mode := f.String("mode", "", "runtime mode: project-host or full (Linux execution host)")
		if err := f.Parse(args[2:]); err != nil {
			return err
		}
		if f.NArg() != 0 || *mode == "" {
			return fmt.Errorf("choose an execution mode with --mode project-host or --mode full; see 'loki tools configure --help'")
		}
		if err := store.ConfigureMode(tools.Mode(*mode)); err != nil {
			return err
		}
		return success(out, "Runtime mode: "+*mode, map[string]any{"mode": *mode})
	case "list", "status", "doctor":
		if len(args) != 2 {
			return fmt.Errorf("usage: loki tools %s", args[1])
		}
		return status(ctx, store, out, diagnostics, args[1] == "doctor")
	case "recover":
		if len(args) != 2 {
			return fmt.Errorf("usage: loki tools recover")
		}
		if err := store.Recover(); err != nil {
			return err
		}
		return success(out, "Tool recovery completed.", nil)
	case "install", "update":
		f := flag.NewFlagSet("tools "+args[1], flag.ContinueOnError)
		f.SetOutput(diagnostics)
		catalog := f.String("catalog", "", "trusted release artifact catalog file")
		version := f.String("version", "", "stable tool release (default: this CLI release)")
		archives := f.String("archives", "", "optional absolute directory of trusted receipt-bound local archives")
		if err := f.Parse(toolArguments(args[2:])); err != nil {
			return err
		}
		if (args[1] == "install" && f.NArg() == 0) || (args[1] == "update" && f.NArg() != 0) {
			if args[1] == "install" {
				return fmt.Errorf("select at least one tool; see 'loki tools install --help'")
			}
			return fmt.Errorf("tools update updates installed tools without tool names; see 'loki tools update --help'")
		}
		if *archives != "" && !filepath.IsAbs(*archives) {
			return fmt.Errorf("--archives requires an absolute execution-host directory")
		}
		store.ArchiveDirectory = *archives
		state, err := store.Load()
		if err != nil {
			return err
		}
		mode := state.Config.Mode
		if *catalog == "" && args[1] == "install" {
			for _, name := range f.Args() {
				if !slices.Contains(publicToolNames, name) {
					return fmt.Errorf("unknown tool %q; run loki tools --help", name)
				}
			}
			if slices.ContainsFunc(f.Args(), func(name string) bool { return name != "browser" }) {
				mode = tools.Full
			}
		}
		release, err := acquireCatalogForMode(ctx, mode, *catalog, *version, diagnostics, defaultUpgradeDependencies().client)
		if err != nil {
			return err
		}
		_, browserInstalled := state.Installed["browser"]
		prepareBrowser := slices.Contains(f.Args(), "browser")
		if mode == tools.ProjectHost && args[1] == "update" && browserInstalled {
			before, err := store.Status()
			if err != nil {
				return err
			}
			changes, err := changedUpgradeTools(before, release)
			if err != nil {
				return err
			}
			prepareBrowser = slices.ContainsFunc(changes, func(change upgradeToolChange) bool { return change.ID == "browser" })
		}
		if mode == tools.ProjectHost && prepareBrowser {
			if err := prepareLocalBrowserHost(ctx, os.Stdin, diagnostics); err != nil {
				return err
			}
		}
		if args[1] == "update" {
			updated, updateErr := updateInstalledTools(ctx, store, release, diagnostics)
			scopeUpgradeHealth(store, nil, updated.Health)
			message := "Installed tools are up to date."
			if updated.Changed {
				message = "Updated all installed tools to " + release.Release + "; activation and data are preserved."
			}
			if updated.Reconnect {
				message += "\nReconnect the client's Loki MCP server to load the new programs."
			}
			if updateErr != nil {
				message = "Tool update needs attention; see the automatic health report and error."
				if jsonOutput(out) {
					if err := json.NewEncoder(out).Encode(updated); err != nil {
						return err
					}
				} else {
					fmt.Fprintln(out, message)
					printUpgradeHealth(out, updated.Health)
				}
				return updateErr
			}
			if err := success(out, message, map[string]any{"release": updated.Release, "changed": updated.Changed, "services_restarted": updated.ServicesRestarted, "reconnect_required": updated.Reconnect, "health": updated.Health}); err != nil {
				return err
			}
			if !jsonOutput(out) {
				printUpgradeHealth(out, updated.Health)
			}
			return updateErr
		}
		selected := make([]tools.ID, 0, f.NArg())
		for _, id := range f.Args() {
			selected = append(selected, tools.ID(id))
		}
		if err := store.InstallToolsForMode(ctx, release, selected, mode, diagnostics); err != nil {
			return err
		}
		if slices.Contains(selected, tools.ID("browser")) {
			if err := verifyInstalledBrowser(ctx, store, diagnostics); err != nil {
				return err
			}
		}
		return success(out, "Installed "+strings.Join(f.Args(), ", ")+" with private prerequisites (activation is separate).", map[string]any{"tools": selected, "release": release.Release})
	case "enable", "disable":
		f := flag.NewFlagSet("tools enable", flag.ContinueOnError)
		f.SetOutput(diagnostics)
		capabilities := f.String("capabilities", "", "explicit optional capability names, comma-separated")
		noStart := f.Bool("no-start", false, "save activation without changing running services")
		if err := f.Parse(toolArguments(args[2:])); err != nil {
			return err
		}
		if f.NArg() != 1 {
			return fmt.Errorf("select exactly one installed tool; see 'loki tools %s --help'", args[1])
		}
		var caps []string
		f.Visit(func(option *flag.Flag) {
			if option.Name == "capabilities" {
				caps = []string{}
				if *capabilities != "" {
					caps = strings.Split(*capabilities, ",")
				}
			}
		})
		if err := store.SetEnabled(tools.ID(f.Arg(0)), args[1] == "enable", caps); err != nil {
			return err
		}
		if !*noStart {
			if err := reconcileToolActivation(ctx, store, diagnostics); err != nil {
				if jsonOutput(out) {
					if encodeErr := result(out, "Tool activation", map[string]any{"success": false, "tool": f.Arg(0), "enabled": args[1] == "enable", "activation_saved": true, "services_applied": false}); encodeErr != nil {
						return encodeErr
					}
				}
				return fmt.Errorf("%s activation was saved, but services could not apply it; run 'loki doctor', then 'loki tools start' (GitHub authentication: 'loki integrations setup github'): %w", f.Arg(0), err)
			}
		}
		message := "Tool " + f.Arg(0) + " " + args[1] + "d."
		if f.Arg(0) == "browser" {
			if caps != nil {
				message += " Optional feature list replaced. Reconnect the client's Loki MCP server to load changes."
			}
			message += "\nChoose recording, PDF and other features with 'loki setup' > Manage tools > browser > Optional features."
		}
		if *noStart {
			message += " Activation saved; running services are unchanged."
		}
		return success(out, message, map[string]any{"tool": f.Arg(0), "enabled": args[1] == "enable", "activation_saved": true, "services_applied": !*noStart})
	case "remove", "uninstall":
		if len(args) != 3 {
			return fmt.Errorf("select exactly one tool to remove; see 'loki tools remove --help'")
		}
		if err := uninstallTool(ctx, store, tools.ID(args[2]), diagnostics); err != nil {
			return err
		}
		return success(out, "Tool "+args[2]+" programs removed. User data, credentials and the CLI are retained.", map[string]any{"tool": args[2], "data_retained": true})
	case "prune":
		f := flag.NewFlagSet("tools prune", flag.ContinueOnError)
		f.SetOutput(diagnostics)
		keep := f.Int("keep", 1, "additional inactive program generations to retain")
		if err := f.Parse(toolArguments(args[2:])); err != nil {
			return err
		}
		if f.NArg() != 1 {
			return fmt.Errorf("usage: loki tools prune TOOL --keep COUNT")
		}
		report, err := store.Prune(ctx, tools.ID(f.Arg(0)), *keep, diagnostics)
		if encodeErr := result(out, "Tool generation cleanup", report); encodeErr != nil {
			return encodeErr
		}
		return err
	case "serve":
		return serve(ctx, store, args[2:], diagnostics)
	default:
		return fmt.Errorf("unknown tools command %q; run 'loki tools --help' to list commands", args[1])
	}
}

func status(ctx context.Context, store management.Store, out, diagnostics io.Writer, doctor bool) error {
	var report management.Report
	var err error
	if doctor {
		fmt.Fprintln(diagnostics, "Checking installed tool resources...")
		state, loadErr := store.Load()
		if loadErr != nil {
			return loadErr
		}
		probes := map[tools.ID]management.Probe{}
		if state.Config.Mode == tools.ProjectHost {
			probes["browser"] = func(ctx context.Context, generation string) error {
				fmt.Fprintln(diagnostics, "Checking managed Chrome startup, sandbox and version (up to 45 seconds)...")
				return browser.CheckRuntime(ctx, generation)
			}
		} else {
			backend, backendErr := management.NewFullBackend(store, diagnostics)
			probes, err = store.FullProbes(backend)
			if err != nil {
				return err
			}
			if backendErr != nil {
				for id := range probes {
					probes[id] = func(context.Context, string) error { return backendErr }
				}
			}
		}
		report, err = store.Doctor(ctx, probes)
	} else {
		report, err = store.Status()
	}
	if err != nil {
		return err
	}
	if err := result(out, "Loki", report); err != nil {
		return err
	}
	if report.Healthy != nil && !*report.Healthy {
		return fmt.Errorf("doctor found unhealthy resources; see reported issues")
	}
	if report.Ready != nil && !*report.Ready {
		return fmt.Errorf("selected tools are not verified ready; inspect their reported readiness")
	}
	return nil
}

func serve(ctx context.Context, store management.Store, args []string, diagnostics io.Writer) error {
	f := flag.NewFlagSet("tools serve", flag.ContinueOnError)
	f.SetOutput(diagnostics)
	engine := f.String("engine", "playwright", "official browser engine: playwright, devtools or both")
	workspace := f.String("workspace", "", "project workspace on this execution host")
	if err := f.Parse(toolArguments(args)); err != nil {
		return err
	}
	if f.NArg() > 1 || f.NArg() == 1 && f.Arg(0) != "browser" {
		return fmt.Errorf("use 'loki tools serve' for enabled tools; see 'loki tools serve --help'")
	}
	state, err := store.Load()
	if err != nil {
		return err
	}
	if f.NArg() == 0 && state.Config.Mode == tools.Full {
		if *workspace != "" {
			return fmt.Errorf("--workspace applies to the project-host browser; full tools use managed workspaces")
		}
		return serveFull(ctx, store, diagnostics)
	}
	if state.Config.Host.Kind != "local" || state.Config.Mode != tools.ProjectHost {
		return fmt.Errorf("this launcher requires project-host configuration on the local execution host")
	}
	installation, exists := state.Installed["browser"]
	if !exists {
		return fmt.Errorf("browser is not installed")
	}
	var caps []string
	enabled := false
	for _, selection := range state.Config.Tools {
		if selection.ID == "browser" {
			caps = selection.Capabilities
			enabled = selection.Enabled
		}
	}
	if !enabled {
		return fmt.Errorf("browser is disabled; run loki tools enable browser")
	}
	if *workspace == "" {
		// An unscoped client connection must not inherit the launcher's working
		// directory as a project-file grant.
		directory, err := os.MkdirTemp("", "loki-browser-workspace-")
		if err != nil {
			return err
		}
		defer os.RemoveAll(directory)
		*workspace = directory
	}
	generation, err := store.Generation(installation.Artifact)
	if err != nil {
		return err
	}
	release, err := store.Lease(installation.Artifact)
	if err != nil {
		return err
	}
	defer release()
	authorize := func(name string) error {
		current, err := store.Load()
		if err != nil {
			return err
		}
		active, exists := current.Installed["browser"]
		if !exists || active.Artifact.Identity() != installation.Artifact.Identity() {
			return fmt.Errorf("browser installation changed; reconnect this MCP server")
		}
		for _, selection := range current.Config.Tools {
			if selection.ID == "browser" && selection.Enabled {
				if !slices.Equal(selection.Capabilities, caps) {
					return fmt.Errorf("browser capabilities changed; reconnect this MCP server")
				}
				if browser.Allowed(name, selection.Capabilities) {
					return nil
				}
			}
		}
		return fmt.Errorf("browser tool %q is disabled or needs an explicit optional capability", name)
	}
	revision := func() string {
		current, err := store.Load()
		if err != nil {
			return "unavailable"
		}
		encoded, err := json.Marshal(current)
		if err != nil {
			return "invalid"
		}
		return fmt.Sprintf("%x", sha256.Sum256(encoded))
	}
	engines := []string{*engine}
	if *engine == "both" {
		engines = []string{"playwright", "devtools"}
	}
	options := make([]toolproxy.Options, 0, len(engines))
	fmt.Fprintln(diagnostics, "Checking bundled Node, sandboxed Chrome and video dependencies...")
	if err := browser.CheckRuntime(ctx, generation); err != nil {
		return err
	}
	for _, name := range engines {
		fmt.Fprintf(diagnostics, "Preparing %s browser engine...\n", name)
		launch, err := browser.Prepare(ctx, browser.Options{Bundle: generation, Data: filepath.Join(store.Root, "data", "browser"), Workspace: *workspace, Engine: name, RuntimeVerified: true, Capabilities: caps, Stderr: diagnostics})
		if err != nil {
			return err
		}
		defer launch.Cleanup()
		options = append(options, toolproxy.Options{Name: "loki-project-browser-" + name, Owner: "browser/" + name, Version: management.Release, Instructions: "Official project-host browser tools on the execution host. Engines have separate tabs, profiles and login state from each other and from the desktop app's in-app browser. Use these tools when the user selects this browser connection. Files and localhost refer to the execution host. Use loki_browser_files to stage uploads or read owned results, including inline screenshots. Save exported files inside the engine's owned session output directory.", Command: launch.Command, RootURI: launch.RootURI, Results: launch.Output, Authorize: authorize, AuthorizeResource: func() error { return authorize("") }, Stderr: diagnostics, Revision: revision})
	}
	return toolproxy.RunMany(ctx, options)
}

// Tool names may precede their options while global options precede the group.
func toolArguments(args []string) []string {
	var options, positional []string
	for i := 0; i < len(args); i++ {
		arg := args[i]
		if arg == "--" {
			positional = append(positional, args[i+1:]...)
			break
		}
		if !strings.HasPrefix(arg, "-") {
			positional = append(positional, arg)
			continue
		}
		options = append(options, arg)
		name := strings.TrimLeft(arg, "-")
		if !strings.Contains(name, "=") && helpValueOption(arg) && i+1 < len(args) {
			i++
			options = append(options, args[i])
		}
	}
	return append(append(options, "--"), positional...)
}
