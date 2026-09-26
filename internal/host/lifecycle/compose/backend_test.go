package compose

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	"loki/internal/host/lifecycle"
)

type runnerCall struct {
	env  []string
	args []string
}

type fakeRunner struct {
	mu      sync.Mutex
	calls   []runnerCall
	outputs map[string][]byte
	errs    map[string]error
}

func (r *fakeRunner) Run(_ context.Context, env []string, args ...string) ([]byte, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	copiedEnv := append([]string(nil), env...)
	copiedArgs := append([]string(nil), args...)
	r.calls = append(r.calls, runnerCall{env: copiedEnv, args: copiedArgs})
	key := strings.Join(args, "\x00")
	if err := r.errs[key]; err != nil {
		return nil, err
	}
	return append([]byte(nil), r.outputs[key]...), nil
}

func (r *fakeRunner) snapshot() []runnerCall {
	r.mu.Lock()
	defer r.mu.Unlock()
	result := make([]runnerCall, len(r.calls))
	for index, call := range r.calls {
		result[index] = runnerCall{
			env:  append([]string(nil), call.env...),
			args: append([]string(nil), call.args...),
		}
	}
	return result
}

func composeGeneration(t *testing.T, version, digestByte string) lifecycle.Generation {
	t.Helper()
	releasedAt := time.Date(2026, 9, 21, 12, 0, 0, 0, time.UTC)
	digest := func(value string) string { return "sha256:" + strings.Repeat(value, 64) }
	generation, err := lifecycle.NewGeneration(lifecycle.GenerationSpec{
		Version:          version,
		ReleasedAt:       releasedAt,
		HostBinaryDigest: digest("a"),
		CoreImageDigest:  digest(digestByte),
		Components: []lifecycle.Component{
			{Name: "browser", Digest: digest("c"), Optional: true},
			{Name: "signing", Digest: digest("d"), Optional: true},
		},
		ConfigSchema: 1, PolicySchema: 1, ToolchainSchema: 1, StateSchema: 1,
		Reads: lifecycle.Compatibility{
			Config:    lifecycle.SchemaRange{Min: 1, Max: 1},
			Policy:    lifecycle.SchemaRange{Min: 1, Max: 1},
			Toolchain: lifecycle.SchemaRange{Min: 1, Max: 1},
			State:     lifecycle.SchemaRange{Min: 1, Max: 1},
		},
		Rollback: lifecycle.RollbackCoverage{
			StateSnapshot: true, ConfigSnapshot: true,
			OptionalComponentState: []string{"browser", "signing"},
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	return generation
}

func composeBackendFixture(t *testing.T) (*Backend, *fakeRunner, string) {
	t.Helper()
	root := filepath.Join(t.TempDir(), "lifecycle")
	if err := os.Mkdir(root, 0700); err != nil {
		t.Fatal(err)
	}
	runner := &fakeRunner{outputs: map[string][]byte{}, errs: map[string]error{}}
	backend, err := New(Config{StateRoot: root, Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	workspace := filepath.Join(t.TempDir(), "workspace")
	if err = os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	return backend, runner, workspace
}

func TestBackendActivatesCanonicalAssetsAndComposeProfiles(t *testing.T) {
	backend, runner, workspace := composeBackendFixture(t)
	generation := composeGeneration(t, "1.2.3", "b")
	if err := backend.Activate(t.Context(), generation, lifecycle.InstallationState{
		Scope: "user", Workspace: workspace,
	}); err != nil {
		t.Fatal(err)
	}
	state, found, err := backend.loadRuntime()
	if err != nil || !found {
		t.Fatalf("runtime state = %#v found=%v err=%v", state, found, err)
	}
	if state.GenerationID != generation.ID ||
		state.CoreImage != defaultCoreRepository+"@"+generation.Spec.CoreImageDigest ||
		state.BrowserImage != defaultBrowserRepo+"@"+generation.Spec.Components[0].Digest ||
		state.Workspace != workspace || len(state.Profiles) != 0 {
		t.Fatalf("runtime state = %#v", state)
	}
	if err = backend.SetComponent(t.Context(), generation, "browser", true); err != nil {
		t.Fatal(err)
	}
	if err = backend.Restart(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = backend.Health(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = backend.Stop(t.Context()); err != nil {
		t.Fatal(err)
	}
	if err = backend.VerifyStopped(t.Context()); err != nil {
		t.Fatal(err)
	}

	calls := runner.snapshot()
	var sawRestart, sawHealth, sawStop, sawVerify bool
	for _, call := range calls {
		joined := strings.Join(call.args, " ")
		switch {
		case strings.Contains(joined, " up -d --remove-orphans --wait --wait-timeout 60"):
			sawHealth = true
		case strings.Contains(joined, " up -d --remove-orphans"):
			sawRestart = true
		case strings.Contains(joined, " down --remove-orphans"):
			sawStop = true
		case strings.HasSuffix(joined, " ps -q"):
			sawVerify = true
		}
		if strings.Contains(joined, " compose ") {
			if !slices.Contains(call.env, "LOKI_IMAGE="+state.CoreImage) ||
				!slices.Contains(call.env, "LOKI_WORKSPACE="+workspace) ||
				!slices.Contains(call.env, "LOKI_BROWSER_IMAGE="+state.BrowserImage) ||
				!slices.Contains(call.env, "LOKI_MCP_TOKEN_FILE="+backend.containerTokenPath()) {
				t.Fatalf("compose environment = %#v", call.env)
			}
			profileIndex := slices.Index(call.args, "--profile")
			if profileIndex < 0 || profileIndex+1 >= len(call.args) || call.args[profileIndex+1] != "browser" {
				t.Fatalf("compose args = %#v", call.args)
			}
		}
	}
	if !sawRestart || !sawHealth || !sawStop || !sawVerify {
		t.Fatalf("compose calls restart=%v health=%v stop=%v verify=%v: %#v", sawRestart, sawHealth, sawStop, sawVerify, calls)
	}

	for path, wantMode := range map[string]os.FileMode{
		filepath.Join(backend.runtimeRoot, "assets", "compose.yaml"):        0600,
		filepath.Join(backend.runtimeRoot, "assets", "github.compose.toml"): 0644,
		backend.tokenPath():          0600,
		backend.containerTokenPath(): 0444,
	} {
		info, statErr := os.Stat(path)
		if statErr != nil || !info.Mode().IsRegular() || info.Mode().Perm() != wantMode {
			t.Fatalf("runtime asset %s = %v, %v; want %04o", path, info, statErr, wantMode)
		}
	}
	canonicalToken, err := os.ReadFile(backend.tokenPath())
	if err != nil {
		t.Fatal(err)
	}
	containerToken, err := os.ReadFile(backend.containerTokenPath())
	if err != nil {
		t.Fatal(err)
	}
	if !slices.Equal(canonicalToken, containerToken) {
		t.Fatal("container MCP token projection differs from canonical token")
	}
}

func TestBackendDoctorProbeExecutesTrustedLauncherProbe(t *testing.T) {
	backend, runner, workspace := composeBackendFixture(t)
	generation := composeGeneration(t, "1.2.3", "b")
	if err := backend.Activate(t.Context(), generation, lifecycle.InstallationState{
		Scope: "user", Workspace: workspace,
	}); err != nil {
		t.Fatal(err)
	}
	if _, err := backend.DoctorProbe(t.Context()); err != nil {
		t.Fatal(err)
	}
	state, found, err := backend.loadRuntime()
	if err != nil || !found {
		t.Fatalf("runtime state found=%v err=%v", found, err)
	}
	foundProbe := false
	for _, call := range runner.snapshot() {
		joined := strings.Join(call.args, " ")
		if !strings.Contains(joined, " exec -T launcher /opt/loki/bin/loki host runtime-probe ") {
			continue
		}
		foundProbe = true
		if !strings.Contains(joined, "--launcher-layout /etc/loki/launcher.json") ||
			!strings.Contains(joined, "--toolchain-catalog /usr/share/doc/loki/toolchain-catalog.json") {
			t.Fatalf("doctor probe args = %#v", call.args)
		}
		if !slices.Contains(call.env, "LOKI_IMAGE="+state.CoreImage) ||
			!slices.Contains(call.env, "LOKI_MCP_TOKEN_FILE="+backend.containerTokenPath()) {
			t.Fatalf("doctor probe environment = %#v", call.env)
		}
	}
	if !foundProbe {
		t.Fatalf("launcher doctor probe was not executed: %#v", runner.snapshot())
	}
}

func TestBackendSnapshotRestoreWithoutVolumesPreservesRuntimeAndCoverage(t *testing.T) {
	backend, _, workspace := composeBackendFixture(t)
	generation := composeGeneration(t, "1.2.3", "b")
	if err := backend.Activate(t.Context(), generation, lifecycle.InstallationState{
		Scope: "system", Workspace: workspace,
	}); err != nil {
		t.Fatal(err)
	}
	if err := backend.SetComponent(t.Context(), generation, "browser", true); err != nil {
		t.Fatal(err)
	}
	host := lifecycle.HostState{
		ActiveGenerationID: generation.ID,
		ConfigSchema:       1, PolicySchema: 1, ToolchainSchema: 1, StateSchema: 1,
		EnabledComponents: []string{"browser"}, Revision: "revision-1",
	}
	runtime, err := backend.Snapshot(t.Context(), lifecycle.OperationBackup, lifecycle.Snapshot{
		Installed: &generation, Host: host,
		Installation: &lifecycle.InstallationState{Scope: "system", Workspace: workspace},
	})
	if err != nil {
		t.Fatal(err)
	}
	if !filepath.IsAbs(runtime.Ref) ||
		!strings.HasPrefix(runtime.Ref, backend.snapshotRoot+string(filepath.Separator)) ||
		!runtime.Coverage.RuntimeState || !runtime.Coverage.ConfigState ||
		!runtime.Coverage.HostState || !runtime.Coverage.WorkspacePreserved ||
		!slices.Equal(runtime.Coverage.OptionalComponentState, []string{"browser"}) {
		t.Fatalf("runtime snapshot = %#v", runtime)
	}
	if err = backend.SetComponent(t.Context(), generation, "browser", false); err != nil {
		t.Fatal(err)
	}
	if err = backend.Restore(t.Context(), runtime.Ref); err != nil {
		t.Fatal(err)
	}
	state, found, err := backend.loadRuntime()
	if err != nil || !found || !slices.Equal(state.Profiles, []string{"browser"}) {
		t.Fatalf("restored runtime = %#v found=%v err=%v", state, found, err)
	}
}

func TestBackendSnapshotHelpersRunAsRootWithBoundedCapabilities(t *testing.T) {
	backend, runner, workspace := composeBackendFixture(t)
	generation := composeGeneration(t, "1.2.3", "b")
	state, err := backend.stateFor(generation, workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	backupDir := t.TempDir()
	if err = backend.archiveVolume(t.Context(), state, "fixture-volume", backupDir, "fixture.tar"); err != nil {
		t.Fatal(err)
	}
	if err = backend.restoreVolume(t.Context(), state, "fixture-volume", backupDir, "fixture.tar"); err != nil {
		t.Fatal(err)
	}
	calls := runner.snapshot()
	if len(calls) != 2 {
		t.Fatalf("snapshot helper calls = %#v", calls)
	}
	for _, call := range calls {
		joined := strings.Join(call.args, " ")
		if !strings.Contains(joined, "run --rm --network none --user 0:0") ||
			!strings.Contains(joined, "--cap-drop ALL") ||
			!strings.Contains(joined, "--cap-add DAC_OVERRIDE") ||
			!strings.Contains(joined, "--security-opt no-new-privileges") {
			t.Fatalf("snapshot helper authority = %#v", call.args)
		}
	}
}

func TestBackendImportsOfflineLegacyVaultIntoRuntimeVolume(t *testing.T) {
	backend, runner, workspace := composeBackendFixture(t)
	generation := composeGeneration(t, "1.2.3", "b")
	state, err := backend.stateFor(generation, workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = backend.saveRuntime(state); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err = os.Chmod(source, 0700); err != nil {
		t.Fatal(err)
	}
	volume := backend.volumeName("runtime-state")
	volumeKey := strings.Join([]string{
		"volume", "ls", "--quiet", "--filter", "name=^" + volume + "$",
	}, "\x00")
	runner.outputs[volumeKey] = []byte(volume + "\n")
	importArgs := []string{
		"run", "--rm", "--network", "none", "--user", "0:0", "--read-only",
		"--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE",
		"--security-opt", "no-new-privileges",
		"--mount", "type=bind,src=" + source + ",dst=/source,readonly",
		"--mount", "type=volume,src=" + volume + ",dst=/target",
		state.CoreImage,
		"migrate-vault", "import",
		"--source-copy", "/source",
		"--destination", "/target",
		"--existing-destination",
	}
	runner.outputs[strings.Join(importArgs, "\x00")] = []byte("{\"migrated\":true}\n")
	raw, err := backend.ImportLegacyVault(t.Context(), source)
	if err != nil || !strings.Contains(string(raw), "\"migrated\":true") {
		t.Fatalf("legacy import = %q err=%v", raw, err)
	}
	var sawImport, sawStop, sawVerifyStopped, sawRestart, sawHealth bool
	for _, call := range runner.snapshot() {
		joined := strings.Join(call.args, " ")
		switch {
		case slices.Equal(call.args, importArgs):
			sawImport = true
		case strings.Contains(joined, " down --remove-orphans"):
			sawStop = true
		case strings.HasSuffix(joined, " ps -q"):
			sawVerifyStopped = true
		case strings.Contains(joined, " up -d --remove-orphans --wait --wait-timeout 60"):
			sawHealth = true
		case strings.Contains(joined, " up -d --remove-orphans"):
			sawRestart = true
		}
	}
	if !sawImport || !sawStop || !sawVerifyStopped || !sawRestart || !sawHealth {
		t.Fatalf(
			"legacy import calls import=%v stop=%v stopped=%v restart=%v health=%v: %#v",
			sawImport, sawStop, sawVerifyStopped, sawRestart, sawHealth, runner.snapshot(),
		)
	}
	if err = os.Chmod(source, 0755); err != nil {
		t.Fatal(err)
	}
	if _, err = backend.ImportLegacyVault(t.Context(), source); err == nil ||
		!strings.Contains(err.Error(), "private real directory") {
		t.Fatalf("public legacy source accepted: %v", err)
	}
}

func TestBackendLegacyVaultImportFailureRestartsRuntime(t *testing.T) {
	backend, runner, workspace := composeBackendFixture(t)
	generation := composeGeneration(t, "1.2.3", "b")
	state, err := backend.stateFor(generation, workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = backend.saveRuntime(state); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err = os.Chmod(source, 0700); err != nil {
		t.Fatal(err)
	}
	volume := backend.volumeName("runtime-state")
	runner.outputs[strings.Join([]string{
		"volume", "ls", "--quiet", "--filter", "name=^" + volume + "$",
	}, "\x00")] = []byte(volume + "\n")
	importArgs := []string{
		"run", "--rm", "--network", "none", "--user", "0:0", "--read-only",
		"--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE",
		"--security-opt", "no-new-privileges",
		"--mount", "type=bind,src=" + source + ",dst=/source,readonly",
		"--mount", "type=volume,src=" + volume + ",dst=/target",
		state.CoreImage,
		"migrate-vault", "import",
		"--source-copy", "/source",
		"--destination", "/target",
		"--existing-destination",
	}
	runner.errs[strings.Join(importArgs, "\x00")] = errors.New("synthetic legacy import failure")
	if _, err = backend.ImportLegacyVault(t.Context(), source); err == nil ||
		!strings.Contains(err.Error(), "synthetic legacy import failure") {
		t.Fatalf("legacy import failure = %v", err)
	}
	var sawRestart, sawHealth bool
	for _, call := range runner.snapshot() {
		joined := strings.Join(call.args, " ")
		if strings.Contains(joined, " up -d --remove-orphans --wait --wait-timeout 60") {
			sawHealth = true
		} else if strings.Contains(joined, " up -d --remove-orphans") {
			sawRestart = true
		}
	}
	if !sawRestart || !sawHealth {
		t.Fatalf("legacy import failure did not restore runtime process state: %#v", runner.snapshot())
	}
}

func TestBackendLegacyVaultVerifyStoppedFailureRestartsRuntime(t *testing.T) {
	backend, runner, workspace := composeBackendFixture(t)
	generation := composeGeneration(t, "1.2.3", "b")
	state, err := backend.stateFor(generation, workspace, nil)
	if err != nil {
		t.Fatal(err)
	}
	if err = backend.saveRuntime(state); err != nil {
		t.Fatal(err)
	}
	source := t.TempDir()
	if err = os.Chmod(source, 0700); err != nil {
		t.Fatal(err)
	}
	volume := backend.volumeName("runtime-state")
	runner.outputs[strings.Join([]string{
		"volume", "ls", "--quiet", "--filter", "name=^" + volume + "$",
	}, "\x00")] = []byte(volume + "\n")
	psKey := strings.Join([]string{
		"compose", "--project-name", backend.project,
		"--file", filepath.Join(backend.runtimeRoot, "assets", "compose.yaml"),
		"ps", "-q",
	}, "\x00")
	runner.outputs[psKey] = []byte("still-running\n")

	if _, err = backend.ImportLegacyVault(t.Context(), source); err == nil ||
		!strings.Contains(err.Error(), "services are still running") {
		t.Fatalf("verify-stopped failure = %v", err)
	}
	var sawRestart, sawHealth bool
	for _, call := range runner.snapshot() {
		joined := strings.Join(call.args, " ")
		if strings.Contains(joined, " up -d --remove-orphans --wait --wait-timeout 60") {
			sawHealth = true
		} else if strings.Contains(joined, " up -d --remove-orphans") {
			sawRestart = true
		}
	}
	if !sawRestart || !sawHealth {
		t.Fatalf("verify-stopped failure left runtime down: %#v", runner.snapshot())
	}
}

func TestBackendSnapshotFailureResumesStoppedRuntime(t *testing.T) {
	backend, runner, workspace := composeBackendFixture(t)
	generation := composeGeneration(t, "1.2.3", "b")
	if err := backend.Activate(t.Context(), generation, lifecycle.InstallationState{
		Scope: "user", Workspace: workspace,
	}); err != nil {
		t.Fatal(err)
	}
	key := strings.Join([]string{
		"volume", "ls", "--quiet", "--filter", "name=^" + backend.volumeName("launcher-state") + "$",
	}, "\x00")
	runner.errs[key] = errors.New("synthetic volume inspection failure")
	if _, err := backend.Snapshot(t.Context(), lifecycle.OperationApply, lifecycle.Snapshot{
		Installed: &generation,
		Host: lifecycle.HostState{
			ActiveGenerationID: generation.ID,
			ConfigSchema:       1, PolicySchema: 1, ToolchainSchema: 1, StateSchema: 1,
			Revision: "revision-1",
		},
		Installation: &lifecycle.InstallationState{Scope: "user", Workspace: workspace},
	}); err == nil || !strings.Contains(err.Error(), "synthetic volume inspection failure") {
		t.Fatalf("snapshot error = %v", err)
	}
	var sawStop, sawRestart, sawHealth bool
	for _, call := range runner.snapshot() {
		joined := strings.Join(call.args, " ")
		switch {
		case strings.Contains(joined, " stop --timeout 30"):
			sawStop = true
		case strings.Contains(joined, " up -d --remove-orphans --wait --wait-timeout 60"):
			sawHealth = true
		case strings.Contains(joined, " up -d --remove-orphans"):
			sawRestart = true
		}
	}
	if !sawStop || !sawRestart || !sawHealth {
		t.Fatalf("snapshot recovery calls stop=%v restart=%v health=%v: %#v", sawStop, sawRestart, sawHealth, runner.snapshot())
	}
}

func TestBackendRejectsUnsupportedMigrationAndEscapingSnapshot(t *testing.T) {
	backend, _, _ := composeBackendFixture(t)
	if err := backend.Migrate(t.Context(), []lifecycle.MigrationStep{{From: 1, To: 2}}); err == nil {
		t.Fatal("unsupported state migration was accepted")
	}
	if err := backend.Restore(t.Context(), filepath.Join(t.TempDir(), "outside")); err == nil {
		t.Fatal("snapshot outside backend root was accepted")
	}
}

func TestExecRunnerBoundsFailureOutput(t *testing.T) {
	runner := ExecRunner{Executable: "/bin/sh"}
	raw, err := runner.Run(t.Context(), nil, "-c", "printf '%040000d' 0; exit 7")
	if err == nil {
		t.Fatal("failing command returned success")
	}
	if len(raw) != maxCommandOutput {
		t.Fatalf("bounded output bytes = %d, want %d", len(raw), maxCommandOutput)
	}
}

func TestBackendPropagatesRunnerFailure(t *testing.T) {
	backend, runner, workspace := composeBackendFixture(t)
	generation := composeGeneration(t, "1.2.3", "b")
	if err := backend.Activate(t.Context(), generation, lifecycle.InstallationState{
		Scope: "user", Workspace: workspace,
	}); err != nil {
		t.Fatal(err)
	}
	state, _, err := backend.loadRuntime()
	if err != nil {
		t.Fatal(err)
	}
	materialized, err := assetsForTest(backend)
	if err != nil {
		t.Fatal(err)
	}
	args := []string{
		"compose", "--project-name", backend.project, "--file", materialized,
		"up", "-d", "--remove-orphans",
	}
	runner.errs[strings.Join(args, "\x00")] = errors.New("synthetic docker failure")
	if err = backend.Restart(t.Context()); err == nil {
		t.Fatalf("runner failure was not propagated for state %#v", state)
	}
}

func assetsForTest(backend *Backend) (string, error) {
	if err := backend.ensureAssetsAndToken(); err != nil {
		return "", err
	}
	return filepath.Join(backend.runtimeRoot, "assets", "compose.yaml"), nil
}

var _ lifecycle.TransactionBackend = (*Backend)(nil)
var _ Runner = (*fakeRunner)(nil)
