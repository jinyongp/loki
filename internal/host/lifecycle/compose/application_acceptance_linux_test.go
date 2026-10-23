//go:build linux

package compose

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"

	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/host/lifecycle"
	"loki/internal/platform/safeio"
)

func verifyRealComposeMigrationPreparation(t *testing.T, ctx context.Context, b *Backend, state runtimeState, image string) lifecycle.SecretMigrationIntent {
	t.Helper()
	runner := ExecRunner{}
	builder := os.Getenv("LOKI_TEST_MIGRATION_SOURCE_BUILDER")
	if !filepath.IsAbs(builder) {
		t.Fatal("private source fixture builder is required")
	}
	volume := b.volumeName("runtime-state")
	if _, err := runner.Run(ctx, nil, "volume", "create", "--label", "com.docker.compose.project="+b.project, "--label", "com.docker.compose.volume=runtime-state", volume); err != nil {
		t.Fatal(err)
	}
	args := []string{"run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--security-opt", "no-new-privileges:true", "--user", "0:0", "--log-driver", "none", "--mount", applicationBind(builder, "/fixture/source-builder", true), "--mount", "type=volume,source=" + volume + ",target=/var/lib/loki/runtime,volume-nocopy", "--entrypoint", "/fixture/source-builder", image, "seed-compose"}
	if _, err := runner.RunPrivate(ctx, nil, nil, args...); err != nil {
		t.Fatal("actual encrypted source fixture preparation", err)
	}
	key, err := age.GenerateX25519Identity()
	if err != nil {
		t.Fatal(err)
	}
	resource := controlpolicy.ApplicationResource{ID: "9af6c2e6-c1d4-41a3-a6e9-9fb3c07c67d2", Alias: "fixture", Repository: "repo:fixture", Environments: []string{"common"}, Keys: map[string]string{"TOKEN": "secret", "API_URL": "variable"}, Network: "none", Principals: []controlpolicy.ApplicationGrant{{Principal: "agent", Metadata: true, Execute: true}}}
	mappings := []appvalues.MigrationProfileMapping{{Profile: "legacy-app", Resource: resource.ID, Environment: "common", Keys: []appvalues.MigrationKeyMapping{{Source: "TOKEN", Name: "TOKEN", Kind: "secret"}, {Source: "API_URL", Name: "API_URL", Kind: "variable"}}}}
	archive := filepath.Join(b.root, "synthetic-source.age")
	intent, err := b.PrepareSecretMigration(ctx, mappings, []controlpolicy.ApplicationResource{resource}, key.Recipient().String(), archive, false)
	if err != nil || !intent.Valid() || intent.Plan.Values != 2 || intent.ContainerImage != image || intent.ContainerGeneration != state.GenerationID || state.ApplicationStateSource != "" && intent.Before.ActiveGeneration != filepath.Base(state.ApplicationStateSource) {
		t.Fatal("real Compose source/mapping metadata preparation", err)
	}
	if _, err := os.Lstat(archive); !os.IsNotExist(err) {
		t.Fatal("prepare created a source backup before maintenance admission", err)
	}
	public, _ := json.Marshal(intent)
	if bytes.Contains(public, []byte("public.example.invalid")) {
		t.Fatal("source value appeared in public preparation")
	}
	lock, err := lifecycle.AcquireOperationLock(b.root)
	if err != nil {
		t.Fatal("prepare retained its host lock", err)
	}
	j, err := lifecycle.OpenOperationJournal(b.root, lock, lifecycle.OperationJournalOptions{})
	if err != nil {
		lock.Close()
		t.Fatal(err)
	}
	items, err := j.List()
	lock.Close()
	if err != nil || len(items) != 0 {
		t.Fatal("prepare admitted a host operation", err)
	}
	args = []string{"run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--user", "0:0", "--mount", "type=volume,source=" + b.volumeName("application-state") + ",target=/fixture/app,readonly,volume-nocopy", "--entrypoint", "/bin/sh", image, "-ec", "test ! -e /fixture/app/generations/" + intent.CandidateGeneration}
	if _, err := runner.RunPrivate(ctx, nil, nil, args...); err != nil {
		t.Fatal("prepare materialized an inactive generation", err)
	}
	return intent
}

func TestRealComposeMigrationPreparationWithoutRunningServices(t *testing.T) {
	image := os.Getenv("LOKI_TEST_NATIVE_SECRET_IMAGE")
	if image == "" {
		if os.Getenv("LOKI_REQUIRE_NATIVE_SECRET_COMPOSE") == "1" {
			t.Fatal("native shipping image is required")
		}
		t.Skip("native packaging suite supplies the shipping image")
	}
	if !strings.Contains(image, "@sha256:") {
		t.Fatal("immutable image is required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), time.Minute)
	defer cancel()
	root := t.TempDir()
	sum := sha256.Sum256([]byte(root))
	b, err := New(Config{StateRoot: filepath.Join(root, "operator"), Project: "loki-prepare-" + hex.EncodeToString(sum[:6]), Runner: ExecRunner{}})
	if err != nil {
		t.Fatal(err)
	}
	runner := ExecRunner{}
	volume := b.volumeName("application-state")
	if _, err := runner.Run(ctx, nil, "volume", "create", "--label", "io.loki.application.owner=loki-tools-"+applicationOwnerID(b.root), volume); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		for _, name := range []string{"runtime-state", "application-state"} {
			if _, err := runner.Run(cleanup, nil, "volume", "rm", b.volumeName(name)); err != nil {
				t.Error("owned preparation fixture cleanup", err)
			}
		}
	})
	script := `set -eu
umask 077
printf '{"resources":[]}' > /run/preparation/catalog.json
exec /opt/loki/bin/loki host prepare-secrets --state-only --uid 10006 --gid 10006 --agent-uid 10000 --metadata-gid 10001 --identity /var/lib/loki/app-store/deployment.json --state /var/lib/loki/app-store --service-state /var/lib/loki/secrets --runtime /run/loki/secrets --config /etc/loki/loki.toml --catalog /run/preparation/catalog.json --execution-contract /usr/share/doc/loki/container-execution-contract.json --unit-directory= --overlay-directory=`
	args := []string{"run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--cap-add", "CHOWN", "--cap-add", "FOWNER", "--cap-add", "DAC_OVERRIDE", "--security-opt", "no-new-privileges:true", "--user", "0:0", "--log-driver", "none", "--tmpfs", "/run/preparation:rw,nosuid,nodev,mode=0700,size=2097152", "--mount", "type=volume,source=" + volume + ",target=/var/lib/loki/app-store,volume-nocopy", "--entrypoint", "/bin/sh", image, "-ec", script}
	body, err := runner.RunPrivate(ctx, nil, nil, args...)
	var prepared struct{ Policy string }
	if err != nil || json.Unmarshal(body, &prepared) != nil || !validSHA256(prepared.Policy) {
		t.Fatal("actual persistent-only preparation failed", err)
	}
	state := runtimeState{Version: 1, GenerationID: "sha256:" + strings.Repeat("a", 64), CoreImage: image, Workspace: root, ApplicationPolicySHA256: prepared.Policy}
	if err := b.saveRuntime(state); err != nil {
		t.Fatal(err)
	}
	verifyRealComposeMigrationPreparation(t, ctx, b, state, image)
}

func TestRealComposeApplicationService(t *testing.T) {
	image := os.Getenv("LOKI_TEST_NATIVE_SECRET_IMAGE")
	if image == "" {
		if os.Getenv("LOKI_REQUIRE_NATIVE_SECRET_COMPOSE") == "1" {
			t.Fatal("native shipping image is required")
		}
		t.Skip("native packaging suite supplies the shipping image")
	}
	if !strings.Contains(image, "@sha256:") {
		t.Fatal("an immutable fixture image is required")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 150*time.Second)
	defer cancel()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	if err := os.Mkdir(workspace, 0700); err != nil {
		t.Fatal(err)
	}
	stateRoot := filepath.Join(root, "operator")
	sum := sha256.Sum256([]byte(root))
	project := "loki-secret-" + hex.EncodeToString(sum[:6])
	runner := ExecRunner{}
	b, err := New(Config{StateRoot: stateRoot, Project: project, Runner: runner})
	if err != nil {
		t.Fatal(err)
	}
	state := runtimeState{Version: 1, GenerationID: "sha256:" + strings.Repeat("a", 64), CoreImage: image, Workspace: workspace}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if _, err := b.compose(cleanup, state, "down", "--volumes", "--remove-orphans"); err != nil {
			t.Error("fixture Compose cleanup failed", err)
		}
		if _, err := runner.Run(cleanup, nil, "volume", "rm", b.volumeName("application-state")); err != nil {
			t.Error("fixture app volume cleanup failed", err)
		}
		if state.ApplicationRuntimeRoot != "" {
			if _, err := runner.Run(cleanup, nil, "run", "--rm", "--network", "none", "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--cap-add", "FOWNER", "--user", "0:0", "--mount", applicationBind(state.ApplicationRuntimeRoot, "/fixture", false), "--entrypoint", "/bin/sh", image, "-ec", "find /fixture -mindepth 1 -maxdepth 1 -exec rm -rf {} +"); err != nil {
				t.Error("fixture tmpfs cleanup failed", err)
			}
			if err := os.Remove(state.ApplicationRuntimeRoot); err != nil {
				t.Error(err)
			}
			if err := os.Remove(filepath.Dir(state.ApplicationRuntimeRoot)); err != nil {
				t.Error(err)
			}
		}
	})
	operation := "123e4567-e89b-12d3-a456-426614174399"
	if err := b.BeginJobMaintenance(ctx, operation, true); err != nil {
		t.Fatal(err)
	}
	if err := b.prepareApplicationValues(ctx, &state); err != nil {
		t.Fatal(err)
	}
	if err := b.saveRuntime(state); err != nil {
		t.Fatal(err)
	}
	if _, err := b.compose(ctx, state, "up", "--detach", "--wait", "--wait-timeout", "45", "application-values"); err != nil {
		t.Fatal(err)
	}
	idRaw, err := b.compose(ctx, state, "ps", "--quiet", "application-values")
	if err != nil {
		t.Fatal(err)
	}
	id := strings.TrimSpace(string(idRaw))
	raw, err := runner.Run(ctx, nil, "inspect", id)
	if err != nil {
		t.Fatal(err)
	}
	var containers []struct {
		Config     struct{ User string }
		HostConfig struct {
			NetworkMode    string
			CapAdd         []string
			ReadonlyRootfs bool
			Memory         int64
			PidsLimit      *int64
		}
		Mounts []struct{ Source, Destination string }
	}
	if json.Unmarshal(raw, &containers) != nil || len(containers) != 1 {
		t.Fatal("invalid container inspection")
	}
	c := containers[0]
	if c.Config.User != "10006:10006" || c.HostConfig.NetworkMode != "none" || len(c.HostConfig.CapAdd) != 0 || !c.HostConfig.ReadonlyRootfs {
		t.Fatal("broker identity or isolation changed")
	}
	if c.HostConfig.Memory != 1<<30 || c.HostConfig.PidsLimit == nil || *c.HostConfig.PidsLimit != 64 {
		t.Fatal("broker no longer has the planned eight-Job memory/process budget")
	}
	for _, mount := range c.Mounts {
		if mount.Destination == "/workspace" || strings.Contains(mount.Destination, "docker.sock") || mount.Destination == "/var/lib/loki/runtime" || mount.Destination == "/var/lib/loki/app-store" {
			t.Fatal("broker acquired foreign authority")
		}
	}
	t.Run("prepare legacy migration through actual private helpers", func(t *testing.T) {
		verifyRealComposeMigrationPreparation(t, ctx, b, state, image)
	})
	admin := func(action string, input []byte) map[string]any {
		t.Helper()
		layout := filepath.Join(filepath.Dir(filepath.Dir(state.ApplicationStateSource)), "secrets-admin.json")
		args := []string{"run", "--rm", "--interactive", "--network", "none", "--read-only", "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--user", "0:0", "--mount", applicationBind(filepath.Join(state.ApplicationRuntimeRoot, "admin"), "/run/loki/secrets/admin", false), "--mount", applicationBind(layout, "/fixture/admin.json", true), "--entrypoint", "/opt/loki/bin/loki", image, "secret", "protection", action, "--layout", "/fixture/admin.json"}
		if action == "revision" {
			args = args[:len(args)-5]
			args = append(args, "secret", "app", "revision", "fixture", "--layout", "/fixture/admin.json")
		}
		if input != nil {
			args = append(args, "--stdin")
		}
		raw, err := runner.RunPrivate(ctx, nil, bytes.NewReader(input), args...)
		if err != nil {
			t.Fatal("private protection helper failed", action)
		}
		var result map[string]any
		if json.Unmarshal(raw, &result) != nil {
			t.Fatal("invalid protection metadata")
		}
		return result
	}
	status := admin("status", nil)
	if status["state"] != "uninitialized" {
		t.Fatal("installation initialized protection implicitly")
	}
	passphrase := []byte("synthetic-owned-compose-fixture-passphrase")
	defer clear(passphrase)
	admin("init", passphrase)
	admin("unlock", passphrase)
	if admin("status", nil)["state"] != "unlocked" {
		t.Fatal("manual unlock did not open the synthetic vault")
	}
	if _, err := b.compose(ctx, state, "restart", "application-values"); err != nil {
		t.Fatal(err)
	}
	if _, err := b.compose(ctx, state, "up", "--detach", "--wait", "--wait-timeout", "30", "application-values"); err != nil {
		t.Fatal(err)
	}
	status = admin("status", nil)
	if status["state"] != "locked" {
		t.Fatal("restart retained an unlocked session")
	}
	if _, err := b.compose(ctx, state, "up", "--detach", "--wait", "--wait-timeout", "40", "launcher"); err != nil {
		t.Fatal(err)
	}
	fence, err := b.maintenanceCall(ctx, state, "status", "", false, false)
	if err != nil || fence == nil || !fence.Active || fence.OperationID != operation || !fence.Interrupt {
		t.Fatal("fresh launcher did not inherit the host operation", err)
	}
	if err := b.BeginJobMaintenance(ctx, operation, true); err != nil {
		t.Fatal(err)
	}
	if err := b.EndJobMaintenance(ctx, operation); err != nil {
		t.Fatal(err)
	}
	operation = "123e4567-e89b-12d3-a456-426614174388"
	if err := b.BeginJobMaintenance(ctx, operation, false); err != nil {
		t.Fatal(err)
	}
	if _, err := b.compose(ctx, state, "stop", "--timeout", "5", "launcher"); err != nil {
		t.Fatal(err)
	}
	if err := b.EndJobMaintenance(ctx, operation); err != nil {
		t.Fatal("offline host fence release failed", err)
	}
	if live, err := b.launcherRunning(ctx, state); err != nil || live {
		t.Fatal("offline helper restarted the launcher", err)
	}
	// Change public grants through the same frozen role preparation used by
	// Restart. Existing ciphertext/generation and the manual unlock credential
	// must survive; a process must read the new policy after its actual restart.
	if _, err := b.compose(ctx, state, "up", "--detach", "--wait", "--wait-timeout", "40", "launcher"); err != nil {
		t.Fatal(err)
	}
	operation = "123e4567-e89b-12d3-a456-426614174377"
	if err := b.BeginJobMaintenance(ctx, operation, false); err != nil {
		t.Fatal(err)
	}
	if _, err := b.compose(ctx, state, "stop", "--timeout", "5", "application-values", "launcher"); err != nil {
		t.Fatal(err)
	}
	resource := controlpolicy.ApplicationResource{ID: "9af6c2e6-c1d4-41a3-a6e9-9fb3c07c67d2", Alias: "fixture", Repository: "repo:fixture", Environments: []string{"common"}, Keys: map[string]string{"TOKEN": "secret"}, Network: "none", Principals: []controlpolicy.ApplicationGrant{{Principal: "agent", Metadata: true}}}
	catalog, _ := json.Marshal(map[string]any{"resources": []controlpolicy.ApplicationResource{resource}})
	if err := safeio.PublishPrivate(filepath.Join(b.root, "application-resources.json"), catalog, true); err != nil {
		t.Fatal(err)
	}
	previousPolicy, previousGeneration := state.ApplicationPolicySHA256, state.ApplicationStateSource
	if err := b.prepareApplicationValues(ctx, &state); err != nil {
		t.Fatal(err)
	}
	if state.ApplicationPolicySHA256 == previousPolicy || state.ApplicationStateSource != previousGeneration {
		t.Fatal("resource policy change lost the active encrypted generation or kept stale grants")
	}
	if err := b.saveRuntime(state); err != nil {
		t.Fatal(err)
	}
	b.ExpectedApplicationPolicy = previousPolicy
	if err := b.ValidateApplicationPolicy(ctx); err == nil {
		t.Fatal("stale prepared role digest passed the commit guard")
	}
	b.ExpectedApplicationPolicy = state.ApplicationPolicySHA256
	if err := b.ValidateApplicationPolicy(ctx); err != nil {
		t.Fatal(err)
	}
	if _, err := b.compose(ctx, state, "up", "--detach", "--wait", "--wait-timeout", "40", "application-values", "launcher"); err != nil {
		t.Fatal(err)
	}
	if admin("status", nil)["state"] != "locked" {
		t.Fatal("policy replacement reused a live key session")
	}
	admin("unlock", passphrase)
	if result := admin("revision", nil); result["resource"] != "fixture" || result["revision"] == "" || result["revision"] == nil {
		t.Fatal("restarted broker did not load the new resource policy")
	}
	if err := b.EndJobMaintenance(ctx, operation); err != nil {
		t.Fatal(err)
	}
}
