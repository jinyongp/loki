package main

import (
	"context"
	"fmt"
	"io"
	"os"
	"runtime"
	"strings"
	"time"

	"charm.land/huh/v2"
	"golang.org/x/term"

	"loki/internal/management"
)

type setupMenuHost struct {
	frontend management.Store
	selected *management.ExecutionSelection
	explicit bool
}

func setupExecutionArguments(selected management.ExecutionSelection) []string {
	args := []string{"--host", selected.Host.Kind, "--remote-command", selected.Command}
	if selected.Root != "" {
		args = append(args, "--root", selected.Root)
	}
	if selected.Host.Kind == "wsl" {
		args = append(args, "--distribution", selected.Host.Distribution)
	}
	if selected.Host.Kind == "ssh" {
		args = append(args, "--address", selected.Host.Address)
	}
	if selected.User != "" {
		args = append(args, "--remote-user", selected.User)
	}
	if selected.System {
		args = append(args, "--system-socket", selected.Socket)
	}
	return args
}

func setupHostLabel(selected *management.ExecutionSelection) string {
	if selected == nil || selected.Host.Kind == "local" && !selected.System {
		return "Local " + runtime.GOOS + " (native)"
	}
	if selected.System {
		return "Local Linux (managed system host)"
	}
	if selected.Host.Kind == "wsl" {
		return "WSL: " + selected.Host.Distribution
	}
	return "SSH: " + selected.Host.Address
}

func selectFullSetupHost(ctx context.Context, store management.Store, input *os.File, diagnostics io.Writer) (*management.ExecutionSelection, error) {
	selected, changed, err := chooseSetupHost(ctx, store, input, diagnostics, true)
	if err != nil {
		return nil, err
	}
	if !changed {
		return nil, fmt.Errorf("installation cancelled; execution host unchanged")
	}
	return selected, nil
}

// Host preparation is an explicit choice, made before downloads or host changes.
func chooseSetupHost(ctx context.Context, store management.Store, input *os.File, diagnostics io.Writer, full bool) (*management.ExecutionSelection, bool, error) {
	terminal, ok := diagnostics.(*os.File)
	if !ok || !term.IsTerminal(int(input.Fd())) || !term.IsTerminal(int(terminal.Fd())) || os.Getenv("TERM") == "dumb" {
		return nil, false, fmt.Errorf("choose an execution host first with 'loki hosts prepare' or 'loki hosts use'; run 'loki setup' in a terminal for guided host selection")
	}
	choices := []huh.Option[string]{}
	if !full {
		choices = append(choices, huh.NewOption("Local "+runtime.GOOS+" — native browser", "local"))
	}
	if runtime.GOOS == "windows" {
		choices = append(choices, huh.NewOption("Managed WSL: loki-tools — create or reuse Loki's dedicated Linux host", "managed-wsl"), huh.NewOption("Existing WSL distribution — choose by name (prepare CLI automatically)", "wsl"))
	}
	if runtime.GOOS == "linux" {
		choices = append(choices, huh.NewOption("Local Linux — prepare managed services (administrator access)", "system"))
	}
	choices = append(choices, huh.NewOption("SSH Linux host — prepare services using existing SSH access", "ssh"), huh.NewOption("Back", ""))
	choice := ""
	description := "Each host has its own installed tools and data. Switching hosts does not move them."
	if full {
		description = "This tool needs Linux services. Choose where to install them. Existing tools and data stay on their current host."
	}
	form := huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title("Choose execution host").Description(description).Options(choices...).Value(&choice)))
	aborted, err := runSetupForm(ctx, form, input, terminal)
	if err != nil || aborted || choice == "" {
		return nil, false, err
	}
	command := []string{"hosts", "use", "local"}
	switch choice {
	case "managed-wsl":
		confirmed := false
		form = huh.NewForm(huh.NewGroup(huh.NewConfirm().Title("Prepare WSL: loki-tools?").Description("Create a dedicated Ubuntu-24.04 distribution if missing, install Linux services and remember this host. Existing user distributions are preserved.").Affirmative("Prepare host").Negative("Back").Value(&confirmed)))
		aborted, err = runSetupForm(ctx, form, input, terminal)
		if err != nil || aborted || !confirmed {
			return nil, false, err
		}
		command = []string{"hosts", "prepare", "wsl", "--distribution", "loki-tools"}
	case "wsl":
		listCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
		names, listErr := setupWSLDistributions(listCtx)
		cancel()
		if listErr != nil {
			return nil, false, listErr
		}
		options := make([]huh.Option[string], 0, len(names)+1)
		for _, name := range names {
			options = append(options, huh.NewOption(name, name))
		}
		options = append(options, huh.NewOption("Back", ""))
		name := ""
		form = huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title("Choose WSL distribution").Description("Uses this distribution's default user. Installs the Loki CLI in that user's private directory if needed, then continues setup.").Options(options...).Value(&name)))
		aborted, err = runSetupForm(ctx, form, input, terminal)
		if err != nil || aborted || name == "" {
			return nil, false, err
		}
		command = []string{"hosts", "prepare", "wsl", "--distribution", name}
	case "system":
		command = []string{"hosts", "prepare", "local"}
	case "ssh":
		address := ""
		form = huh.NewForm(huh.NewGroup(huh.NewInput().Title("SSH destination (user@host)").Description("Prepare Loki and Linux services on this host; administrator access may be required.").Value(&address).Validate(func(value string) error {
			if strings.TrimSpace(value) == "" {
				return fmt.Errorf("enter an SSH destination")
			}
			return nil
		})))
		aborted, err = runSetupForm(ctx, form, input, terminal)
		if err != nil || aborted {
			return nil, false, err
		}
		command = []string{"hosts", "prepare", "ssh", "--address", strings.TrimSpace(address)}
	}
	if err := runHosts(ctx, store, command[1:], diagnostics, diagnostics); err != nil {
		return nil, false, err
	}
	selected, err := store.ExecutionSelection()
	return selected, err == nil, err
}
