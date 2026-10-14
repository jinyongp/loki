package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"strings"
	"time"

	"loki/internal/management"
	"loki/internal/tools"
)

type operatorReport struct {
	management.Report
	HostName      string                        `json:"host_name,omitempty"`
	DefaultHost   string                        `json:"default_host,omitempty"`
	HostCount     int                           `json:"host_count"`
	CLIVersion    string                        `json:"cli_version"`
	ExecutionHost management.ExecutionSelection `json:"execution_host"`
	StateRoot     string                        `json:"state_root"`
	NextActions   []string                      `json:"next_actions"`
	Connections   []clientConnection            `json:"connections"`
}

func operatorStatus(ctx context.Context, store management.Store, selected *management.ExecutionSelection, out, diagnostics io.Writer, doctor bool) error {
	report, probeErr := inspectOperatorHost(ctx, store, selected, diagnostics, doctor)
	if report == nil {
		probeErr = fmt.Errorf("cannot inspect %s; check 'loki hosts show' or choose another host with 'loki setup': %w", setupHostLabel(selected), probeErr)
		report = &management.Report{Issues: []string{probeErr.Error()}}
	}
	host := management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "local"}, Command: "loki"}
	if selected != nil {
		host = *selected
	}
	root := store.Root
	if host.Remote() {
		root = host.Root
	}
	view := operatorReport{Report: *report, CLIVersion: management.ManagerRelease, ExecutionHost: host, StateRoot: root, NextActions: statusNextActions(*report)}
	if doctor {
		// A fresh diagnostic has already run; point at recovery rather than
		// asking users to repeat the same failed check.
		actions := view.NextActions[:0]
		for _, action := range view.NextActions {
			if !strings.HasPrefix(action, "loki doctor") {
				actions = append(actions, action)
			}
		}
		view.NextActions = actions
	}
	if registry, err := store.HostRegistry(); err == nil {
		view.DefaultHost = registry.Selected().Name
		view.HostCount = len(registry.Hosts)
		if record, err := store.HostForTarget(host); err == nil {
			view.HostName = record.Name
		}
	}
	client := inspectCodexConnection()
	if config, err := defaultCodexConfig(); err == nil {
		if raw, err := readCodexConfiguration(config); err == nil {
			client.Bindings = codexBindings(raw, store)
		}
	}
	view.Connections = []clientConnection{client}
	managed, connectionErr := inspectManagedConnections(store)
	if connectionErr != nil {
		managed = []clientConnection{{Provider: "openai", Issue: "Cannot inspect local tunnel metadata; run 'loki connections status openai'"}}
	}
	view.Connections = append(view.Connections, managed...)
	if jsonOutput(out) {
		if err := json.NewEncoder(out).Encode(view); err != nil {
			return err
		}
	} else {
		if err := printStatusForHost(out, *report, view.CLIVersion, &host, root); err != nil {
			return err
		}
		hostName := view.HostName
		if hostName == "" {
			hostName = setupHostLabel(&host)
		}
		fmt.Fprintf(out, "\nHost selection: %s; CLI default: %s (%d registered)\n", hostName, view.DefaultHost, view.HostCount)
		fmt.Fprintln(out, "  loki hosts list — inspect hosts; loki --host NAME status — inspect one")
		fmt.Fprintln(out, "\nClient connections (on this computer):")
		for _, client := range view.Connections {
			state := "not configured"
			if client.Configured {
				state = "configured; target and runtime not checked"
			}
			if client.Issue != "" {
				state = client.Issue
			}
			label := client.Provider
			if client.Host != "" {
				label += " (" + client.Host + ")"
			}
			fmt.Fprintf(out, "  %s: %s\n", label, state)
			for _, binding := range client.Bindings {
				fmt.Fprintf(out, "    %s → %s\n", binding.Server, binding.Host)
			}
		}
		if !doctor {
			fmt.Fprintln(out, "\nStatus shows configuration without starting tools or testing client connections.\nRun 'loki doctor' to check tools; browser sessions start when requested by a client.")
		}
		if len(view.NextActions) > 0 {
			fmt.Fprintln(out, "\nNext:")
			for _, action := range view.NextActions {
				fmt.Fprintln(out, "  "+action)
			}
		}
	}
	return probeErr
}

func inspectOperatorHost(ctx context.Context, store management.Store, selected *management.ExecutionSelection, diagnostics io.Writer, doctor bool) (*management.Report, error) {
	limit := 30 * time.Second
	if doctor {
		limit = 2 * time.Minute
	}
	ctx, cancel := context.WithTimeout(ctx, limit)
	defer cancel()
	var raw bytes.Buffer
	var probeErr error
	if selected == nil || !selected.Remote() {
		probeErr = status(ctx, store, &commandOutput{Writer: &raw, json: true}, diagnostics, doctor)
	} else {
		command := "status"
		if doctor {
			command = "doctor"
		}
		args := []string{"--json", "tools", command}
		if selected.Root != "" {
			args = append([]string{"--root", selected.Root}, args...)
		}
		relay, err := management.RelaySelection(ctx, *selected, args)
		if err != nil {
			return nil, err
		}
		var bounded githubRelayOutput
		relay.Stdout, relay.Stderr = &bounded, diagnostics
		probeErr = relay.Run()
		if bounded.overflow {
			return nil, fmt.Errorf("execution host status exceeds its bound")
		}
		raw.Write(bounded.Bytes())
	}
	var report management.Report
	if err := json.Unmarshal(raw.Bytes(), &report); err != nil {
		if probeErr != nil {
			return nil, probeErr
		}
		return nil, fmt.Errorf("invalid execution host status: %w", err)
	}
	if report.Tools == nil {
		return nil, fmt.Errorf("invalid execution host status: missing tool state")
	}
	return &report, probeErr
}

func statusNextActions(report management.Report) []string {
	if report.Tools == nil {
		return []string{"loki hosts show — inspect the selected execution host", "loki setup — check access or choose another host"}
	}
	installed, enabled := 0, 0
	for _, name := range publicToolNames {
		state := report.Tools[tools.ID(name)]
		if state.Installed {
			installed++
			if state.Enabled {
				enabled++
			}
		}
	}
	if installed == 0 {
		return []string{"loki setup — choose your host, tools and client connection"}
	}
	if len(report.Issues) > 0 || report.Healthy != nil && !*report.Healthy {
		return []string{"loki doctor — inspect the reported problems", "loki setup — repair tool configuration or change host"}
	}
	if enabled == 0 {
		return []string{"loki tools manage — enable an installed tool"}
	}
	if report.Ready == nil || !*report.Ready {
		return []string{"loki doctor — verify runtime readiness", "loki setup — configure tools and client connections"}
	}
	return []string{"loki setup — manage tools and client connections"}
}

func toolRuntimeLabel(state tools.State, mode tools.Mode) string {
	if !state.Installed {
		return "—"
	}
	if !state.Enabled {
		return "disabled"
	}
	if state.Readiness == tools.Ready {
		if mode == tools.ProjectHost {
			return "verified; starts on demand"
		}
		return "ready"
	}
	if state.Readiness == tools.Degraded {
		return "needs attention"
	}
	if mode == tools.ProjectHost {
		return "on demand; not checked"
	}
	return "not checked"
}
