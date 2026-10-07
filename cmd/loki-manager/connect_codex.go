package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	hostconfig "loki/internal/config"
	"loki/internal/management"
	"loki/internal/tools"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const codexBegin = "# BEGIN Loki browser\n"
const codexEnd = "# END Loki browser\n"

func connectCodex(store management.Store, arguments []string, out, diagnostics io.Writer) error {
	return connectCodexOnHost(context.Background(), store, nil, arguments, out, diagnostics)
}

func connectRemoteCodex(ctx context.Context, selected management.ExecutionSelection, arguments []string, out, diagnostics io.Writer) error {
	return connectCodexOnHost(ctx, management.Store{}, &selected, arguments, out, diagnostics)
}

func connectCodexOnHost(ctx context.Context, store management.Store, selected *management.ExecutionSelection, arguments []string, out, diagnostics io.Writer) error {
	f := flag.NewFlagSet("tools connect", flag.ContinueOnError)
	f.SetOutput(diagnostics)
	workspace := f.String("workspace", "", "absolute project directory on this execution host")
	config := f.String("config", "", "Codex config.toml; defaults to ~/.codex/config.toml")
	remote := f.Bool("remote", false, "launch through the Codex remote executor")
	if err := f.Parse(toolArguments(arguments)); err != nil {
		return err
	}
	if f.NArg() != 1 || f.Arg(0) != "codex" {
		return fmt.Errorf("usage: loki tools connect --workspace ABSOLUTE-PROJECT [--config ABSOLUTE-FILE] [--remote] codex")
	}
	var state management.Snapshot
	var err error
	if selected == nil {
		state, err = store.Load()
	} else {
		argv := []string{"_connection-state", "--workspace", *workspace}
		if selected.Root != "" {
			argv = append([]string{"--root", selected.Root}, argv...)
		}
		relay, relayErr := management.RelaySelection(ctx, *selected, argv)
		if relayErr != nil {
			return relayErr
		}
		var response githubRelayOutput
		relay.Stdout, relay.Stderr = &response, diagnostics
		if relayErr := relay.Run(); relayErr != nil {
			return fmt.Errorf("check selected tools before connecting Codex: %w", relayErr)
		}
		var report management.Report
		if response.overflow || json.Unmarshal(response.Bytes(), &report) != nil {
			return fmt.Errorf("invalid execution-host connection report")
		}
		state.Config.Mode = report.Target.Mode
		state.Installed = map[tools.ID]management.Installation{}
		for id, observed := range report.Tools {
			if observed.Installed {
				state.Installed[id] = management.Installation{}
			}
			state.Config.Tools = append(state.Config.Tools, tools.Selection{ID: id, Enabled: observed.Enabled})
		}
	}
	if err != nil {
		return err
	}
	if selected == nil && state.Config.Mode == tools.ProjectHost {
		info, err := os.Stat(*workspace)
		if !filepath.IsAbs(*workspace) || err != nil || !info.IsDir() {
			return fmt.Errorf("Codex browser workspace must be an existing absolute directory")
		}
	}
	installed := false
	for _, selection := range state.Config.Tools {
		if selection.Enabled && (state.Config.Mode == tools.Full || selection.ID == "browser") {
			if _, ok := state.Installed[selection.ID]; ok {
				installed = true
			}
		}
	}
	if !installed {
		return fmt.Errorf("install and enable selected tools before connecting Codex")
	}
	if *config == "" {
		if directory := os.Getenv("CODEX_HOME"); directory != "" {
			*config = filepath.Join(directory, "config.toml")
		} else {
			home, err := os.UserHomeDir()
			if err != nil {
				return err
			}
			*config = filepath.Join(home, ".codex", "config.toml")
		}
	}
	if !filepath.IsAbs(*config) {
		return fmt.Errorf("Codex configuration path must be absolute")
	}
	binary, err := os.Executable()
	if err != nil {
		return err
	}
	binary, err = filepath.EvalSymlinks(binary)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(*config), 0700); err != nil {
		return err
	}
	lock, err := os.OpenFile(*config+".loki-lock", os.O_CREATE|os.O_EXCL|os.O_WRONLY, 0600)
	if err != nil {
		return fmt.Errorf("Codex configuration is locked; inspect %s.loki-lock: %w", *config, err)
	}
	lock.Close()
	defer os.Remove(*config + ".loki-lock")
	var old []byte
	info, err := os.Lstat(*config)
	if err == nil {
		if !info.Mode().IsRegular() || info.Size() > 4<<20 {
			return fmt.Errorf("Codex configuration must be a bounded regular file")
		}
		old, err = os.ReadFile(*config)
	}
	if err != nil && !os.IsNotExist(err) {
		return err
	}
	var block strings.Builder
	server, begin, end := "loki_browser", codexBegin, codexEnd
	launch := []string{"--root", store.Root, "tools", "serve"}
	if selected != nil {
		launch = []string{"--host", selected.Host.Kind, "--remote-command", selected.Command}
		if selected.System {
			launch = append(launch, "--system-socket", selected.Socket)
		}
		if selected.Root != "" {
			launch = append(launch, "--root", selected.Root)
		}
		if selected.Host.Kind == "wsl" {
			launch = append(launch, "--distribution", selected.Host.Distribution)
		}
		if selected.Host.Kind == "ssh" {
			launch = append(launch, "--address", selected.Host.Address)
		}
		if selected.User != "" {
			launch = append(launch, "--remote-user", selected.User)
		}
		launch = append(launch, "tools", "serve")
	}
	if state.Config.Mode == tools.Full {
		server, begin, end = "loki", "# BEGIN Loki tools\n", "# END Loki tools\n"
	} else {
		launch = append(launch, "browser", "--workspace", filepath.Clean(*workspace), "--engine", "both")
	}
	block.WriteString(begin + "[mcp_servers." + server + "]\ncommand = " + strconv.Quote(binary) + "\nargs = [")
	for index, argument := range launch {
		if index > 0 {
			block.WriteString(", ")
		}
		block.WriteString(strconv.Quote(argument))
	}
	block.WriteString("]\nstartup_timeout_sec = 120\ntool_timeout_sec = 120\n")
	if *remote {
		block.WriteString("experimental_environment = \"remote\"\n")
	}
	block.WriteString(end)
	next, err := hostconfig.MergeClientConnection(old, []byte(block.String()), server, begin, end)
	if err != nil {
		return err
	}
	if bytes.Equal(old, next) {
		return success(out, "Codex tool connection is already configured.", map[string]any{"config": *config, "server": server, "changed": false})
	}
	file, err := os.CreateTemp(filepath.Dir(*config), ".loki-codex-*")
	if err != nil {
		return err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(next); err != nil {
		file.Close()
		return err
	}
	if err := file.Sync(); err != nil {
		file.Close()
		return err
	}
	if err := file.Close(); err != nil {
		return err
	}
	current, err := os.ReadFile(*config)
	if os.IsNotExist(err) && len(old) == 0 {
		err = nil
	}
	if err != nil || !bytes.Equal(old, current) {
		return fmt.Errorf("Codex configuration changed during setup; retry")
	}
	if err := os.Rename(file.Name(), *config); err != nil {
		return err
	}
	return success(out, "Codex tool connection configured: "+*config+"\nReopen the Codex project to load "+server, map[string]any{"config": *config, "server": server, "changed": true})
}
