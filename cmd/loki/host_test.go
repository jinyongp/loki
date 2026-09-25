package main

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	"loki/internal/host/lifecycle"
	"loki/internal/host/releases"
	"loki/internal/work/jobs"
)

type fakeHostLifecycle struct {
	status       lifecycle.UpdateStatus
	plan         lifecycle.PreparedPlan
	result       lifecycle.ApplyResult
	statusErr    error
	prepareErr   error
	applyErr     error
	applyOptions []lifecycle.ApplyOptions
}

func (f *fakeHostLifecycle) Status(context.Context) (lifecycle.UpdateStatus, error) {
	return f.status, f.statusErr
}

func (f *fakeHostLifecycle) Prepare(context.Context) (lifecycle.PreparedPlan, error) {
	return f.plan, f.prepareErr
}

func (f *fakeHostLifecycle) Apply(_ context.Context, options lifecycle.ApplyOptions) (lifecycle.ApplyResult, error) {
	f.applyOptions = append(f.applyOptions, options)
	return f.result, f.applyErr
}

func TestRunHostUpdateWithRoutesActionsAndEncodesJSON(t *testing.T) {
	manager := &fakeHostLifecycle{
		status: lifecycle.UpdateStatus{UpdateAvailable: true},
		plan: lifecycle.PreparedPlan{
			ID: "sha256:" + strings.Repeat("a", 64),
		},
		result: lifecycle.ApplyResult{
			PlanID: "sha256:" + strings.Repeat("a", 64),
		},
	}
	for action, want := range map[string]string{
		"status":  "\"update_available\":true",
		"prepare": "\"id\":\"sha256:",
		"apply":   "\"plan_id\":\"sha256:",
	} {
		var stdout, stderr bytes.Buffer
		code := runHostUpdateWith(
			t.Context(), manager, action,
			lifecycle.ApplyOptions{InterruptActiveJobs: action == "apply"},
			&stdout, &stderr,
		)
		if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), want) {
			t.Fatalf("%s = code %d stdout %q stderr %q", action, code, stdout.String(), stderr.String())
		}
	}
	if !reflect.DeepEqual(manager.applyOptions, []lifecycle.ApplyOptions{{InterruptActiveJobs: true}}) {
		t.Fatalf("apply options = %#v", manager.applyOptions)
	}

	var stdout, stderr bytes.Buffer
	if code := runHostUpdateWith(t.Context(), manager, "unknown", lifecycle.ApplyOptions{}, &stdout, &stderr); code != 2 {
		t.Fatalf("unknown action code = %d", code)
	}
}

type fakeHostRuntimeBackend struct {
	active       string
	snapshots    map[string]string
	nextSnapshot int
	healthErr    error
}

func (b *fakeHostRuntimeBackend) Snapshot(_ context.Context, _ lifecycle.OperationKind, snapshot lifecycle.Snapshot) (lifecycle.RuntimeSnapshot, error) {
	if b.snapshots == nil {
		b.snapshots = map[string]string{}
	}
	b.nextSnapshot++
	ref := "runtime-" + strings.Repeat("x", b.nextSnapshot)
	b.snapshots[ref] = b.active
	return lifecycle.RuntimeSnapshot{
		Ref: ref,
		Coverage: lifecycle.BackupCoverage{
			RuntimeState: true, ConfigState: true, HostState: true, WorkspacePreserved: true,
			OptionalComponentState: append([]string(nil), snapshot.Host.EnabledComponents...),
		},
	}, nil
}

func (b *fakeHostRuntimeBackend) Activate(_ context.Context, generation lifecycle.Generation, _ lifecycle.InstallationState) error {
	b.active = generation.ID
	return nil
}

func (*fakeHostRuntimeBackend) SetComponent(context.Context, lifecycle.Generation, string, bool) error {
	return nil
}

func (*fakeHostRuntimeBackend) Migrate(context.Context, []lifecycle.MigrationStep) error { return nil }
func (*fakeHostRuntimeBackend) Restart(context.Context) error                            { return nil }
func (b *fakeHostRuntimeBackend) Health(context.Context) error                           { return b.healthErr }
func (b *fakeHostRuntimeBackend) Restore(_ context.Context, ref string) error {
	b.active = b.snapshots[ref]
	return nil
}
func (b *fakeHostRuntimeBackend) Stop(context.Context) error {
	b.active = ""
	return nil
}
func (*fakeHostRuntimeBackend) VerifyStopped(context.Context) error { return nil }

func hostGenerationFixture(t *testing.T, now time.Time) lifecycle.Generation {
	t.Helper()
	generation, err := lifecycle.NewGeneration(lifecycle.GenerationSpec{
		Version:          "1.2.3",
		ReleasedAt:       now.Add(-time.Hour),
		HostBinaryDigest: "sha256:" + strings.Repeat("a", 64),
		CoreImageDigest:  "sha256:" + strings.Repeat("b", 64),
		ConfigSchema:     1,
		PolicySchema:     1,
		ToolchainSchema:  1,
		StateSchema:      1,
		Reads: lifecycle.Compatibility{
			Config:    lifecycle.SchemaRange{Min: 1, Max: 1},
			Policy:    lifecycle.SchemaRange{Min: 1, Max: 1},
			Toolchain: lifecycle.SchemaRange{Min: 1, Max: 1},
			State:     lifecycle.SchemaRange{Min: 1, Max: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return generation
}

func releaseManifestFixture(t *testing.T, now time.Time) releases.ReleaseManifest {
	t.Helper()
	generation, err := releases.NewGeneration(releases.GenerationSpec{
		Version:          "1.2.3",
		ReleasedAt:       now.Add(-time.Hour),
		HostBinaryDigest: "sha256:" + strings.Repeat("a", 64),
		CoreImageDigest:  "sha256:" + strings.Repeat("b", 64),
		ConfigSchema:     1,
		PolicySchema:     1,
		ToolchainSchema:  1,
		StateSchema:      1,
		Reads: releases.Compatibility{
			Config:    releases.SchemaRange{Min: 1, Max: 1},
			Policy:    releases.SchemaRange{Min: 1, Max: 1},
			Toolchain: releases.SchemaRange{Min: 1, Max: 1},
			State:     releases.SchemaRange{Min: 1, Max: 1},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	target := func(path, character string) releases.TargetDescriptor {
		return releases.TargetDescriptor{Path: path, Length: 1, SHA256: strings.Repeat(character, 64)}
	}
	manifest, err := releases.NewReleaseManifest(releases.ReleaseManifest{
		Version:          releases.ReleaseManifestVersion,
		Generation:       generation,
		HostBinary:       target("releases/bin/loki-1.2.3-linux-amd64", "a"),
		HostAssets:       target("releases/assets/loki-host-1.2.3.tar.gz", "d"),
		ToolchainCatalog: target("toolchains/catalog-1.2.3.json", "e"),
		Provenance:       target("releases/provenance/loki-1.2.3.intoto.jsonl", "f"),
		Notices:          target("releases/notices/loki-1.2.3.txt", "1"),
		ReleaseNotes:     target("releases/notes/loki-1.2.3.md", "2"),
		SupportedHosts: []releases.SupportedHost{{
			Environment: "native", Distribution: "ubuntu", Version: "24.04", Arch: "amd64",
		}},
		Runtime: releases.RuntimeRequirements{DockerMin: "27.0.0", ComposeMin: "2.30.0"},
	})
	if err != nil {
		t.Fatal(err)
	}
	return manifest
}

func TestHostInstallOptionParsing(t *testing.T) {
	var stderr bytes.Buffer
	options, err := parseHostInstallOptions([]string{
		"--system", "--workspace", "/srv/workspace", "--state-root", "/var/lib/loki/test-lifecycle",
	}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if !options.System || options.Workspace != "/srv/workspace" || options.StateRoot != "/var/lib/loki/test-lifecycle" {
		t.Fatalf("install options = %#v", options)
	}
	if _, err = parseHostInstallOptions([]string{"--workspace", "relative"}, &stderr); err == nil {
		t.Fatal("relative workspace was accepted")
	}
	interactive, err := parseHostInstallOptions(nil, &stderr)
	if err != nil || interactive.Workspace != "" {
		t.Fatalf("interactive install options = %#v err=%v", interactive, err)
	}
	approved, err := parseHostInstallOptions([]string{
		"--workspace", "/srv/workspace",
		"--create-workspace", "--prepare-workspace", "--allow-sudo-workspace",
		"--install-prerequisites", "--allow-sudo-docker",
	}, &stderr)
	if err != nil || !approved.CreateWorkspace || !approved.PrepareWorkspace || !approved.AllowSudoWorkspace ||
		!approved.InstallPrerequisites || !approved.AllowSudoDocker {
		t.Fatalf("approved install options = %#v err=%v", approved, err)
	}
}

func TestRunHostInstallWithInitializesTransactionalState(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	previousNow := lifecycleTimeNow
	lifecycleTimeNow = func() time.Time { return now }
	defer func() { lifecycleTimeNow = previousNow }()

	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(workspace, "keep.txt"), []byte("keep"), 0600); err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(t.TempDir(), "state")
	candidate := hostGenerationFixture(t, now)
	backend := &fakeHostRuntimeBackend{}
	var stdout, stderr bytes.Buffer
	code := runHostInstallWith(t.Context(), hostInstallOptions{
		StateRoot:    stateRoot,
		Workspace:    workspace,
		DockerAccess: hostDockerAccessSudo,
	}, candidate, backend, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "Loki installed successfully.") {
		t.Fatalf("install code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
	store, err := lifecycle.OpenFileStore(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if backend.active != candidate.ID || snapshot.Installed == nil || snapshot.Installed.ID != candidate.ID ||
		snapshot.Installation == nil || snapshot.Installation.Scope != "user" ||
		snapshot.Installation.Workspace != workspace || snapshot.Installation.DockerAccess != hostDockerAccessSudo {
		t.Fatalf("installed snapshot=%#v backend=%q", snapshot, backend.active)
	}
	if raw, err := os.ReadFile(filepath.Join(workspace, "keep.txt")); err != nil || string(raw) != "keep" {
		t.Fatalf("workspace content=%q err=%v", raw, err)
	}
}

func TestRunHostInstallWithRetriesAfterFailedRuntimeApply(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	previousNow := lifecycleTimeNow
	lifecycleTimeNow = func() time.Time { return now }
	defer func() { lifecycleTimeNow = previousNow }()

	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(t.TempDir(), "state")
	candidate := hostGenerationFixture(t, now)
	backend := &fakeHostRuntimeBackend{healthErr: context.DeadlineExceeded}
	var stdout, stderr bytes.Buffer
	options := hostInstallOptions{StateRoot: stateRoot, Workspace: workspace}
	if code := runHostInstallWith(t.Context(), options, candidate, backend, &stdout, &stderr); code != 1 {
		t.Fatalf("failed install code=%d stderr=%q", code, stderr.String())
	}
	store, err := lifecycle.OpenFileStore(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Installed != nil || snapshot.Available == nil || snapshot.Available.ID != candidate.ID ||
		snapshot.Installation == nil || snapshot.Installation.Workspace != workspace {
		t.Fatalf("retryable install state = %#v", snapshot)
	}

	backend.healthErr = nil
	stdout.Reset()
	stderr.Reset()
	if code := runHostInstallWith(t.Context(), options, candidate, backend, &stdout, &stderr); code != 0 {
		t.Fatalf("retry install code=%d stderr=%q", code, stderr.String())
	}
	snapshot, err = store.Snapshot(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if snapshot.Installed == nil || snapshot.Installed.ID != candidate.ID {
		t.Fatalf("installed state after retry = %#v", snapshot)
	}
}

func TestBootstrapReleaseManifestLoadsLifecycleGeneration(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	manifest := releaseManifestFixture(t, now)
	raw, err := releases.EncodeReleaseManifest(manifest)
	if err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(t.TempDir(), "release-manifest.json")
	if err = os.WriteFile(path, raw, 0600); err != nil {
		t.Fatal(err)
	}
	loaded, err := loadBootstrapHostRelease(path)
	if err != nil {
		t.Fatal(err)
	}
	decoded := loaded.Generation
	if decoded.ID != manifest.Generation.ID || decoded.Spec.HostBinaryDigest != manifest.Generation.Spec.HostBinaryDigest {
		t.Fatalf("decoded generation = %#v", decoded)
	}
	if loaded.Runtime != manifest.Runtime {
		t.Fatalf("runtime requirements = %#v, want %#v", loaded.Runtime, manifest.Runtime)
	}
	if _, err = loadBootstrapHostGeneration(""); err == nil {
		t.Fatal("missing verified release manifest was accepted")
	}
	if err = os.Chmod(path, 0644); err != nil {
		t.Fatal(err)
	}
	if _, err = loadBootstrapHostGeneration(path); err == nil {
		t.Fatal("public verified release manifest was accepted")
	}
}

func TestDefaultHostStateRootUsesInstallationScope(t *testing.T) {
	t.Setenv("XDG_STATE_HOME", "/tmp/loki-host-state")
	userRoot, err := defaultHostStateRoot(false)
	if err != nil {
		t.Fatal(err)
	}
	if userRoot != "/tmp/loki-host-state/loki/lifecycle" {
		t.Fatalf("user lifecycle root = %q", userRoot)
	}
	systemRoot, err := defaultHostStateRoot(true)
	if err != nil {
		t.Fatal(err)
	}
	if systemRoot != "/var/lib/loki/lifecycle" {
		t.Fatalf("system lifecycle root = %q", systemRoot)
	}
}

func TestRunHostUpdateStatusSupportsUserScopedState(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	stateRoot := filepath.Join(t.TempDir(), "state")
	store, err := lifecycle.EnsureFileStore(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err = os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	candidate := hostGenerationFixture(t, now)
	if err = store.InitializeInstall(t.Context(), candidate, lifecycle.InstallationState{
		Scope: "user", Workspace: workspace,
	}, now); err != nil {
		t.Fatal(err)
	}
	var stdout, stderr bytes.Buffer
	code := runHost([]string{"update", "status", "--state-root", stateRoot}, &stdout, &stderr)
	if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "\"update_available\":true") {
		t.Fatalf("user status code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

type staticHostJobs struct {
	jobs []string
	err  error
}

func (j staticHostJobs) ActiveJobs(context.Context) ([]string, error) {
	return append([]string(nil), j.jobs...), j.err
}

func TestRunHostMaintenanceBlocksActiveJobsByDefault(t *testing.T) {
	var stdout, stderr bytes.Buffer
	code := runHostMaintenanceWith(
		t.Context(),
		lifecycle.Manager{
			Jobs:       staticHostJobs{jobs: []string{"job-b", "job-a", "job-a"}},
			Maintainer: &lifecycle.TransactionEngine{},
		},
		"backup",
		hostMaintenanceOptions{},
		&stdout,
		&stderr,
	)
	if code != 1 || !strings.Contains(stderr.String(), "job-a, job-b") || stdout.Len() != 0 {
		t.Fatalf("maintenance code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestRunHostMaintenanceBacksUpInstalledState(t *testing.T) {
	now := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	previousNow := lifecycleTimeNow
	lifecycleTimeNow = func() time.Time { return now }
	defer func() { lifecycleTimeNow = previousNow }()

	workspace := filepath.Join(t.TempDir(), "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(t.TempDir(), "state")
	candidate := hostGenerationFixture(t, now)
	backend := &fakeHostRuntimeBackend{}
	var installOut, installErr bytes.Buffer
	if code := runHostInstallWith(t.Context(), hostInstallOptions{
		StateRoot: stateRoot, Workspace: workspace,
	}, candidate, backend, &installOut, &installErr); code != 0 {
		t.Fatalf("install code=%d stderr=%q", code, installErr.String())
	}
	store, err := lifecycle.OpenFileStore(stateRoot)
	if err != nil {
		t.Fatal(err)
	}
	engine := &lifecycle.TransactionEngine{Store: store, Backend: backend, Now: lifecycleTimeNow}
	manager := lifecycle.Manager{Store: store, Jobs: staticHostJobs{}, Maintainer: engine, Now: lifecycleTimeNow}
	var stdout, stderr bytes.Buffer
	code := runHostMaintenanceWith(
		t.Context(), manager, "backup", hostMaintenanceOptions{}, &stdout, &stderr,
	)
	if code != 0 || stderr.Len() != 0 || !strings.Contains(stdout.String(), "\"runtime_ref\":") {
		t.Fatalf("backup code=%d stdout=%q stderr=%q", code, stdout.String(), stderr.String())
	}
}

func TestHostMaintenanceOptionParsing(t *testing.T) {
	var stderr bytes.Buffer
	options, err := parseHostMaintenanceOptions("restore", []string{
		"--system", "--state-root", "/var/lib/loki/lifecycle", "sha256:" + strings.Repeat("a", 64),
	}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if !options.System || options.StateRoot != "/var/lib/loki/lifecycle" ||
		options.RestoreBackupID != "sha256:"+strings.Repeat("a", 64) {
		t.Fatalf("maintenance options = %#v", options)
	}
	if _, err = parseHostMaintenanceOptions("restore", nil, &stderr); err == nil {
		t.Fatal("restore without backup id was accepted")
	}
	enable, err := parseHostMaintenanceOptions("enable", []string{"browser"}, &stderr)
	if err != nil {
		t.Fatal(err)
	}
	if enable.Component != "browser" {
		t.Fatalf("enable options = %#v", enable)
	}
	if _, err = parseHostMaintenanceOptions("disable", nil, &stderr); err == nil {
		t.Fatal("disable without component was accepted")
	}
}

func TestLauncherJournalInventoryReadsActiveJobsWithoutWriterOwnership(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "launcher")
	journalRoot := filepath.Join(dir, "journal")
	if err := os.MkdirAll(journalRoot, 0700); err != nil {
		t.Fatal(err)
	}
	limits := jobs.JournalLimits{
		MaxRecords: 8, MaxRecordBytes: 128 << 10, MaxOutputBytes: 4096, Retention: time.Minute,
	}
	journal, err := jobs.OpenJournal(journalRoot, limits)
	if err != nil {
		t.Fatal(err)
	}
	defer journal.Close()
	now := time.Now().UTC()

	runningID := strings.Repeat("a", 32)
	if _, err = journal.Admit(runningID, "oci:"+strings.Repeat("b", 64), now.Add(time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.BindInstance(runningID, "oci-instance-sha256:"+strings.Repeat("1", 64), now.Add(time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.MarkRunning(runningID, now.Add(2*time.Nanosecond)); err != nil {
		t.Fatal(err)
	}

	doneID := strings.Repeat("c", 32)
	if _, err = journal.Admit(doneID, "oci:"+strings.Repeat("d", 64), now.Add(time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.MarkTerminal(doneID, jobs.Result{
		Outcome: jobs.OutcomeLaunchFailed, Cleanup: jobs.CleanupNotRequired,
	}, now.Add(3*time.Nanosecond)); err != nil {
		t.Fatal(err)
	}

	cleanupID := strings.Repeat("e", 32)
	if _, err = journal.Admit(cleanupID, "oci:"+strings.Repeat("f", 64), now.Add(time.Minute), now); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.BindInstance(cleanupID, "oci-instance-sha256:"+strings.Repeat("2", 64), now.Add(3*time.Nanosecond)); err != nil {
		t.Fatal(err)
	}
	if _, err = journal.MarkTerminal(cleanupID, jobs.Result{
		Outcome: jobs.OutcomeLaunchFailed, Cleanup: jobs.CleanupPending,
	}, now.Add(4*time.Nanosecond)); err != nil {
		t.Fatal(err)
	}

	layoutPath := filepath.Join(t.TempDir(), "launcher.json")
	raw, err := json.Marshal(map[string]any{
		"StateDirectory":         dir,
		"MaxJobs":                limits.MaxRecords,
		"MaxConcurrentJobs":      1,
		"MaxOutputBytes":         limits.MaxOutputBytes,
		"ResultRetentionSeconds": 60,
	})
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(layoutPath, raw, 0600); err != nil {
		t.Fatal(err)
	}
	if err = os.Chmod(layoutPath, 0600); err != nil {
		t.Fatal(err)
	}

	inventory := launcherJournalInventory{LayoutPath: layoutPath}
	active, err := inventory.ActiveJobs(t.Context())
	if err != nil {
		t.Fatal(err)
	}
	if !reflect.DeepEqual(active, []string{runningID, cleanupID}) {
		t.Fatalf("active jobs = %#v", active)
	}
	if _, err = jobs.OpenJournal(journalRoot, limits); err == nil || !strings.Contains(err.Error(), "already owned") {
		t.Fatalf("inventory disturbed launcher journal ownership: %v", err)
	}
}
