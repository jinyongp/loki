//go:build linux

package migration

import (
	"context"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
	"time"

	"loki/internal/config"
	"loki/internal/control/appvalues"
	"loki/internal/execution"
	hostapplication "loki/internal/host/application"
)

func TestRealNativeCandidateNamespaceAndManualProtection(t *testing.T) {
	provider, broker := os.Getenv("LOKI_SECRET_TEST_DEVTOOLS"), os.Getenv("LOKI_SECRET_TEST_BROKER")
	if os.Geteuid() != 0 || provider == "" || broker == "" {
		if os.Getenv("LOKI_REQUIRE_NATIVE_MIGRATION") == "1" {
			t.Fatal("root, systemd and exact native broker/provider are required")
		}
		t.Skip("native values suite supplies the owned systemd fixture")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 50*time.Second)
	defer cancel()
	root := t.TempDir()
	copyProgram := func(source, name string) string {
		t.Helper()
		body, err := os.ReadFile(source)
		if err != nil {
			t.Fatal(err)
		}
		path := filepath.Join(root, name)
		if err := os.WriteFile(path, body, 0555); err != nil {
			t.Fatal(err)
		}
		if err := os.Chmod(path, 0555); err != nil {
			t.Fatal(err)
		}
		return path
	}
	broker = copyProgram(broker, "broker")
	provider = copyProgram(provider, "provider")
	configuration, err := config.Load("../../../config/runtime.toml")
	if err != nil {
		t.Fatal(err)
	}
	body, err := os.ReadFile("../../../packaging/native/execution-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	contract, err := execution.Load(body)
	if err != nil {
		t.Fatal(err)
	}
	runtime, err := os.MkdirTemp("/dev/shm", "loki-native-stage-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.RemoveAll(runtime) })
	emptyRoot := filepath.Join(root, "empty-root")
	if err := os.Mkdir(emptyRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(emptyRoot, 0755); err != nil {
		t.Fatal(err)
	}
	options := hostapplication.PrepareOptions{UID: 10006, GID: 10006, AgentUID: 10000, MetadataGID: 10001, IdentityFile: filepath.Join(root, "identity.json"), StateRoot: filepath.Join(root, "state"), ServiceStateRoot: "/var/lib/loki-go/secrets", RuntimeRoot: runtime, Binary: "/opt/loki/bin/devtools", Principal: "agent", Configuration: configuration, Contract: contract}
	before, err := hostapplication.PrepareState(options)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(before.HostGeneration, "dvt", "original-generation-canary"), []byte("public namespace sentinel"), 0600); err != nil {
		t.Fatal(err)
	}
	options.MigrationOperation = "123e4567-e89b-42d3-a456-426614174521"
	// Use the immutable host operation's derived generation-shaped identifier.
	id, err := newAttempt()
	if err != nil {
		t.Fatal(err)
	}
	candidateID := strings.ReplaceAll(id, "-", "")
	prepared, err := hostapplication.PrepareCandidate(options, before.Identity, candidateID)
	if err != nil {
		t.Fatal(err)
	}
	candidate := NativeCandidate{Operation: options.MigrationOperation, Preparation: prepared, RootDirectory: emptyRoot, BrokerHost: broker, ProviderHost: provider}
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 25*time.Second)
		defer stop()
		if err := candidate.Stop(cleanup); err != nil {
			t.Error("owned native candidate cleanup", err)
		}
	})
	if err := candidate.Start(ctx); err != nil {
		t.Fatal("native inactive broker start", err)
	}
	if state, err := candidate.Status(ctx); err != nil || state != "uninitialized" {
		t.Fatal("native start initialized protection", state, err)
	}
	if err := candidate.Start(ctx); err != nil {
		t.Fatal("lost start acknowledgement not replayable", err)
	}
	pidBody, err := nativeCommand(ctx, "/usr/bin/systemctl", "show", candidate.unit(), "--property=MainPID", "--value")
	pid, parseErr := strconv.Atoi(strings.TrimSpace(string(pidBody)))
	if err != nil || parseErr != nil || pid <= 0 {
		t.Fatal("native candidate PID was not established", err, parseErr)
	}
	processRoot := filepath.Join("/proc", strconv.Itoa(pid), "root")
	for _, path := range []string{filepath.Join(prepared.Layout.Root, "original-generation-canary"), options.IdentityFile, "/workspace", "/var/run/docker.sock"} {
		if _, err := os.Lstat(filepath.Join(processRoot, path)); !os.IsNotExist(err) {
			t.Fatal("inactive namespace exposed another generation/host authority", path, err)
		}
	}
	if _, err := candidate.Client().Call(ctx, appvalues.AdminRequest{Action: "protection_init"}, []byte("synthetic-native-candidate-passphrase")); err != nil {
		t.Fatal("explicit native candidate initialization", err)
	}
	if state, err := candidate.Status(ctx); err != nil || state != "unlocked" {
		t.Fatal("explicit native protection state", state, err)
	}
	if err := candidate.Stop(ctx); err != nil {
		t.Fatal(err)
	}
	if err := candidate.Start(ctx); err != nil {
		t.Fatal("native candidate restart", err)
	}
	if state, err := candidate.Status(ctx); err != nil || state != "locked" && state != "locked_manual" {
		t.Fatal("native restart preserved an unlocked session", state, err)
	}
	if _, err := candidate.Client().Call(ctx, appvalues.AdminRequest{Action: "protection_unlock"}, []byte("wrong-native-candidate-passphrase")); err == nil {
		t.Fatal("wrong native passphrase unlocked candidate")
	}
	if _, err := candidate.Client().Call(ctx, appvalues.AdminRequest{Action: "protection_unlock"}, []byte("synthetic-native-candidate-passphrase")); err != nil {
		t.Fatal("explicit native candidate unlock", err)
	}
}
