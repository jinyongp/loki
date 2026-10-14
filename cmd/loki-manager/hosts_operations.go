package main

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"sync"

	"loki/internal/management"
)

type hostStatusResult struct {
	ID      string                        `json:"host_id"`
	Name    string                        `json:"name"`
	Default bool                          `json:"default"`
	Target  management.ExecutionSelection `json:"target"`
	Report  *management.Report            `json:"report,omitempty"`
	Error   string                        `json:"error,omitempty"`
}

func statusAllHosts(ctx context.Context, store management.Store, out, diagnostics io.Writer, doctor bool) error {
	registry, err := store.HostRegistry()
	if err != nil {
		return err
	}
	results := make([]hostStatusResult, len(registry.Hosts))
	logs := make([]bytes.Buffer, len(results))
	jobs := make(chan int)
	var workers sync.WaitGroup
	for worker := 0; worker < min(4, len(results)); worker++ {
		workers.Add(1)
		go func() {
			defer workers.Done()
			for i := range jobs {
				h := registry.Hosts[i]
				r, e := inspectOperatorHost(ctx, store, &h.Target, &logs[i], doctor)
				results[i] = hostStatusResult{ID: h.ID, Name: h.Name, Default: h.ID == registry.Default, Target: h.Target, Report: r}
				if e != nil {
					results[i].Error = e.Error()
				}
			}
		}()
	}
	for i := range results {
		jobs <- i
	}
	close(jobs)
	workers.Wait()
	failed := false
	for i, r := range results {
		if logs[i].Len() > 0 {
			fmt.Fprintf(diagnostics, "[%s]\n%s", r.Name, logs[i].String())
		}
		failed = failed || r.Error != ""
	}
	if jsonOutput(out) {
		if err := json.NewEncoder(out).Encode(map[string]any{"cli_version": management.ManagerRelease, "default_host": registry.Default, "hosts": results, "complete": !failed}); err != nil {
			return err
		}
	} else {
		fmt.Fprintln(out, "Loki execution hosts")
		for _, r := range results {
			label := r.Name
			if r.Default {
				label += " (default)"
			}
			fmt.Fprintf(out, "\n%s\n  Target: %s\n", label, setupHostLabel(&r.Target))
			if r.Error != "" {
				fmt.Fprintln(out, "  Unavailable:", shortDiagnostic(r.Error))
				continue
			}
			fmt.Fprintln(out, "  Host CLI:", r.Report.ManagerVersion)
			installed, _ := setupToolCounts(*r.Report)
			fmt.Fprintln(out, "  Installed tools:", installed)
			if doctor && r.Report.Ready != nil {
				fmt.Fprintln(out, "  Ready:", *r.Report.Ready)
			}
		}
	}
	if failed {
		return fmt.Errorf("some hosts could not be inspected; other host results are retained")
	}
	return nil
}

type hostUpgradeResult struct {
	ID     string             `json:"host_id"`
	Result upgradeApplyResult `json:"result"`
	Error  string             `json:"error,omitempty"`
}

func upgradeAllHosts(ctx context.Context, store management.Store, args []string, input io.Reader, out, diagnostics io.Writer) error {
	return upgradeAllHostsWithDependencies(ctx, store, args, input, out, diagnostics, defaultUpgradeDependencies())
}
func upgradeAllHostsWithDependencies(ctx context.Context, store management.Store, args []string, input io.Reader, out, diagnostics io.Writer, deps upgradeDependencies) error {
	filtered := []string{}
	for _, arg := range args {
		if arg != "--all-hosts" {
			filtered = append(filtered, arg)
		}
	}
	for _, arg := range filtered {
		if arg == "--cli-only" {
			return fmt.Errorf("--all-hosts cannot be combined with --cli-only")
		}
	}
	deps.plan = func(ctx context.Context, store management.Store, version string, diagnostics io.Writer) (*upgradeToolsPlan, error) {
		registry, err := store.HostRegistry()
		if err != nil {
			return nil, err
		}
		group := &upgradeToolsPlan{Host: "all registered hosts", RuntimeAction: "per-host actions shown below", Changes: []upgradeToolChange{}, All: true}
		for _, h := range registry.Hosts {
			child, err := planUpgradeTools(ctx, store, &h.Target, false, version, diagnostics, deps.client)
			if err != nil {
				group.Failures = append(group.Failures, hostUpgradeResult{ID: h.ID, Error: h.Name + ": " + err.Error()})
				group.Update = true
				continue
			}
			child.HostID = h.ID
			child.Host = h.Name + " (" + child.Host + ")"
			group.Hosts = append(group.Hosts, child)
			group.Update = group.Update || child.Update
			group.Clients = group.Clients || child.Clients
			group.Reconnect = group.Reconnect || child.Reconnect
		}
		raw, err := json.Marshal(group.Hosts)
		if err != nil {
			return nil, err
		}
		group.Basis = fmt.Sprintf("%x", sha256.Sum256(raw))
		return group, nil
	}
	return runUpgrade(ctx, store, filtered, input, out, diagnostics, deps)
}
func applyUpgradeGroup(ctx context.Context, store management.Store, request upgradeApplyRequest, diagnostics io.Writer) (upgradeApplyResult, error) {
	result := upgradeApplyResult{Hosts: append([]hostUpgradeResult{}, request.Failures...)}
	errors := []string{}
	for _, failure := range request.Failures {
		errors = append(errors, failure.Error)
	}
	for _, child := range request.Targets {
		applied, err := applyUpgradeTools(ctx, store, child, diagnostics)
		entry := hostUpgradeResult{ID: child.HostID, Result: applied}
		if err != nil {
			entry.Error = err.Error()
			errors = append(errors, entry.ID+": "+entry.Error)
		}
		result.Hosts = append(result.Hosts, entry)
		result.Changed = result.Changed || applied.Changed
		result.HostManagerUpdated = result.HostManagerUpdated || applied.HostManagerUpdated
		result.ServicesRestarted = result.ServicesRestarted || applied.ServicesRestarted
		result.Reconnect = result.Reconnect || applied.Reconnect
	}
	if len(errors) > 0 {
		return result, fmt.Errorf("host updates incomplete: %s", strings.Join(errors, "; "))
	}
	return result, nil
}
