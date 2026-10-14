package main

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"slices"
	"strings"
	"time"

	"loki/internal/management"
	"loki/internal/tools"
)

type upgradeToolChange struct {
	ID               tools.ID `json:"id"`
	Current          string   `json:"current"`
	Target           string   `json:"target"`
	VerificationOnly bool     `json:"verification_required,omitempty"`
}

type upgradeToolsPlan struct {
	Host           string                         `json:"host"`
	ManagerCurrent string                         `json:"manager_current,omitempty"`
	ManagerUpdate  bool                           `json:"manager_update"`
	ReleaseCurrent string                         `json:"release_current,omitempty"`
	Installed      int                            `json:"installed"`
	Changes        []upgradeToolChange            `json:"changes"`
	Update         bool                           `json:"update_available"`
	RuntimeAction  string                         `json:"runtime_action"`
	Reconnect      bool                           `json:"reconnect_required"`
	RuntimeUpdate  bool                           `json:"runtime_update_required"`
	Basis          string                         `json:"-"`
	Selection      *management.ExecutionSelection `json:"-"`
	Remember       bool                           `json:"-"`
	Clients        bool                           `json:"-"`
}

// Read only public installation state. Runtime probes and host preparation are
// deliberately excluded from planning, including --check and cancelled upgrades.
func planUpgradeTools(ctx context.Context, store management.Store, selected *management.ExecutionSelection, remember bool, target string, diagnostics io.Writer, client *http.Client) (*upgradeToolsPlan, error) {
	if selected == nil {
		var err error
		selected, err = store.ExecutionSelection()
		if err != nil {
			return nil, err
		}
	}
	report, err := inspectOperatorHost(ctx, store, selected, diagnostics, false)
	if err != nil {
		return nil, fmt.Errorf("cannot plan upgrades for %s: %w; use --cli-only to update just this CLI", setupHostLabel(selected), err)
	}
	manager, err := upgradeHostVersion(ctx, *report, selected, diagnostics)
	if err != nil {
		return nil, err
	}
	plan := &upgradeToolsPlan{Host: setupHostLabel(selected), ManagerCurrent: manager, ReleaseCurrent: report.Release, Changes: []upgradeToolChange{}, RuntimeAction: "none", Selection: selected, Remember: remember}
	plan.ManagerUpdate = selected != nil && selected.Remote() && manager != target
	plan.Basis, err = upgradeToolsBasis(*report)
	if err != nil {
		return nil, err
	}
	for _, state := range report.Tools {
		if state.Installed {
			plan.Installed++
		}
	}
	if plan.Installed != 0 {
		catalog, err := acquireCatalogForTarget(ctx, report.Target, target, diagnostics, client)
		if err != nil {
			return nil, err
		}
		plan.Changes, err = changedUpgradeTools(*report, catalog)
		if err != nil {
			return nil, err
		}
		plan.Update = len(plan.Changes) != 0
		plan.RuntimeUpdate, err = upgradeRuntimeNeeded(*report, catalog)
		if err != nil {
			return nil, err
		}
		plan.Update = plan.Update || plan.RuntimeUpdate
		plan.Reconnect, err = upgradeClientsAffected(*report, catalog, plan.Changes)
		if err != nil {
			return nil, err
		}
	}
	plan.Update = plan.Update || plan.ManagerUpdate
	for _, state := range report.Tools {
		plan.Clients = plan.Clients || state.Installed && state.Enabled
		if state.Installed && state.Enabled && (plan.ManagerUpdate || plan.RuntimeUpdate) {
			plan.Reconnect = true
		}
	}
	if plan.RuntimeUpdate {
		plan.RuntimeAction = "replace running services after checking for active jobs"
	}
	if plan.Reconnect {
		plan.RuntimeAction += "; reconnect client MCP after completion"
	}
	return plan, nil
}

func upgradeHostVersion(ctx context.Context, report management.Report, selected *management.ExecutionSelection, diagnostics io.Writer) (string, error) {
	if report.ManagerVersion != "" {
		return report.ManagerVersion, nil
	}
	if selected == nil || !selected.Remote() {
		return management.ManagerRelease, nil
	}
	ctx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	var version githubRelayOutput
	if err := defaultRemotePreparationDependencies().run(ctx, *selected, []string{"version"}, nil, &version, diagnostics); err != nil {
		return "", err
	}
	if version.overflow {
		return "", fmt.Errorf("execution-host version exceeds its bound")
	}
	value := strings.TrimPrefix(strings.TrimSpace(version.String()), "loki ")
	if !stableVersionPattern.MatchString(value) {
		return "", fmt.Errorf("invalid execution-host CLI version")
	}
	return value, nil
}

func upgradeClientsAffected(report management.Report, catalog tools.Catalog, changes []upgradeToolChange) (bool, error) {
	registry, err := tools.NewRegistry(catalog.Modules)
	if err != nil {
		return false, err
	}
	enabled := []tools.ID{}
	for id, state := range report.Tools {
		if state.Installed && state.Enabled {
			enabled = append(enabled, id)
		}
	}
	resolution, err := registry.ResolveInstallation(report.Target, enabled)
	if err != nil {
		return false, err
	}
	for _, module := range resolution.Ordered {
		for _, change := range changes {
			if change.ID == module.ID {
				return true, nil
			}
		}
	}
	return false, nil
}

func upgradeRuntimeNeeded(report management.Report, catalog tools.Catalog) (bool, error) {
	if report.Target.Mode != tools.Full || len(report.Deployments) == 0 {
		return false, nil
	}
	registry, err := tools.NewRegistry(catalog.Modules)
	if err != nil {
		return false, err
	}
	enabled := []tools.ID{}
	for id, state := range report.Tools {
		if state.Installed && state.Enabled {
			enabled = append(enabled, id)
		}
	}
	resolution, err := registry.ResolveInstallation(report.Target, enabled)
	if err != nil {
		return false, err
	}
	artifacts, err := resolution.Artifacts(catalog.Artifacts)
	if err != nil {
		return false, err
	}
	expected := map[tools.ID]string{}
	for _, artifact := range artifacts {
		expected[artifact.Module] = artifact.SHA256
	}
	for _, deployment := range report.Deployments {
		if len(deployment.Artifacts) != len(expected) {
			return true, nil
		}
		for _, artifact := range deployment.Artifacts {
			if expected[artifact.Module] != artifact.SHA256 {
				return true, nil
			}
		}
	}
	return false, nil
}

func changedUpgradeTools(report management.Report, catalog tools.Catalog) ([]upgradeToolChange, error) {
	ids := []tools.ID{}
	for id, state := range report.Tools {
		if state.Installed {
			ids = append(ids, id)
		}
	}
	slices.Sort(ids)
	registry, err := tools.NewRegistry(catalog.Modules)
	if err != nil {
		return nil, err
	}
	resolution, err := registry.ResolveInstallation(report.Target, ids)
	if err != nil {
		return nil, err
	}
	artifacts, err := resolution.Artifacts(catalog.Artifacts)
	if err != nil {
		return nil, err
	}
	changes := []upgradeToolChange{}
	for _, artifact := range artifacts {
		current := report.Tools[artifact.Module]
		if !current.Installed || current.SHA256 == "" || current.SHA256 != artifact.SHA256 {
			changes = append(changes, upgradeToolChange{ID: artifact.Module, Current: current.Release, Target: artifact.Release, VerificationOnly: current.Installed && current.SHA256 == ""})
		}
	}
	return changes, nil
}

func upgradeToolsBasis(report management.Report) (string, error) {
	// Check before preparing the host manager. Its version can change after the
	// frontend publication, while installed package identities must stay fixed.
	type state struct {
		Installed, Enabled bool
		Release            string
		SHA256             string
		Capabilities       []string
	}
	value := struct {
		Release     string
		Target      tools.Target
		Tools       map[tools.ID]state
		Deployments []string
	}{Release: report.Release, Target: report.Target, Tools: map[tools.ID]state{}}
	for id, tool := range report.Tools {
		value.Tools[id] = state{tool.Installed, tool.Enabled, tool.Release, tool.SHA256, tool.Capabilities}
	}
	for _, deployment := range report.Deployments {
		value.Deployments = append(value.Deployments, deployment.ID+":"+deployment.PlanDigest)
	}
	slices.Sort(value.Deployments)
	data, err := json.Marshal(value)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%x", sha256.Sum256(data)), nil
}

func printUpgradeToolsPlan(out io.Writer, plan *upgradeToolsPlan, target string) {
	if plan == nil {
		fmt.Fprintln(out, "Scope: CLI only")
		return
	}
	fmt.Fprintln(out, "Execution host:", plan.Host)
	if plan.ManagerUpdate {
		fmt.Fprintf(out, "  Host CLI: %s -> %s\n", plan.ManagerCurrent, target)
	}
	if plan.Installed == 0 {
		fmt.Fprintln(out, "  Tools: none installed")
	}
	for _, change := range plan.Changes {
		action := "update"
		if change.VerificationOnly {
			action = "verify; update only if package changed"
		}
		fmt.Fprintf(out, "  %s: %s -> %s (%s)\n", change.ID, change.Current, change.Target, action)
	}
	if plan.Installed > 0 && len(plan.Changes) == 0 {
		fmt.Fprintln(out, "  Tool packages: unchanged")
	}
	fmt.Fprintln(out, "  Runtime:", plan.RuntimeAction)
}
