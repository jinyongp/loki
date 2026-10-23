//go:build linux

package compose

import (
	"bytes"
	"context"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"filippo.io/age"
	"loki/internal/control/appvalues"
	"loki/internal/host/lifecycle"
)

func fixtureApplicationAdmin(t *testing.T, ctx context.Context, b *Backend, state runtimeState, input []byte, arguments ...string) map[string]any {
	t.Helper()
	layout := filepath.Join(filepath.Dir(filepath.Dir(state.ApplicationStateSource)), "secrets-admin.json")
	args := []string{"run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--security-opt", "no-new-privileges:true", "--user", "0:0", "--memory", "134217728", "--pids-limit", "32", "--log-driver", "none", "--entrypoint", "/opt/loki/bin/loki", "--mount", applicationBind(filepath.Join(state.ApplicationRuntimeRoot, "admin"), "/run/loki/secrets/admin", true), "--mount", applicationBind(layout, "/etc/loki/secret-admin.json", true)}
	if input != nil {
		args = append(args, "--interactive")
	}
	args = append(args, state.CoreImage, "secret")
	args = append(args, arguments...)
	args = append(args, "--layout", "/etc/loki/secret-admin.json")
	body, err := b.runner.(PrivateRunner).RunPrivate(ctx, nil, bytes.NewReader(input), args...)
	var metadata map[string]any
	if err != nil || json.Unmarshal(body, &metadata) != nil {
		t.Fatal("owned fixture administration failed", arguments[0], arguments[1], err)
	}
	return metadata
}

func setFixtureApplicationToken(t *testing.T, ctx context.Context, b *Backend, state runtimeState, value []byte, request string) {
	setFixtureApplicationSecret(t, ctx, b, state, "TOKEN", value, request)
}

func setFixtureApplicationSecret(t *testing.T, ctx context.Context, b *Backend, state runtimeState, name string, value []byte, request string) {
	t.Helper()
	revision := fixtureApplicationAdmin(t, ctx, b, state, nil, "app", "revision", "fixture")["revision"]
	if revision == nil || revision == "" {
		t.Fatal("missing opaque edit revision")
	}
	result := fixtureApplicationAdmin(t, ctx, b, state, value, "app", "set", "fixture", "--name", name, "--replace", "--revision", revision.(string), "--request-id", request, "--stdin")
	if result["changed"] != true {
		t.Fatal("private fixture value was not changed")
	}
}

func verifyRealComposePairedApplicationRestore(t *testing.T, ctx context.Context, b *Backend, binary string, backup lifecycle.BackupRecord, identity *age.X25519Identity, passphrase []byte) {
	t.Helper()
	second := os.Getenv("LOKI_TEST_PAIRED_NATIVE_SECRET_IMAGE")
	probe := os.Getenv("LOKI_TEST_PRIVATE_VALUE_PROBE")
	before, found, err := b.loadRuntime()
	if err != nil || !found || second == before.CoreImage || !pinnedRestoreCandidateImage(second) || !filepath.IsAbs(probe) {
		t.Fatal("distinct native images and private comparison executable required", err)
	}
	store, err := lifecycle.OpenFileStore(b.root)
	if err != nil {
		t.Fatal(err)
	}
	snapshot, err := store.Snapshot(ctx)
	if err != nil {
		t.Fatal(err)
	}
	spec := snapshot.Installed.Spec
	spec.Version = "0.49.1"
	b.coreRepository, spec.CoreImageDigest, _ = strings.Cut(second, "@")
	candidate, err := lifecycle.NewGeneration(spec)
	if err != nil || candidate.ID == backup.Installed.ID {
		t.Fatal("second paired program identity", err)
	}
	if err := store.SaveAvailable(ctx, candidate); err != nil {
		t.Fatal(err)
	}
	plan, err := lifecycle.Prepare(snapshot.Installed, candidate, snapshot.Host, time.Now())
	if err != nil {
		t.Fatal(err)
	}
	if err := store.SavePrepared(ctx, plan); err != nil {
		t.Fatal(err)
	}
	fixtureApplicationAdmin(t, ctx, b, before, passphrase, "protection", "unlock", "--stdin")
	engine := lifecycle.TransactionEngine{Store: store, Backend: b}
	if _, err := engine.Apply(ctx, lifecycle.ApplyRequest{Plan: plan}); err != nil {
		t.Fatal("actual original-owner program update", err)
	}
	current, found, err := b.loadRuntime()
	if err != nil || !found || current.CoreImage == before.CoreImage || current.GenerationID != candidate.ID {
		t.Fatal("actual distinct program was not activated", err)
	}
	fixtureApplicationAdmin(t, ctx, b, current, passphrase, "protection", "unlock", "--stdin")
	setFixtureApplicationToken(t, ctx, b, current, []byte("synthetic-paired-newer-value-B"), "123e4567-e89b-42d3-a456-426614174492")
	intent, err := b.PrepareApplicationRestore(ctx, backup.ID, false)
	if err != nil || intent.BeforeImage == intent.TargetImage || intent.BeforeProgram == intent.TargetProgram || intent.TargetProgram != backup.Installed.ID {
		t.Fatal("paired restore did not bind both old program and app archive", err)
	}
	planPath := filepath.Join(b.root, "paired-restore-plan.json")
	if err := writePrivateJSON(planPath, intent); err != nil {
		t.Fatal(err)
	}
	invoke := func(action string, input []byte, extra ...string) ApplicationRestoreStatus {
		t.Helper()
		args := append([]string{"host", "application-restore", "compose", action, "--state-root", b.root, "--project", b.project}, extra...)
		command := exec.CommandContext(ctx, binary, args...)
		command.Stdin = bytes.NewReader(input)
		var output, diagnostic bytes.Buffer
		command.Stdout, command.Stderr = &output, &diagnostic
		if err := command.Run(); err != nil {
			t.Fatal("public paired rollback", action, err, diagnostic.String())
		}
		var result ApplicationRestoreStatus
		if json.Unmarshal(output.Bytes(), &result) != nil {
			t.Fatal("invalid paired rollback response")
		}
		return result
	}
	pending := invoke("apply", nil, "--plan", planPath, "--approve")
	if pending.Protection != "uninitialized" || pending.Released {
		t.Fatal("paired restore did not wait for manual protection", pending)
	}
	key := filepath.Join(b.root, "paired-operator-identity")
	if err := os.WriteFile(key, []byte(identity.String()), 0600); err != nil {
		t.Fatal(err)
	}
	defer os.Remove(key)
	pass := []byte("synthetic-paired-candidate-protection")
	defer clear(pass)
	completed := invoke("recover", pass, "--identity-file", key, "--protection", "init", "--stdin")
	if completed.State != lifecycle.OperationSucceeded || !completed.Released || completed.Operation != pending.Operation {
		t.Fatal("paired program/data restore did not finish", completed)
	}
	restored, found, err := b.loadRuntime()
	if err != nil || !found || restored.CoreImage != intent.TargetImage || restored.GenerationID != backup.Installed.ID || filepath.Base(restored.ApplicationStateSource) != intent.CandidateGeneration {
		t.Fatal("paired restore did not select the exact old program and fresh data generation", err)
	}
	body, err := appvalues.EncodeApplicationRestore(pass, []byte("synthetic-paired-backup-value-A"))
	if err != nil {
		t.Fatal(err)
	}
	defer clear(body)
	args := []string{"run", "--rm", "--interactive", "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--user", "10006:10006", "--memory", "268435456", "--pids-limit", "32", "--log-driver", "none", "--tmpfs", "/tmp:rw,nosuid,nodev,size=134217728,uid=10006,gid=10006,mode=0700", "--mount", applicationBind(restored.ApplicationStateSource, "/var/lib/loki/secrets", false), "--mount", applicationBind(probe, "/fixture/private-probe", true), "--entrypoint", "/fixture/private-probe", restored.CoreImage, intent.Backup.Intent.Resources[0].Profile()}
	if _, err := b.runner.(PrivateRunner).RunPrivate(ctx, nil, bytes.NewReader(body), args...); err != nil {
		t.Fatal("restored data did not match original private delivery", err)
	}
}
