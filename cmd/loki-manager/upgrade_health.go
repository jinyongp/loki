package main

import (
	"context"
	"fmt"
	"io"
	"strings"
	"time"

	"loki/internal/management"
	"loki/internal/tools"
	"loki/modules/browser"
)

type upgradeHealth struct {
	State       string             `json:"state"`
	Report      *management.Report `json:"report,omitempty"`
	Note        string             `json:"note,omitempty"`
	Error       string             `json:"error,omitempty"`
	NextActions []string           `json:"next_actions,omitempty"`
}

func diagnoseUpgradeTools(ctx context.Context, store management.Store, diagnostics io.Writer) (*upgradeHealth, error) {
	return diagnoseUpgradeToolsWith(ctx, store, diagnostics, management.NewFullBackend, browser.CheckRuntime)
}

// Reuse doctor's resource checks and probes, but preserve intentionally stopped
// full services and disabled project-host tools. Unobserved readiness is explicit.
func diagnoseUpgradeToolsWith(ctx context.Context, store management.Store, diagnostics io.Writer, fullBackend func(management.Store, io.Writer) (management.FullBackend, error), browserProbe management.Probe) (*upgradeHealth, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Minute)
	defer cancel()
	fmt.Fprintln(diagnostics, "Checking tool health after upgrade...")
	health := &upgradeHealth{State: "failed"}
	fail := func(err error) (*upgradeHealth, error) {
		health.Error = err.Error()
		health.NextActions = health.actions()
		return health, fmt.Errorf("post-upgrade health check failed: %w", err)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	before, err := store.Status()
	if err != nil {
		return fail(err)
	}
	probes := map[tools.ID]management.Probe{}
	deferred := ""
	enabled := false
	for _, tool := range before.Tools {
		enabled = enabled || tool.Enabled
	}
	if !enabled {
		deferred = "No tools are enabled; runtime checks were deferred."
	} else if before.Target.Mode == tools.Full {
		if len(before.Deployments) == 0 {
			deferred = "Full services remain stopped; runtime checks were deferred until services are started."
		} else {
			backend, backendErr := fullBackend(store, diagnostics)
			probes, err = store.FullProbes(backend)
			if err != nil {
				return fail(err)
			}
			if backendErr != nil {
				for id := range probes {
					probes[id] = func(context.Context, string) error { return backendErr }
				}
			}
		}
	} else if before.Tools["browser"].Enabled {
		probes["browser"] = browserProbe
	}
	report, err := store.Doctor(ctx, probes)
	health.Report = &report
	if err != nil {
		return fail(err)
	}
	if err := ctx.Err(); err != nil {
		return fail(err)
	}
	if report.Healthy == nil || !*report.Healthy || deferred == "" && (report.Ready == nil || !*report.Ready) {
		return fail(fmt.Errorf("installed tools need attention; see the automatic health report"))
	}
	health.State = "passed"
	if deferred != "" {
		health.State, health.Note = "deferred", deferred+" Installed resources were checked."
	}
	health.NextActions = health.actions()
	return health, nil
}

func (h *upgradeHealth) actions() []string {
	if h.Report == nil {
		return nil
	}
	if h.State == "deferred" {
		if h.Report.Target.Mode == tools.Full && len(h.Report.Deployments) == 0 {
			for _, tool := range h.Report.Tools {
				if tool.Enabled {
					return []string{"loki tools start — start selected services when needed and verify runtime readiness"}
				}
			}
		}
		return []string{"loki tools manage — enable tools when needed"}
	}
	if h.State != "failed" {
		return nil
	}
	var actions []string
	for _, action := range statusNextActions(*h.Report) {
		if !strings.HasPrefix(action, "loki doctor") {
			actions = append(actions, action)
		}
	}
	return actions
}

func scopeUpgradeHealth(store management.Store, selected *management.ExecutionSelection, health *upgradeHealth) {
	if health == nil {
		return
	}
	host := management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "local"}, Command: "loki"}
	if selected != nil {
		host = *selected
	}
	name := ""
	if record, err := store.HostForTarget(host); err == nil {
		name = record.Name
	}
	health.NextActions = health.actions()
	prefix := recoveryCommandPrefix(store.Root, host, name)
	for i, action := range health.NextActions {
		if strings.HasPrefix(action, "loki ") {
			health.NextActions[i] = prefix + strings.TrimPrefix(action, "loki")
		}
	}
}

func printUpgradeHealth(out io.Writer, health *upgradeHealth) {
	if health == nil {
		return
	}
	fmt.Fprintln(out, "Automatic health check:", health.State)
	if health.Note != "" {
		fmt.Fprintln(out, "  "+health.Note)
	}
	if health.Error != "" {
		fmt.Fprintln(out, "  "+health.Error)
	}
	if health.Report != nil && health.State == "failed" {
		for _, issue := range health.Report.Issues {
			fmt.Fprintln(out, "  "+issue)
		}
	}
	for _, action := range health.NextActions {
		fmt.Fprintln(out, "  "+action)
	}
}
