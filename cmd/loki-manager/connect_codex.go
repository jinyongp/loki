package main

import (
	"bytes"
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
	f := flag.NewFlagSet("tools connect", flag.ContinueOnError)
	f.SetOutput(diagnostics)
	workspace := f.String("workspace", "", "absolute project directory on this execution host")
	config := f.String("config", "", "Codex config.toml; defaults to ~/.codex/config.toml")
	remote := f.Bool("remote", false, "launch through the Codex remote executor")
	if err := f.Parse(arguments); err != nil {
		return err
	}
	if f.NArg() != 1 || f.Arg(0) != "codex" {
		return fmt.Errorf("usage: loki tools connect --workspace ABSOLUTE-PROJECT [--config ABSOLUTE-FILE] [--remote] codex")
	}
	state, err := store.Load()
	if err != nil {
		return err
	}
	if state.Config.Mode == tools.ProjectHost {
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
		fmt.Fprintln(out, "Codex tool connection is already configured.")
		return nil
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
	fmt.Fprintln(out, "Codex tool connection configured:", *config)
	fmt.Fprintln(out, "Reopen the Codex project to load", server)
	return nil
}
