package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"path/filepath"
	"runtime"
	"slices"
	"strings"

	"loki/internal/management"
	"loki/internal/tools"
)

var publicToolNames = []string{"browser", "workspace", "execution", "git", "github", "secrets", "sharing", "coordination"}

// Read a single bounded line without buffering subsequent wizard input.
func readSetupLine(input io.Reader, limit int) (string, error) {
	var line []byte
	var one [1]byte
	for len(line) <= limit {
		n, err := input.Read(one[:])
		if n > 0 {
			line = append(line, one[0])
			if one[0] == '\n' {
				return string(line), nil
			}
		}
		if err != nil {
			return string(line), err
		}
		if n == 0 {
			return string(line), io.ErrNoProgress
		}
	}
	return "", fmt.Errorf("input exceeds %d bytes", limit)
}

type setupOptions struct {
	selected                            []tools.ID
	mode                                tools.Mode
	catalog, version, workspace, client string
	noStart                             bool
}

func parseSetup(args []string, input io.Reader, diagnostics io.Writer) (setupOptions, error) {
	f := flag.NewFlagSet("setup", flag.ContinueOnError)
	f.SetOutput(diagnostics)
	selection := f.String("tools", "", "comma-separated tool names")
	mode := f.String("mode", "", "project-host or full (default: inferred from tools)")
	catalog := f.String("catalog", "", "optional offline catalog")
	version := f.String("version", "", "stable tool release")
	workspace := f.String("workspace", "", "project directory for client connection")
	client := f.String("connect", "", "connect a client after setup: codex")
	noStart := f.Bool("no-start", false, "install and enable tools without starting services")
	if err := f.Parse(toolArguments(args)); err != nil {
		return setupOptions{}, err
	}
	if *selection != "" && f.NArg() != 0 {
		return setupOptions{}, fmt.Errorf("select tools by name or --tools, not both")
	}
	names := f.Args()
	if *selection != "" {
		names = strings.FieldsFunc(*selection, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
	}
	if len(names) == 0 {
		fmt.Fprintln(diagnostics, "Loki setup — choose the tools you need.")
		fmt.Fprintln(diagnostics, "  browser       Web navigation and screenshots")
		fmt.Fprintln(diagnostics, "  workspace     Managed files and workspaces")
		fmt.Fprintln(diagnostics, "  execution     Confined jobs and commands")
		fmt.Fprintln(diagnostics, "  git           Git operations and optional signing")
		fmt.Fprintln(diagnostics, "  github        GitHub repositories, issues and Projects")
		fmt.Fprintln(diagnostics, "  secrets       Protected application secrets")
		fmt.Fprintln(diagnostics, "  sharing       Managed published endpoints")
		fmt.Fprintln(diagnostics, "  coordination  External task coordination")
		fmt.Fprint(diagnostics, "Tools (space or comma separated; Enter cancels): ")
		line, err := readSetupLine(input, 4096)
		if err != nil && err != io.EOF {
			return setupOptions{}, err
		}
		if len(line) > 4096 {
			return setupOptions{}, fmt.Errorf("tool selection exceeds input limit")
		}
		names = strings.FieldsFunc(line, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' || r == '\r' || r == '\n' })
	}
	options := setupOptions{catalog: *catalog, version: *version, workspace: *workspace, client: *client, noStart: *noStart, mode: tools.Mode(*mode)}
	for _, name := range names {
		if !slices.Contains(publicToolNames, name) {
			return options, fmt.Errorf("unknown tool %q; choose from %s", name, strings.Join(publicToolNames, ", "))
		}
		id := tools.ID(name)
		if slices.Contains(options.selected, id) {
			return options, fmt.Errorf("duplicate tool %q", name)
		}
		options.selected = append(options.selected, id)
	}
	if options.mode == "" {
		options.mode = tools.ProjectHost
		if runtime.GOOS == "windows" && runtime.GOARCH == "arm64" {
			options.mode = tools.Full
		}
		for _, id := range options.selected {
			if id != "browser" {
				options.mode = tools.Full
			}
		}
	}
	if options.mode != tools.ProjectHost && options.mode != tools.Full {
		return options, fmt.Errorf("mode must be project-host or full")
	}
	if options.mode == tools.ProjectHost && slices.ContainsFunc(options.selected, func(id tools.ID) bool { return id != "browser" }) {
		return options, fmt.Errorf("selected tools require full mode")
	}
	if options.client != "" && options.client != "codex" {
		return options, fmt.Errorf("supported client: codex")
	}
	if options.client != "" && options.mode == tools.ProjectHost && !filepath.IsAbs(options.workspace) {
		return options, fmt.Errorf("--connect requires --workspace with an absolute project path")
	}
	if options.catalog != "" && options.version != "" {
		return options, fmt.Errorf("use --catalog or --version, not both")
	}
	if options.version != "" && !stableVersionPattern.MatchString(options.version) {
		return options, fmt.Errorf("version must be a stable MAJOR.MINOR.PATCH release")
	}
	return options, nil
}

func runSetup(ctx context.Context, store management.Store, options setupOptions, input io.Reader, out, diagnostics io.Writer) error {
	if len(options.selected) == 0 {
		return success(out, "Setup cancelled. CLI and installed tools are retained.", map[string]any{"state": "cancelled"})
	}
	state, err := store.Load()
	if err != nil {
		return err
	}
	if state.Config.Mode == tools.Full && options.mode == tools.ProjectHost {
		options.mode = tools.Full
	}
	catalog, err := acquireCatalogForMode(ctx, options.mode, options.catalog, options.version, diagnostics, defaultUpgradeDependencies().client)
	if err != nil {
		return err
	}
	if err := store.InstallToolsForMode(ctx, catalog, options.selected, options.mode, diagnostics); err != nil {
		return err
	}
	for _, id := range options.selected {
		if err := store.SetEnabled(id, true, nil); err != nil {
			return err
		}
	}
	if options.mode == tools.Full && !options.noStart {
		if err := management.PrepareEnvironment(ctx, input, diagnostics); err != nil {
			return err
		}
		backend, err := management.NewFullBackend(store, diagnostics)
		if err != nil {
			return err
		}
		fmt.Fprintln(diagnostics, "Starting selected tools...")
		if slices.Contains(options.selected, tools.ID("github")) {
			integration, ok := backend.(management.IntegrationBackend)
			if !ok {
				return fmt.Errorf("execution host does not provide protected GitHub setup")
			}
			if err := runGitHubWizard(ctx, store, integration, false, false, input, diagnostics, diagnostics); err != nil {
				return err
			}
		} else if _, err := store.ReconcileFull(ctx, backend); err != nil {
			return err
		}
	}
	if options.client != "" {
		args := []string{"--workspace", options.workspace, options.client}
		if err := connectCodex(store, args, diagnostics, diagnostics); err != nil {
			return err
		}
	}
	if options.noStart && slices.Contains(options.selected, tools.ID("github")) {
		fmt.Fprintln(diagnostics, "GitHub selected. Configure or reuse your App with 'loki integrations setup github'.")
	}
	if options.mode == tools.ProjectHost && options.client == "" {
		fmt.Fprintln(diagnostics, "Connect your project with 'loki tools connect --workspace PATH codex'.")
	}
	return success(out, "Loki setup completed.", map[string]any{"tools": options.selected, "mode": options.mode, "release": catalog.Release, "started": options.mode == tools.Full && !options.noStart})
}
