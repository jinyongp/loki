package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"slices"
	"strings"

	"loki/internal/management"
	"loki/internal/tools"
)

type upgradeApplyRequest struct {
	All       bool                           `json:"all_hosts,omitempty"`
	Targets   []upgradeApplyRequest          `json:"targets,omitempty"`
	Failures  []hostUpgradeResult            `json:"failures,omitempty"`
	HostID    string                         `json:"host_id,omitempty"`
	Version   string                         `json:"version"`
	Basis     string                         `json:"basis"`
	Selection *management.ExecutionSelection `json:"selection,omitempty"`
	Remember  bool                           `json:"remember"`
	Force     bool                           `json:"force"`
}

type upgradeApplyResult struct {
	Hosts              []hostUpgradeResult `json:"hosts,omitempty"`
	Changed            bool                `json:"changed"`
	HostManagerUpdated bool                `json:"host_manager_updated"`
	ServicesRestarted  bool                `json:"services_restarted"`
	Reconnect          bool                `json:"reconnect_required"`
}

func executeUpgradeTools(ctx context.Context, binary string, frontend management.Store, request upgradeApplyRequest, input io.Reader, diagnostics io.Writer) (upgradeApplyResult, error) {
	data, err := json.Marshal(request)
	if err != nil {
		return upgradeApplyResult{}, err
	}
	argv := []string{"--root", frontend.Root, "_upgrade-apply"}
	if !request.All && len(data) <= 16<<10 {
		argv = append(argv, "--request", string(data))
	} else {
		file, err := os.CreateTemp("", "loki-host-upgrade-*.json")
		if err != nil {
			return upgradeApplyResult{}, err
		}
		defer os.Remove(file.Name())
		if _, err := file.Write(data); err != nil {
			file.Close()
			return upgradeApplyResult{}, err
		}
		if err := file.Close(); err != nil {
			return upgradeApplyResult{}, err
		}
		argv = append(argv, "--request-file", file.Name())
	}
	command := exec.CommandContext(ctx, binary, argv...)
	var output githubRelayOutput
	command.Stdin, command.Stdout, command.Stderr = input, &output, diagnostics
	err = command.Run()
	var applied upgradeApplyResult
	if output.overflow {
		return applied, fmt.Errorf("upgrade result exceeds its bound")
	}
	if decodeErr := json.Unmarshal(output.Bytes(), &applied); decodeErr != nil {
		if err != nil {
			return applied, err
		}
		return applied, fmt.Errorf("invalid execution-host upgrade result: %w", decodeErr)
	}
	return applied, err
}

func runUpgradeApply(ctx context.Context, frontend management.Store, args []string, out, diagnostics io.Writer) error {
	flags := flag.NewFlagSet("_upgrade-apply", flag.ContinueOnError)
	flags.SetOutput(diagnostics)
	raw := flags.String("request", "", "bounded upgrade request")
	requestFile := flags.String("request-file", "", "private bounded upgrade request file")
	if err := flags.Parse(args); err != nil {
		return err
	}
	if *requestFile != "" {
		if *raw != "" {
			return fmt.Errorf("choose one upgrade request source")
		}
		info, err := os.Lstat(*requestFile)
		if err != nil {
			return err
		}
		if !info.Mode().IsRegular() || info.Size() > 256<<10 {
			return fmt.Errorf("invalid upgrade request file")
		}
		data, err := os.ReadFile(*requestFile)
		if err != nil {
			return err
		}
		*raw = string(data)
	}
	if flags.NArg() != 0 || len(*raw) > 256<<10 {
		return fmt.Errorf("invalid upgrade apply request")
	}
	var request upgradeApplyRequest
	if err := json.Unmarshal([]byte(*raw), &request); err != nil {
		return err
	}
	if request.Version != management.ManagerRelease || len(request.Basis) != 64 {
		return fmt.Errorf("upgrade request differs from verified candidate")
	}
	if request.Selection != nil {
		if err := request.Selection.Validate(); err != nil {
			return err
		}
	}
	if request.All {
		if request.Selection != nil || len(request.Targets) > 128 || len(request.Failures) > 128 {
			return fmt.Errorf("invalid grouped upgrade request")
		}
		ids := map[string]bool{}
		for _, child := range request.Targets {
			if child.All || child.Version != request.Version || len(child.Basis) != 64 || child.HostID == "" || ids[child.HostID] || child.Selection == nil {
				return fmt.Errorf("invalid grouped upgrade target")
			}
			if err := child.Selection.Validate(); err != nil {
				return err
			}
			ids[child.HostID] = true
		}
	}
	var applied upgradeApplyResult
	var err error
	if request.All {
		applied, err = applyUpgradeGroup(ctx, frontend, request, diagnostics)
	} else {
		applied, err = applyUpgradeTools(ctx, frontend, request, diagnostics)
	}
	if encodeErr := json.NewEncoder(out).Encode(applied); encodeErr != nil {
		return encodeErr
	}
	return err
}

func applyUpgradeTools(ctx context.Context, frontend management.Store, request upgradeApplyRequest, diagnostics io.Writer) (upgradeApplyResult, error) {
	applied := upgradeApplyResult{}
	selected := request.Selection
	if request.HostID != "" {
		h, err := frontend.RegisteredHost(request.HostID)
		if err != nil {
			return applied, err
		}
		if selected == nil || h.Target != *selected {
			return applied, fmt.Errorf("registered host changed after confirmation; run upgrade again")
		}
	}
	if request.Remember && request.HostID == "" {
		current, err := frontend.ExecutionSelection()
		if err != nil {
			return applied, err
		}
		if (current == nil) != (selected == nil) || current != nil && *current != *selected {
			return applied, fmt.Errorf("execution host changed after confirmation; run 'loki upgrade' again")
		}
	}
	report, err := inspectOperatorHost(ctx, frontend, selected, diagnostics, false)
	if err != nil {
		return applied, err
	}
	basis, err := upgradeToolsBasis(*report)
	if err != nil || basis != request.Basis {
		return applied, fmt.Errorf("tool selection or running deployment changed after confirmation; run 'loki upgrade' again")
	}
	if selected != nil && selected.Remote() {
		before, err := upgradeHostVersion(ctx, *report, selected, diagnostics)
		if err != nil {
			return applied, err
		}
		if before != request.Version {
			if selected.Owned {
				prepared, err := ensureSetupExecutionHost(ctx, frontend, *selected, request.Remember, diagnostics)
				if err != nil {
					return applied, err
				}
				if request.HostID != "" && prepared != *selected {
					if err := frontend.UpdateHostTarget(request.HostID, *selected, prepared); err != nil {
						return applied, err
					}
				}
				selected = &prepared
			} else if err := upgradeUserHostManager(ctx, *selected, request.Version, request.Force, diagnostics, defaultRemotePreparationDependencies().run); err != nil {
				return applied, err
			}
		}
		applied.HostManagerUpdated = before != request.Version
		applied.Changed = applied.HostManagerUpdated
	}
	installed, enabled := 0, false
	for _, tool := range report.Tools {
		if tool.Installed {
			installed++
			enabled = enabled || tool.Enabled
		}
	}
	if installed == 0 {
		return applied, nil
	}
	prefix := []string{"--host", "local", "--root", frontend.Root}
	if selected != nil && selected.Remote() {
		prefix = setupBoundArguments(frontend, *selected)
	}
	var output githubRelayOutput
	err = run(ctx, append(prefix, "--json", "tools", "update", "--version", request.Version), &output, diagnostics)
	var updated toolUpdateResult
	if decodeErr := json.Unmarshal(output.Bytes(), &updated); decodeErr == nil {
		applied.Changed = applied.Changed || updated.Changed
		applied.ServicesRestarted = updated.ServicesRestarted
		applied.Reconnect = updated.Reconnect || applied.HostManagerUpdated && enabled
	} else if err == nil {
		err = fmt.Errorf("invalid tools update result: %w", decodeErr)
	}
	if err != nil {
		return applied, err
	}
	return applied, nil
}

// Existing user hosts upgrade their recorded owned CLI in place. Bootstrap
// would redirect a custom command/root to the default private install path.
func upgradeUserHostManager(ctx context.Context, selected management.ExecutionSelection, version string, force bool, diagnostics io.Writer, invoke remoteHostRunner) error {
	var help githubRelayOutput
	if err := invoke(ctx, selected, []string{"upgrade", "--help"}, nil, &help, diagnostics); err != nil {
		return fmt.Errorf("cannot inspect host CLI upgrade support: %w", err)
	}
	if help.overflow {
		return fmt.Errorf("execution-host upgrade help exceeds its bound")
	}
	args := []string{"--host", "local"}
	if selected.Root != "" {
		args = append(args, "--root", selected.Root)
	}
	args = append(args, "upgrade", "--version", version, "--yes")
	if force {
		args = append(args, "--force")
	}
	// Managers predating integrated upgrade only update themselves by default.
	if strings.Contains(help.String(), "--cli-only") {
		args = append(args, "--cli-only")
	}
	fmt.Fprintf(diagnostics, "Updating Loki %s on %s...\n", version, setupHostLabel(&selected))
	var output githubRelayOutput
	if err := invoke(ctx, selected, args, nil, &output, diagnostics); err != nil {
		return fmt.Errorf("execution-host CLI upgrade failed: %w\n%s", err, strings.TrimSpace(output.String()))
	}
	var actual githubRelayOutput
	if err := invoke(ctx, selected, []string{"version"}, nil, &actual, diagnostics); err != nil {
		return err
	}
	if actual.overflow || strings.TrimSpace(actual.String()) != "loki "+version {
		return fmt.Errorf("execution-host CLI did not report the target release")
	}
	return nil
}

type toolUpdateResult struct {
	Release           string `json:"release"`
	Changed           bool   `json:"changed"`
	ServicesRestarted bool   `json:"services_restarted"`
	Reconnect         bool   `json:"reconnect_required"`
}

// Update files and apply new full-mode resources only when a deployment was
// already running. A stopped service stays stopped; active jobs block changes.
func updateInstalledTools(ctx context.Context, store management.Store, catalog tools.Catalog, diagnostics io.Writer) (toolUpdateResult, error) {
	return updateInstalledToolsWith(ctx, store, catalog, diagnostics, toolUpdateDependencies{
		backend: management.NewFullBackend,
		reconcile: func(ctx context.Context, store management.Store, backend management.FullBackend) error {
			_, err := store.ReconcileFull(ctx, backend)
			return err
		},
		verify: verifyInstalledBrowser,
	})
}

type toolUpdateDependencies struct {
	backend   func(management.Store, io.Writer) (management.FullBackend, error)
	reconcile func(context.Context, management.Store, management.FullBackend) error
	verify    func(context.Context, management.Store, io.Writer) error
}

func updateInstalledToolsWith(ctx context.Context, store management.Store, catalog tools.Catalog, diagnostics io.Writer, deps toolUpdateDependencies) (toolUpdateResult, error) {
	updated := toolUpdateResult{Release: catalog.Release}
	before, err := store.Status()
	if err != nil {
		return updated, err
	}
	changes, err := changedUpgradeTools(before, catalog)
	if err != nil {
		return updated, err
	}
	restart, err := upgradeRuntimeNeeded(before, catalog)
	if err != nil {
		return updated, err
	}
	files := len(changes) != 0
	if !files && !restart {
		return updated, nil
	}
	var backend management.FullBackend
	if restart {
		backend, err = deps.backend(store, diagnostics)
		if err != nil {
			return updated, err
		}
		if idle, ok := backend.(idleMaintenanceBackend); ok {
			if err := idle.RequireIdle(ctx); err != nil {
				return updated, fmt.Errorf("active jobs block tool updates: %w", err)
			}
		}
	}
	if files {
		if err := store.UpdateTools(ctx, catalog, diagnostics); err != nil {
			return updated, err
		}
		updated.Changed = true
	}
	updated.Reconnect, err = upgradeClientsAffected(before, catalog, changes)
	if err != nil {
		return updated, err
	}
	updated.Reconnect = updated.Reconnect || restart
	if restart {
		fmt.Fprintln(diagnostics, "Replacing running services with the updated tools...")
		if idle, ok := backend.(idleMaintenanceBackend); ok {
			if err := idle.RequireIdle(ctx); err != nil {
				return updated, fmt.Errorf("tools staged but new active jobs block service replacement; retry 'loki upgrade --tools-only': %w", err)
			}
		}
		if err := deps.reconcile(ctx, store, backend); err != nil {
			return updated, fmt.Errorf("tool files updated but services need recovery; run 'loki tools start': %w", err)
		}
		updated.ServicesRestarted = true
		updated.Changed = true
	}
	if before.Tools["browser"].Installed && slices.ContainsFunc(changes, func(change upgradeToolChange) bool { return change.ID == "browser" }) {
		if err := deps.verify(ctx, store, diagnostics); err != nil {
			return updated, fmt.Errorf("tools updated but browser readiness failed: %w", err)
		}
	}
	return updated, nil
}
