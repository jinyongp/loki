package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"loki/internal/fault"
	"loki/internal/management"
	"loki/internal/tools"
)

func TestUpgradeHealthPreservesStoppedServicesAndChecksResources(t *testing.T) {
	store, catalog := upgradeRuntimeFixture(t)
	probe := &upgradeRuntimeProbe{}
	if err := store.StopFull(t.Context(), probe); err != nil {
		t.Fatal(err)
	}
	before, _ := store.Load()
	check := func() (*upgradeHealth, error) {
		return diagnoseUpgradeToolsWith(t.Context(), store, io.Discard,
			func(management.Store, io.Writer) (management.FullBackend, error) {
				t.Fatal("stopped services contacted the backend")
				return nil, nil
			}, nil)
	}
	health, err := check()
	if err != nil || health.State != "deferred" || health.Report.Healthy == nil || !*health.Report.Healthy || !strings.Contains(health.Note, "remain stopped") {
		t.Fatalf("health=%+v err=%v", health, err)
	}
	after, _ := store.Load()
	if !reflect.DeepEqual(before, after) {
		t.Fatal("health check mutated tool selection")
	}
	deployments, err := store.Deployments()
	if err != nil || len(deployments) != 0 {
		t.Fatal("health check started services", deployments, err)
	}
	generation, _ := store.Generation(catalog.Artifacts[1])
	if err := os.Remove(filepath.Join(generation, ".loki-owner.json")); err != nil {
		t.Fatal(err)
	}
	health, err = check()
	if err == nil || health.State != "failed" || *health.Report.Healthy {
		t.Fatal("stopped runtime hid resource damage", health, err)
	}
}

func upgradeBrowserHealthFixture(t *testing.T, enabled bool) management.Store {
	t.Helper()
	store := management.Store{Root: t.TempDir()}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	artifact := tools.Artifact{Module: "browser", Release: management.Release, Target: management.LocalTarget(tools.ProjectHost), URL: "https://example.invalid/browser.zip", SHA256: strings.Repeat("a", 64), Bytes: 1, Format: "zip"}
	manifest := tools.Manifest{Schema: 1, ID: "browser", Release: artifact.Release, Targets: []tools.Target{artifact.Target}, Tools: []string{}}
	prepareUpgradeGeneration(t, store, artifact, manifest)
	state.Installed["browser"] = management.Installation{Artifact: artifact, Manifest: manifest}
	state.Config.Tools = []tools.Selection{{ID: "browser", Enabled: enabled}}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	return store
}

func TestUpgradeHealthChecksEnabledBrowserAndDefersDisabledBrowser(t *testing.T) {
	for _, enabled := range []bool{false, true} {
		store := upgradeBrowserHealthFixture(t, enabled)
		called := false
		check := func(probeErr error) (*upgradeHealth, error) {
			return diagnoseUpgradeToolsWith(t.Context(), store, io.Discard, nil, func(context.Context, string) error {
				called = true
				return probeErr
			})
		}
		health, err := check(nil)
		if err != nil || called != enabled || enabled && health.State != "passed" || !enabled && health.State != "deferred" {
			t.Fatalf("enabled=%v called=%v health=%+v err=%v", enabled, called, health, err)
		}
		if enabled {
			health, err = check(errors.New("Chrome cannot start"))
			if err == nil || health.State != "failed" || !strings.Contains(strings.Join(health.Report.Issues, " "), "Chrome cannot start") {
				t.Fatal("browser failure hidden", health, err)
			}
		}
	}
}

type upgradeRecoveryBackend struct {
	upgradeRuntimeProbe
	recovery fault.StoreRecovery
	observed int
}

func (b *upgradeRecoveryBackend) Observe(context.Context, management.DeploymentReservation) (management.FullObservation, error) {
	b.observed++
	return management.FullObservation{Services: map[string]string{"mcp": "ready", "workspace": "ready"}, StoreRecovery: []fault.StoreRecovery{b.recovery}}, nil
}

func TestUpgradeHealthIncludesRuntimeStoreRecoveryAndScopedActions(t *testing.T) {
	store, catalog := upgradeRuntimeFixture(t)
	// Use the real full-mode attribution path for a module backed by runtime.
	state, _ := store.Load()
	a := catalog.Artifacts[1]
	a.Module = "secrets"
	m := catalog.Modules[1]
	m.ID = "secrets"
	prepareUpgradeGeneration(t, store, a, m)
	state.Installed[a.Module] = management.Installation{Artifact: a, Manifest: m}
	state.Config.Tools = append(state.Config.Tools, tools.Selection{ID: a.Module, Enabled: true})
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	probe := &upgradeRuntimeProbe{}
	if err := store.StopFull(t.Context(), probe); err != nil {
		t.Fatal(err)
	}
	plan, err := store.PlanFull()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReserveDeployment(plan); err != nil {
		t.Fatal(err)
	}
	backend := &upgradeRecoveryBackend{recovery: fault.StoreRecovery{Schema: 1, Owner: "secrets", Reason: "missing_key"}}
	health, err := diagnoseUpgradeToolsWith(t.Context(), store, io.Discard, func(management.Store, io.Writer) (management.FullBackend, error) { return backend, nil }, nil)
	if err == nil || health.State != "failed" || backend.observed != 1 || len(health.Report.StoreRecovery) != 1 {
		t.Fatalf("health=%+v observed=%d err=%v", health, backend.observed, err)
	}
	selected := management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "ssh", Address: "user@build"}, Command: "/custom bin/loki", Root: "/custom state"}
	for range 2 { // Re-scoping a host-side result must not duplicate flags.
		scopeUpgradeHealth(store, &selected, health)
	}
	actions := strings.Join(health.NextActions, "\n")
	if !strings.Contains(actions, "--address user@build") || !strings.Contains(actions, "'/custom state'") || !strings.Contains(actions, "backups list") || strings.Contains(actions, "loki doctor") || strings.Count(health.NextActions[0], "--root") != 1 {
		t.Fatal(actions)
	}
}

func TestToolUpdateRetainsOriginalFailureAndAutomaticDiagnosis(t *testing.T) {
	for _, stage := range []string{"current", "updated", "service failure"} {
		t.Run(stage, func(t *testing.T) {
			store, catalog := upgradeRuntimeFixture(t)
			if stage != "current" {
				changeUpgradeArtifact(t, store, &catalog, "workspace")
			}
			probe := &upgradeRuntimeProbe{fail: stage == "service failure"}
			deps := probe.dependencies()
			calls := 0
			cause := errors.New("fixture health failure")
			deps.diagnose = func(context.Context, management.Store, io.Writer) (*upgradeHealth, error) {
				calls++
				return &upgradeHealth{State: "failed", Error: cause.Error()}, cause
			}
			updated, err := updateInstalledToolsWith(t.Context(), store, catalog, io.Discard, deps)
			if !errors.Is(err, cause) || updated.Health == nil || updated.Health.State != "failed" || calls != 1 || updated.Changed != (stage != "current") {
				t.Fatalf("result=%+v calls=%d err=%v", updated, calls, err)
			}
			if stage == "service failure" && !strings.Contains(err.Error(), "fixture service start failed") {
				t.Fatal("automatic diagnosis hid the original update error", err)
			}
		})
	}
}

func TestToolUpdateCLIAutomaticallyReportsHealth(t *testing.T) {
	for _, structured := range []bool{false, true} {
		for _, damaged := range []bool{false, true} {
			store, catalog := upgradeRuntimeFixture(t)
			if err := store.StopFull(t.Context(), &upgradeRuntimeProbe{}); err != nil {
				t.Fatal(err)
			}
			if damaged {
				generation, _ := store.Generation(catalog.Artifacts[1])
				if err := os.Remove(filepath.Join(generation, ".loki-owner.json")); err != nil {
					t.Fatal(err)
				}
			}
			data, _ := json.Marshal(catalog)
			file := filepath.Join(t.TempDir(), "catalog.json")
			if err := os.WriteFile(file, data, 0600); err != nil {
				t.Fatal(err)
			}
			var out, diagnostics bytes.Buffer
			args := []string{"--host", "local", "--root", store.Root, "tools", "update", "--catalog", file}
			if structured {
				args = append(args, "--json")
			}
			err := run(t.Context(), args, &out, &diagnostics)
			if (err != nil) != damaged {
				t.Fatalf("damaged=%v err=%v stdout=%s stderr=%s", damaged, err, out.String(), diagnostics.String())
			}
			want := "deferred"
			if damaged {
				want = "failed"
			}
			if structured {
				value := singleJSON(t, out.Bytes())
				health, ok := value["health"].(map[string]any)
				if !ok || health["state"] != want {
					t.Fatal(value)
				}
			} else if !strings.Contains(out.String(), "Automatic health check: "+want) {
				t.Fatal(out.String())
			}
		}
	}
}

func TestRemoteUpgradeKeepsAutomaticHealthAndRecoveryOnSelectedHost(t *testing.T) {
	store := management.Store{Root: t.TempDir()}
	recovery := fault.StoreRecovery{Schema: 1, Owner: "secrets", Reason: "missing_data"}
	unhealthy := false
	report := management.Report{ManagerVersion: management.ManagerRelease, Target: management.LocalTarget(tools.Full), Tools: map[tools.ID]tools.State{"secrets": {Installed: true, Enabled: true}}, Healthy: &unhealthy, Issues: []string{recovery.Error()}, StoreRecovery: []fault.StoreRecovery{recovery}}
	data, _ := json.Marshal(report)
	log := installSetupSSHFixture(t, string(data))
	// Keep the existing SSH fixture for status and add the real update reply
	// contract: one JSON report survives a nonzero remote process exit.
	ssh := filepath.Join(filepath.Dir(log), "ssh")
	script, _ := os.ReadFile(ssh)
	reply, _ := json.Marshal(toolUpdateResult{Changed: true, Health: &upgradeHealth{State: "failed", Report: &report, Error: "encrypted store needs recovery", NextActions: recovery.NextActions()}})
	t.Setenv("LOKI_UPGRADE_HEALTH_REPLY", string(reply))
	script = bytes.Replace(script, []byte("case \"$*\" in\n"), []byte("case \"$*\" in\n  *\"'tools' 'update'\"*) printf '%s\\n' \"$LOKI_UPGRADE_HEALTH_REPLY\"; exit 1 ;;\n"), 1)
	if err := os.WriteFile(ssh, script, 0700); err != nil {
		t.Fatal(err)
	}
	selected := management.ExecutionSelection{Schema: 1, Host: tools.Host{Kind: "ssh", Address: "user@build"}, Command: "/custom bin/loki", Root: "/custom state"}
	basis, err := upgradeToolsBasis(report)
	if err != nil {
		t.Fatal(err)
	}
	applied, err := applyUpgradeTools(t.Context(), store, upgradeApplyRequest{Version: management.ManagerRelease, Basis: basis, Selection: &selected}, io.Discard)
	if err == nil || !applied.Changed || applied.Health == nil || len(applied.Health.Report.StoreRecovery) != 1 {
		t.Fatalf("result=%+v err=%v", applied, err)
	}
	if !strings.Contains(applied.Health.NextActions[0], "--address user@build") || !strings.Contains(applied.Health.NextActions[0], "'/custom state'") || strings.Contains(applied.Health.NextActions[0], store.Root) {
		t.Fatal("recovery redirected to the frontend", applied.Health.NextActions)
	}
	calls, _ := os.ReadFile(log)
	if !strings.Contains(string(calls), "'tools' 'update'") || !strings.Contains(string(calls), "'--root' '/custom state'") || strings.Contains(string(calls), "'tools' 'start'") {
		t.Fatal(string(calls))
	}
	deps, _, _ := upgradeFixture(t, management.ManagerRelease, false)
	deps.plan = func(context.Context, management.Store, string, io.Writer) (*upgradeToolsPlan, error) {
		return &upgradeToolsPlan{Installed: 1, Selection: &selected, Basis: basis}, nil
	}
	deps.run = func(context.Context, string, ...string) ([]byte, error) { return nil, nil }
	deps.apply = func(context.Context, string, management.Store, upgradeApplyRequest, io.Reader, io.Writer) (upgradeApplyResult, error) {
		return applied, errors.New("remote health failed")
	}
	for _, structured := range []bool{false, true} {
		var out bytes.Buffer
		err := runUpgrade(t.Context(), store, []string{"-y"}, strings.NewReader(""), &commandOutput{Writer: &out, json: structured}, io.Discard, deps)
		if err == nil {
			t.Fatal("unhealthy upgrade claimed success")
		}
		if structured {
			value := singleJSON(t, out.Bytes())
			health := value["applied"].(map[string]any)["health"].(map[string]any)
			if value["state"] != "partial" || value["changed"] != true || health["state"] != "failed" {
				t.Fatal(value)
			}
		} else if !strings.Contains(out.String(), "Automatic health check: failed") || !strings.Contains(out.String(), "--address user@build") || strings.Contains(out.String(), "Retry 'loki upgrade") {
			t.Fatal(out.String())
		}
	}
}
