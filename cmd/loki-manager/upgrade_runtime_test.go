package main

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"runtime"
	"strings"
	"testing"

	"loki/internal/management"
	"loki/internal/tools"
)

type upgradeRuntimeProbe struct {
	idleCalls, busyAt, reconciles int
	fail                          bool
}

func (p *upgradeRuntimeProbe) RequireIdle(context.Context) error {
	p.idleCalls++
	if p.busyAt > 0 && p.idleCalls >= p.busyAt {
		return errors.New("fixture job running")
	}
	return nil
}
func (*upgradeRuntimeProbe) ConfirmAbsent(context.Context, management.DeploymentReservation) error {
	return nil
}
func (*upgradeRuntimeProbe) Stop(context.Context, management.DeploymentReservation) error { return nil }
func (*upgradeRuntimeProbe) Prepare(context.Context, management.DeploymentReservation, management.FullTopology, management.FullLayouts) error {
	return nil
}
func (*upgradeRuntimeProbe) Start(context.Context, management.DeploymentReservation) error {
	return nil
}
func (*upgradeRuntimeProbe) Observe(context.Context, management.DeploymentReservation) (management.FullObservation, error) {
	return management.FullObservation{}, nil
}
func (p *upgradeRuntimeProbe) dependencies() toolUpdateDependencies {
	return toolUpdateDependencies{
		backend:  func(management.Store, io.Writer) (management.FullBackend, error) { return p, nil },
		diagnose: func(context.Context, management.Store, io.Writer) (*upgradeHealth, error) { return nil, nil },
		reconcile: func(ctx context.Context, store management.Store, b management.FullBackend) error {
			p.reconciles++
			if p.fail {
				return errors.New("fixture service start failed")
			}
			if err := store.StopFull(ctx, b); err != nil {
				return err
			}
			plan, err := store.PlanFull()
			if err != nil {
				return err
			}
			_, err = store.ReserveDeployment(plan)
			return err
		},
	}
}

func upgradeRuntimeFixture(t *testing.T) (management.Store, tools.Catalog) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("full tools use Linux")
	}
	store := management.Store{Root: t.TempDir()}
	target := management.LocalTarget(tools.Full)
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	state.Config.Mode = tools.Full
	state.Config.Release = "0.2.98"
	state.Config.Contract = tools.CompositionContract
	catalog := tools.Catalog{Schema: 1, Release: "0.2.98", Contract: tools.CompositionContract}
	for _, id := range []tools.ID{"runtime-core", "workspace", "git"} {
		a := tools.Artifact{Module: id, Release: "0.2.98", Target: target, URL: "https://example.invalid/tool.zip", SHA256: strings.Repeat("a", 64), Bytes: 1, Format: "zip"}
		m := tools.Manifest{Schema: 1, ID: id, Release: a.Release, Contract: tools.CompositionContract, Targets: []tools.Target{target}, Tools: []string{}}
		if id != "runtime-core" {
			m.Requires = []tools.ID{"runtime-core"}
		}
		prepareUpgradeGeneration(t, store, a, m)
		state.Installed[id] = management.Installation{Artifact: a, Manifest: m}
		catalog.Artifacts = append(catalog.Artifacts, a)
		catalog.Modules = append(catalog.Modules, m)
	}
	state.Config.Tools = []tools.Selection{{ID: "workspace", Enabled: true}, {ID: "git", Enabled: false}}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	plan, err := store.PlanFull()
	if err != nil {
		t.Fatal(err)
	}
	if _, err := store.ReserveDeployment(plan); err != nil {
		t.Fatal(err)
	}
	return store, catalog
}

func prepareUpgradeGeneration(t *testing.T, store management.Store, a tools.Artifact, m tools.Manifest) {
	t.Helper()
	directory, err := store.Generation(a)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(directory, 0700); err != nil {
		t.Fatal(err)
	}
	values := map[string]any{".loki-owner.json": management.Owner{Schema: 1, Identity: a.Identity()}, "module.json": m, "full-runtime.json": management.FullPayload{Schema: 1, Module: a.Module, Release: a.Release, Target: a.Target, Images: map[string]string{}, Programs: map[string]string{}, Assets: map[string]string{}}}
	for name, value := range values {
		data, err := json.Marshal(value)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(directory, name), data, 0600); err != nil {
			t.Fatal(err)
		}
	}
}

func changeUpgradeArtifact(t *testing.T, store management.Store, catalog *tools.Catalog, id tools.ID) {
	t.Helper()
	for i := range catalog.Artifacts {
		if catalog.Artifacts[i].Module == id {
			catalog.Artifacts[i].SHA256 = strings.Repeat("b", 64)
			prepareUpgradeGeneration(t, store, catalog.Artifacts[i], catalog.Modules[i])
		}
	}
}

func TestToolUpdatesPreserveStoppedAndUnaffectedServices(t *testing.T) {
	for _, kind := range []string{"unchanged", "release-only", "stopped", "disabled-tool"} {
		t.Run(kind, func(t *testing.T) {
			store, catalog := upgradeRuntimeFixture(t)
			beforePlan, err := store.PlanFull()
			if err != nil {
				t.Fatal(err)
			}
			probe := &upgradeRuntimeProbe{}
			switch kind {
			case "release-only":
				catalog.Release = "0.2.99"
			case "stopped":
				if err := store.StopFull(t.Context(), probe); err != nil {
					t.Fatal(err)
				}
				changeUpgradeArtifact(t, store, &catalog, "workspace")
			case "disabled-tool":
				changeUpgradeArtifact(t, store, &catalog, "git")
			}
			updated, err := updateInstalledToolsWith(t.Context(), store, catalog, io.Discard, probe.dependencies())
			if err != nil || updated.ServicesRestarted || probe.reconciles != 0 || probe.idleCalls != 0 {
				t.Fatalf("updated=%+v probe=%+v err=%v", updated, probe, err)
			}
			if kind == "unchanged" && updated.Changed {
				t.Fatal("unchanged tool claimed update")
			}
			if kind == "disabled-tool" && updated.Reconnect {
				t.Fatal("disabled tool update affected client")
			}
			if kind == "release-only" || kind == "disabled-tool" {
				afterPlan, err := store.PlanFull()
				if err != nil || !reflect.DeepEqual(beforePlan, afterPlan) {
					t.Fatal("unaffected live deployment invalidated", err)
				}
			}
			if kind == "stopped" {
				deployments, err := store.Deployments()
				if err != nil || len(deployments) != 0 {
					t.Fatal("stopped services started", deployments, err)
				}
			}
		})
	}
}

func TestToolUpdateBlocksActiveJobsBeforeChangingFiles(t *testing.T) {
	store, catalog := upgradeRuntimeFixture(t)
	changeUpgradeArtifact(t, store, &catalog, "workspace")
	before, _ := os.ReadFile(store.ActivationPath())
	probe := &upgradeRuntimeProbe{busyAt: 1}
	updated, err := updateInstalledToolsWith(t.Context(), store, catalog, io.Discard, probe.dependencies())
	after, _ := os.ReadFile(store.ActivationPath())
	if err == nil || updated.Changed || string(before) != string(after) || probe.reconciles != 0 {
		t.Fatalf("changed active state: %+v %v", updated, err)
	}
}

func TestToolUpdateResumesRuntimeAfterPartialFailure(t *testing.T) {
	for _, kind := range []string{"new job", "service failure"} {
		t.Run(kind, func(t *testing.T) {
			store, catalog := upgradeRuntimeFixture(t)
			changeUpgradeArtifact(t, store, &catalog, "workspace")
			sentinel := filepath.Join(store.Root, "user-data")
			if err := os.WriteFile(sentinel, []byte("retained"), 0600); err != nil {
				t.Fatal(err)
			}
			probe := &upgradeRuntimeProbe{fail: kind == "service failure"}
			if kind == "new job" {
				probe.busyAt = 2
			}
			updated, err := updateInstalledToolsWith(t.Context(), store, catalog, io.Discard, probe.dependencies())
			if err == nil || !updated.Changed || updated.ServicesRestarted {
				t.Fatalf("missing partial failure: %+v %v", updated, err)
			}
			report, _ := store.Status()
			changes, err := changedUpgradeTools(report, catalog)
			if err != nil || len(changes) != 0 {
				t.Fatal("files were not updated", changes, err)
			}
			pending, err := upgradeRuntimeNeeded(report, catalog)
			if err != nil || !pending {
				t.Fatal("runtime retry incorrectly considered current", err)
			}
			probe.busyAt = 0
			probe.fail = false
			resumed, err := updateInstalledToolsWith(t.Context(), store, catalog, io.Discard, probe.dependencies())
			if err != nil || !resumed.ServicesRestarted || !resumed.Changed {
				t.Fatalf("runtime retry: %+v %v", resumed, err)
			}
			report, _ = store.Status()
			if pending, err := upgradeRuntimeNeeded(report, catalog); err != nil || pending {
				t.Fatal("runtime still stale", err)
			}
			if data, err := os.ReadFile(sentinel); err != nil || string(data) != "retained" {
				t.Fatal("data changed", err)
			}
		})
	}
}
