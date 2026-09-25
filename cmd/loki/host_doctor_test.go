package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	hostdiagnostics "loki/internal/host/diagnostics"
	"loki/internal/host/lifecycle"
	lifecyclecompose "loki/internal/host/lifecycle/compose"
	"loki/internal/work/jobs"
	toolchain "loki/internal/work/toolchains"
)

type fakeHostDoctorRuntime struct {
	readiness    lifecyclecompose.RuntimeReadiness
	readinessErr error
	snapshots    []lifecycle.RuntimeSnapshotInfo
}

type fakeProbedHostDoctorRuntime struct {
	*fakeHostDoctorRuntime
	raw []byte
	err error
}

func (f *fakeProbedHostDoctorRuntime) DoctorProbe(context.Context) ([]byte, error) {
	return append([]byte(nil), f.raw...), f.err
}

func (f *fakeHostDoctorRuntime) Readiness(context.Context) (lifecyclecompose.RuntimeReadiness, error) {
	return f.readiness, f.readinessErr
}

func (f *fakeHostDoctorRuntime) RuntimeSnapshotUsage(_ context.Context, ref string) (int64, error) {
	for _, snapshot := range f.snapshots {
		if snapshot.Ref == ref {
			return snapshot.Bytes, nil
		}
	}
	return 0, errors.New("runtime snapshot missing")
}

func (f *fakeHostDoctorRuntime) ListRuntimeSnapshots(context.Context) ([]lifecycle.RuntimeSnapshotInfo, error) {
	return append([]lifecycle.RuntimeSnapshotInfo(nil), f.snapshots...), nil
}

func (f *fakeHostDoctorRuntime) DeleteRuntimeSnapshot(context.Context, string) error {
	return errors.New("diagnostics must not delete runtime snapshots")
}

func hostDoctorFixture(t *testing.T) (hostDoctorOptions, *fakeHostDoctorRuntime, time.Time) {
	t.Helper()
	now := time.Date(2026, 9, 22, 3, 0, 0, 0, time.UTC)
	stateRoot := filepath.Join(t.TempDir(), "lifecycle")
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	candidate := hostGenerationFixture(t, now)
	var installOut, installErr bytes.Buffer
	if code := runHostInstallWith(
		t.Context(),
		hostInstallOptions{StateRoot: stateRoot, Workspace: workspace},
		candidate,
		&fakeHostRuntimeBackend{},
		&installOut,
		&installErr,
	); code != 0 {
		t.Fatalf("install fixture code=%d stderr=%q", code, installErr.String())
	}

	store, err := lifecycle.OpenFileStore(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	backups, err := store.ReadBackupSnapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	runtime := &fakeHostDoctorRuntime{
		readiness: lifecyclecompose.RuntimeReadiness{
			Activated: true, GenerationID: candidate.ID,
			RequiredServices: []string{"egress", "executor", "launcher", "mcp", "runtime"},
			RunningServices:  []string{"egress", "executor", "launcher", "mcp", "runtime"},
		},
	}
	for _, backup := range backups {
		runtime.snapshots = append(runtime.snapshots, lifecycle.RuntimeSnapshotInfo{
			Ref: backup.RuntimeRef, Bytes: 128, CreatedAt: backup.CreatedAt,
		})
	}

	toolchainRoot := filepath.Join(t.TempDir(), "toolchains")
	t.Cleanup(func() {
		_ = filepath.WalkDir(toolchainRoot, func(path string, entry os.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil
			}
			if entry.IsDir() {
				_ = os.Chmod(path, 0700)
			} else {
				_ = os.Chmod(path, 0600)
			}
			return nil
		})
	})
	goRelease := toolchain.GoRelease{
		Version: "1.27.1",
		URL:     "https://go.dev/dl/go1.27.1.linux-amd64.tar.gz",
		SHA256:  strings.Repeat("a", 64),
	}
	catalog := toolchain.Catalog{Version: toolchain.CatalogVersion, Go: []toolchain.GoRelease{goRelease}}
	if err = catalog.Validate(); err != nil {
		t.Fatal(err)
	}
	generationStore := toolchain.GenerationStore{Root: toolchainRoot, Protected: catalog.GenerationIDs()}
	if _, err = generationStore.Provision(t.Context(), goRelease.GenerationID(), func(_ context.Context, root string) error {
		return os.WriteFile(filepath.Join(root, "marker"), []byte("managed-go"), 0444)
	}); err != nil {
		t.Fatal(err)
	}
	catalogPath := filepath.Join(t.TempDir(), "toolchain-catalog.json")
	catalogRaw, err := json.Marshal(catalog)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(catalogPath, append(catalogRaw, '\n'), 0644); err != nil {
		t.Fatal(err)
	}

	journalDir := filepath.Join(t.TempDir(), "launcher")
	if err = os.MkdirAll(filepath.Join(journalDir, "journal"), 0700); err != nil {
		t.Fatal(err)
	}
	launcherPath := filepath.Join(t.TempDir(), "launcher.json")
	layout := hostLauncherLayout{
		StateDirectory: journalDir, ToolchainStore: toolchainRoot,
		MaxJobs: 64, MaxConcurrentJobs: 8, MaxOutputBytes: 262144, ResultRetentionSeconds: 3600,
	}
	layoutRaw, err := json.Marshal(layout)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(launcherPath, append(layoutRaw, '\n'), 0600); err != nil {
		t.Fatal(err)
	}

	return hostDoctorOptions{
		StateRoot: stateRoot, LauncherLayout: launcherPath, ToolchainCatalog: catalogPath,
	}, runtime, now
}

func TestDoctorToolchainsAcceptsLazyUninitializedStore(t *testing.T) {
	options, _, _ := hostDoctorFixture(t)
	var layout hostLauncherLayout
	raw, err := os.ReadFile(options.LauncherLayout)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &layout); err != nil {
		t.Fatal(err)
	}
	layout.ToolchainStore = filepath.Join(t.TempDir(), "toolchains")
	if err = os.Mkdir(layout.ToolchainStore, 0700); err != nil {
		t.Fatal(err)
	}
	checks := inspectDoctorToolchains(options.ToolchainCatalog, &layout)
	if len(checks) != 1 || checks[0].Status != hostdiagnostics.StatusHealthy ||
		checks[0].Code != "toolchains_ready" {
		t.Fatalf("lazy store checks = %#v", checks)
	}
	evidence := map[string]string{}
	for _, item := range checks[0].Evidence {
		evidence[item.Name] = item.Value
	}
	if evidence["store_initialized"] != "false" ||
		evidence["provisioned_catalog_generations"] != "0" ||
		evidence["unprovisioned_catalog_generations"] == "0" {
		t.Fatalf("lazy store evidence = %#v", evidence)
	}
}

func TestHostDoctorUsesRuntimeProbeForComposeState(t *testing.T) {
	options, runtime, now := hostDoctorFixture(t)
	options.LauncherLayout = filepath.Join(t.TempDir(), "missing-launcher.json")
	options.ToolchainCatalog = filepath.Join(t.TempDir(), "missing-toolchain-catalog.json")
	probe := hostdiagnostics.RuntimeProbe{
		MaxConcurrentJobs: 8,
		ToolchainChecks: []hostdiagnostics.Check{
			hostdiagnostics.Healthy(
				"toolchains", "toolchains_ready",
				"administrator-approved managed toolchains are provisioned",
				hostdiagnostics.Evidence{Name: "catalog_generations", Value: "1"},
			),
		},
	}
	raw, err := json.Marshal(probe)
	if err != nil {
		t.Fatal(err)
	}
	probed := &fakeProbedHostDoctorRuntime{fakeHostDoctorRuntime: runtime, raw: raw}
	report, err := inspectHostDoctor(t.Context(), options, hostDoctorDependencies{
		OpenRuntime: func(*lifecycle.FileStore) (hostDoctorRuntime, error) { return probed, nil },
		Now:         func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if !report.Healthy() {
		t.Fatalf("runtime-probed report = %#v", report)
	}
	for _, check := range report.Checks {
		if check.Code == "launcher_layout_unavailable" || check.Code == "toolchain_layout_unavailable" {
			t.Fatalf("runtime probe fell back to host layout: %#v", report.Checks)
		}
	}
}

func TestHostRuntimeProbeEmitsValidatedRuntimeState(t *testing.T) {
	options, _, _ := hostDoctorFixture(t)
	var stdout, stderr bytes.Buffer
	if code := runHostRuntimeProbe([]string{
		"--launcher-layout", options.LauncherLayout,
		"--toolchain-catalog", options.ToolchainCatalog,
	}, &stdout, &stderr); code != 0 {
		t.Fatalf("runtime probe code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	var probe hostdiagnostics.RuntimeProbe
	if err := json.Unmarshal(stdout.Bytes(), &probe); err != nil {
		t.Fatal(err)
	}
	if err := probe.Validate(); err != nil {
		t.Fatal(err)
	}
	if len(probe.ActiveJobs) != 0 || probe.MaxConcurrentJobs != 8 ||
		len(probe.ToolchainChecks) != 1 || probe.ToolchainChecks[0].Status != hostdiagnostics.StatusHealthy {
		t.Fatalf("runtime probe = %#v", probe)
	}
}

func TestHostDoctorHealthyReportIsBoundedAndRedacted(t *testing.T) {
	options, runtime, now := hostDoctorFixture(t)
	dependencies := hostDoctorDependencies{
		OpenRuntime: func(*lifecycle.FileStore) (hostDoctorRuntime, error) { return runtime, nil },
		Now:         func() time.Time { return now },
	}
	report, err := inspectHostDoctor(t.Context(), options, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if !report.Healthy() || report.Status != hostdiagnostics.StatusHealthy || len(report.Checks) != 8 {
		t.Fatalf("healthy report = %#v", report)
	}
	encoded, err := json.Marshal(report)
	if err != nil {
		t.Fatal(err)
	}
	if len(encoded) > 16<<10 {
		t.Fatalf("diagnostic report exceeded bound: %d bytes", len(encoded))
	}
	for _, forbidden := range []string{"password=", "token=", "private_key", "authorization"} {
		if strings.Contains(strings.ToLower(string(encoded)), forbidden) {
			t.Fatalf("diagnostic report contains protected field %q: %s", forbidden, encoded)
		}
	}

	var stdout, stderr bytes.Buffer
	code := runHostDoctorWith(t.Context(), options, dependencies, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "\"status\":\"healthy\"") {
		t.Fatalf("doctor code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestHostDoctorBlocksWhenRecoveredActiveJobsExceedLimit(t *testing.T) {
	options, runtime, now := hostDoctorFixture(t)
	var layout hostLauncherLayout
	raw, err := os.ReadFile(options.LauncherLayout)
	if err != nil {
		t.Fatal(err)
	}
	if err = json.Unmarshal(raw, &layout); err != nil {
		t.Fatal(err)
	}
	journal, err := jobs.OpenJournal(filepath.Join(layout.StateDirectory, "journal"), jobs.JournalLimits{
		MaxRecords: layout.MaxJobs, MaxRecordBytes: int64(layout.MaxOutputBytes)*6 + (64 << 10),
		MaxOutputBytes: layout.MaxOutputBytes, Retention: time.Duration(layout.ResultRetentionSeconds) * time.Second,
	})
	if err != nil {
		t.Fatal(err)
	}
	const hexIDs = "abcdef0123456789"
	for index := 0; index < layout.MaxConcurrentJobs+1; index++ {
		id := strings.Repeat(string(hexIDs[index]), 32)
		if _, err = journal.Admit(id, "oci:"+strings.Repeat("a", 64), now.Add(time.Hour), now); err != nil {
			t.Fatal(err)
		}
	}
	if err = journal.Close(); err != nil {
		t.Fatal(err)
	}
	report, err := inspectHostDoctor(t.Context(), options, hostDoctorDependencies{
		OpenRuntime: func(*lifecycle.FileStore) (hostDoctorRuntime, error) { return runtime, nil },
		Now:         func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, check := range report.Checks {
		if check.Name == "jobs" && check.Status == hostdiagnostics.StatusBlocked &&
			check.Code == "active_jobs_over_limit" {
			found = true
		}
	}
	if !found {
		t.Fatalf("active Job overload was not reported: %#v", report.Checks)
	}
}

func TestHostDoctorReportsDegradedOrphanStorage(t *testing.T) {
	options, runtime, now := hostDoctorFixture(t)
	runtime.snapshots = append(runtime.snapshots, lifecycle.RuntimeSnapshotInfo{
		Ref: "orphan-runtime-snapshot", Bytes: 64, CreatedAt: now.Add(-time.Hour),
	})
	report, err := inspectHostDoctor(t.Context(), options, hostDoctorDependencies{
		OpenRuntime: func(*lifecycle.FileStore) (hostDoctorRuntime, error) { return runtime, nil },
		Now:         func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	if report.Status != hostdiagnostics.StatusDegraded {
		t.Fatalf("degraded report = %#v", report)
	}
	found := false
	for _, check := range report.Checks {
		if check.Name == "storage" && check.Status == hostdiagnostics.StatusDegraded &&
			check.Code == "orphan_snapshots_present" {
			found = true
		}
	}
	if !found {
		t.Fatalf("orphan storage check missing: %#v", report.Checks)
	}
}

func TestHostDoctorBlocksRuntimeGenerationMismatch(t *testing.T) {
	options, runtime, now := hostDoctorFixture(t)
	runtime.readiness.GenerationID = "sha256:" + strings.Repeat("f", 64)
	report, err := inspectHostDoctor(t.Context(), options, hostDoctorDependencies{
		OpenRuntime: func(*lifecycle.FileStore) (hostDoctorRuntime, error) { return runtime, nil },
		Now:         func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}
	found := false
	for _, check := range report.Checks {
		if check.Name == "runtime" && check.Status == hostdiagnostics.StatusBlocked &&
			check.Code == "runtime_generation_mismatch" {
			found = true
		}
	}
	if !found {
		t.Fatalf("runtime generation mismatch was not reported: %#v", report.Checks)
	}
}

func TestHostDoctorBlocksWithoutLeakingRuntimeFailureCause(t *testing.T) {
	options, runtime, now := hostDoctorFixture(t)
	runtime.readinessErr = errors.New("docker diagnostic failed with token=super-sensitive-value")
	dependencies := hostDoctorDependencies{
		OpenRuntime: func(*lifecycle.FileStore) (hostDoctorRuntime, error) { return runtime, nil },
		Now:         func() time.Time { return now },
	}
	var stdout, stderr bytes.Buffer
	code := runHostDoctorWith(t.Context(), options, dependencies, &stdout, &stderr)
	if code != 1 || stderr.Len() != 0 {
		t.Fatalf("blocked doctor code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	if strings.Contains(stdout.String(), "super-sensitive-value") ||
		!strings.Contains(stdout.String(), "\"code\":\"runtime_inspection_failed\"") ||
		!strings.Contains(stdout.String(), "\"status\":\"blocked\"") {
		t.Fatalf("blocked report leaked or lost stable status: %q", stdout.String())
	}
}

func TestHostDoctorOptionParsing(t *testing.T) {
	var stderr bytes.Buffer
	options, err := parseHostDoctorOptions([]string{
		"--state-root", "/var/lib/loki/lifecycle",
		"--launcher-layout", "/etc/loki-go/launcher.json",
	}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if options.ToolchainCatalog != defaultHostToolchainCatalog {
		t.Fatalf("default toolchain catalog = %q", options.ToolchainCatalog)
	}
	if _, err = parseHostDoctorOptions([]string{"--state-root", "relative"}, &stderr); err == nil {
		t.Fatal("relative doctor state root was accepted")
	}
}
