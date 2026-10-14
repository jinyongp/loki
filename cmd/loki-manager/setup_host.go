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

func setupSavedHostChoices(registry management.HostRegistry, full bool) []huh.Option[string] {
	choices := []huh.Option[string]{}
	for _, h := range registry.Hosts {
		if full && !h.Target.Remote() {
			continue
		}
		label := h.Name + " — " + setupHostLabel(&h.Target)
		if h.ID == registry.Default {
			label += " (Loki default)"
		}
		choices = append(choices, huh.NewOption(label, "registered:"+h.ID))
	}
	return append(choices, huh.NewOption("Set up another execution host...", "prepare"), huh.NewOption("Back", ""))
}

func setupHostPreparationChoices(platform string) []huh.Option[string] {
	choices := []huh.Option[string]{}
	if platform == "windows" {
		choices = append(choices,
			huh.NewOption("Existing WSL distribution — select an installed distribution...", "wsl"),
			huh.NewOption("Dedicated WSL: loki-tools — create or reuse Loki's host...", "managed-wsl"))
	}
	if platform == "linux" {
		choices = append(choices, huh.NewOption("Local Linux — set up managed services...", "system"))
	}
	return append(choices, huh.NewOption("SSH Linux host — enter an address...", "ssh"), huh.NewOption("Back", ""))
}

// Host preparation is a separate choice, before downloads or host changes.
func chooseSetupHost(ctx context.Context, store management.Store, input *os.File, diagnostics io.Writer, full bool) (*management.ExecutionSelection, bool, error) {
	terminal, ok := diagnostics.(*os.File)
	if !ok || !term.IsTerminal(int(input.Fd())) || !term.IsTerminal(int(terminal.Fd())) || os.Getenv("TERM") == "dumb" {
		return nil, false, fmt.Errorf("choose an execution host first with 'loki hosts prepare' or 'loki hosts use'; run 'loki setup' in a terminal for guided host selection")
	}
	registry, err := store.HostRegistry()
	if err != nil {
		return nil, false, err
	}
	choices := setupSavedHostChoices(registry, full)
	description := "Use a saved host, or set up another execution host."
	if full {
		description = "This tool needs Linux services. Use a saved Linux host, or set one up."
	}
	description += "\nSelection sets Loki's default. Tools, data and existing Codex connections stay on their hosts."
	for {
		choice := choices[0].Value
		for _, option := range choices {
			if option.Value == "registered:"+registry.Default {
				choice = option.Value
			}
		}
		form := huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title("Choose execution host").Description(description).Options(choices...).Value(&choice)))
		aborted, err := runSetupForm(ctx, form, input, terminal)
		if err != nil || aborted || choice == "" {
			return nil, false, err
		}
		if strings.HasPrefix(choice, "registered:") {
			if err := store.UseHost(strings.TrimPrefix(choice, "registered:")); err != nil {
				return nil, false, err
			}
			selected, err := store.ExecutionSelection()
			return selected, err == nil, err
		}
		selected, changed, err := prepareSetupHost(ctx, store, input, terminal)
		if err != nil || changed {
			return selected, changed, err
		}
	}
}

func prepareSetupHost(ctx context.Context, store management.Store, input, terminal *os.File) (*management.ExecutionSelection, bool, error) {
	choices := setupHostPreparationChoices(runtime.GOOS)
	choice := choices[0].Value
	description := "Prepare a host, then choose which tools to install."
	if runtime.GOOS == "windows" {
		description += "\nExisting WSL uses your distribution; dedicated WSL uses a separate loki-tools distribution."
	}
	form := huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title("Set up execution host").
		Description(description).
		Options(choices...).Value(&choice)))
	aborted, err := runSetupForm(ctx, form, input, terminal)
	if err != nil || aborted || choice == "" {
		return nil, false, err
	}
	var command []string
	var preparationLabel string
	switch choice {
	case "managed-wsl":
		confirmed := false
		form = huh.NewForm(huh.NewGroup(huh.NewConfirm().Title("Set up dedicated WSL: loki-tools?").Description("Create a separate Ubuntu-24.04 distribution if missing and prepare its Loki CLI. Your existing WSL distributions stay separate. Tools are installed in the next step.").Affirmative("Prepare host").Negative("Back").Value(&confirmed)))
		aborted, err = runSetupForm(ctx, form, input, terminal)
		if err != nil || aborted || !confirmed {
			return nil, false, err
		}
		command = []string{"hosts", "prepare", "wsl", "--distribution", "loki-tools"}
		preparationLabel = "WSL: loki-tools"
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
		name := options[0].Value
		form = huh.NewForm(huh.NewGroup(huh.NewSelect[string]().Title("Choose existing WSL distribution").Description("Uses an already installed distribution and its default user. Prepares the Loki CLI if needed; no new distribution is created.").Options(options...).Value(&name)))
		aborted, err = runSetupForm(ctx, form, input, terminal)
		if err != nil || aborted || name == "" {
			return nil, false, err
		}
		command = []string{"hosts", "prepare", "wsl", "--distribution", name}
		preparationLabel = "WSL: " + name
	case "system":
		command = []string{"hosts", "prepare", "local"}
		preparationLabel = "Local Linux (managed system host)"
	case "ssh":
		address := ""
		form = huh.NewForm(huh.NewGroup(huh.NewInput().Title("SSH destination (user@host)").Description("Use existing SSH access to prepare the Loki CLI on this Linux host. Tools are installed in the next step.").Value(&address).Validate(func(value string) error {
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
		preparationLabel = "SSH: " + strings.TrimSpace(address)
	default:
		return nil, false, fmt.Errorf("unknown execution host preparation")
	}
	if err := setupWorking(terminal, "Selecting and preparing execution host", preparationLabel, func(log io.Writer) error {
		return runHosts(ctx, store, command[1:], log, log)
	}); err != nil {
		return nil, false, err
	}
	selected, err := store.ExecutionSelection()
	return selected, err == nil, err
}

func setupBoundArguments(frontend management.Store, selected management.ExecutionSelection) []string {
	if record, err := frontend.HostForTarget(selected); err == nil {
		return []string{"--root", frontend.Root, "--host-id", record.ID}
	}
	return setupExecutionArguments(selected)
}
