package main

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"loki/internal/host/lifecycle"
	lifecyclecompose "loki/internal/host/lifecycle/compose"
)

type staticIntegrationRuntime struct {
	readiness lifecyclecompose.RuntimeReadiness
	err       error
}

func (r staticIntegrationRuntime) Readiness(context.Context) (lifecyclecompose.RuntimeReadiness, error) {
	return r.readiness, r.err
}

func hostIntegrationStoreFixture(t *testing.T) (*lifecycle.FileStore, time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 29, 14, 0, 0, 0, time.UTC)
	root := filepath.Join(t.TempDir(), "state")
	store, err := lifecycle.EnsureFileStore(root)
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err = os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	generation := newHostUpdateFixture(t, "1.2.3", now.Add(-time.Hour)).generation
	if err = store.InitializeInstall(t.Context(), generation, lifecycle.InstallationState{
		Scope: "user", Workspace: workspace,
	}, now); err != nil {
		t.Fatal(err)
	}
	if err = store.CommitGeneration(t.Context(), generation, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	return store, now
}

func TestInspectHostBrowserIntegrationDisabledReadyAndDegraded(t *testing.T) {
	store, now := hostIntegrationStoreFixture(t)
	runtime := staticIntegrationRuntime{}

	report, err := inspectHostIntegration(t.Context(), store, runtime, "browser")
	if err != nil {
		t.Fatal(err)
	}
	if !report.Configured || report.Enabled || report.Ready || report.State != "disabled" || !report.ProjectionConsistent {
		t.Fatalf("disabled browser report=%#v", report)
	}

	if err = store.CommitComponents(t.Context(), []string{"browser"}, now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	managed := lifecycle.DefaultManagedIntegrationState()
	managed.Browser.Enabled = true
	if err = store.CommitManagedIntegrations(t.Context(), managed, "browser-enabled", now.Add(3*time.Second)); err != nil {
		t.Fatal(err)
	}
	runtime.readiness = lifecyclecompose.RuntimeReadiness{
		Activated: true, GenerationID: "generation",
		RunningServices: []string{"browser", "browser-proxy", "egress", "executor", "launcher", "mcp", "runtime"},
	}
	report, err = inspectHostIntegration(t.Context(), store, runtime, "browser")
	if err != nil {
		t.Fatal(err)
	}
	if !report.Enabled || !report.Ready || report.State != "ready" || !report.ProjectionConsistent {
		t.Fatalf("ready browser report=%#v", report)
	}

	managed.Browser.Enabled = false
	if err = store.CommitManagedIntegrations(t.Context(), managed, "projection-mismatch", now.Add(4*time.Second)); err != nil {
		t.Fatal(err)
	}
	report, err = inspectHostIntegration(t.Context(), store, runtime, "browser")
	if err != nil {
		t.Fatal(err)
	}
	if report.Ready || report.State != "degraded" || report.ProjectionConsistent {
		t.Fatalf("mismatched browser report=%#v", report)
	}
}

func TestInspectHostBrowserIntegrationDegradesWhenReadinessFails(t *testing.T) {
	store, now := hostIntegrationStoreFixture(t)
	if err := store.CommitComponents(t.Context(), []string{"browser"}, now.Add(time.Second)); err != nil {
		t.Fatal(err)
	}
	managed := lifecycle.DefaultManagedIntegrationState()
	managed.Browser.Enabled = true
	if err := store.CommitManagedIntegrations(t.Context(), managed, "browser-enabled", now.Add(2*time.Second)); err != nil {
		t.Fatal(err)
	}
	report, err := inspectHostIntegration(t.Context(), store, staticIntegrationRuntime{err: errors.New("runtime unavailable")}, "browser")
	if err != nil {
		t.Fatal(err)
	}
	if report.State != "degraded" || report.Ready {
		t.Fatalf("runtime failure browser report=%#v", report)
	}
}

func TestParseHostIntegrationBrowserActions(t *testing.T) {
	for _, action := range []string{"status", "enable", "disable", "doctor"} {
		options, name, err := parseHostIntegrationOptions(action, []string{"--state-root", "/tmp/loki-state", "browser"}, os.Stderr)
		if err != nil {
			t.Fatalf("%s parse: %v", action, err)
		}
		if name != "browser" || options.StateRoot != "/tmp/loki-state" {
			t.Fatalf("%s options=%#v name=%q", action, options, name)
		}
	}
	if _, _, err := parseHostIntegrationOptions("status", []string{"unknown"}, os.Stderr); err == nil {
		t.Fatal("unknown integration was accepted")
	}
}
