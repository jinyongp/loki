//go:build linux

package application

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
	"loki/internal/config"
	"loki/internal/execution"
)

func TestHostApplicationGenerationPreparation(t *testing.T) {
	if os.Geteuid() != 0 {
		if os.Getenv("LOKI_REQUIRE_HOST_APP_LAYOUT") == "1" {
			t.Fatal("root filesystem preparation fixture is required")
		}
		t.Skip("native values suite runs this owned fixture as root")
	}
	c, err := config.Load(filepath.Join("..", "..", "..", "config", "runtime.toml"))
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile(filepath.Join("..", "..", "..", "packaging", "native", "execution-contract.json"))
	if err != nil {
		t.Fatal(err)
	}
	contract, err := execution.Load(raw)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	runtimeRoot, err := os.MkdirTemp("/dev/shm", "loki-host-prepare-")
	if err != nil {
		t.Fatal(err)
	}
	defer os.RemoveAll(runtimeRoot)
	options := PrepareOptions{UID: 10006, GID: 10006, AgentUID: 10000, MetadataGID: 10001, IdentityFile: filepath.Join(root, "identity.json"), StateRoot: filepath.Join(root, "state"), ServiceStateRoot: "/var/lib/loki-go/secrets", RuntimeRoot: runtimeRoot, Binary: "/opt/loki/bin/devtools", Principal: "agent", Configuration: c, Contract: contract}
	options.JobDeploymentID = "deployment-" + strings.Repeat("1", 32)
	stateOnly, err := PrepareState(options)
	if err != nil {
		t.Fatal(err)
	}
	entries, err := os.ReadDir(runtimeRoot)
	if err != nil || len(entries) != 0 || stateOnly.LayoutFile != "" || stateOnly.Identity.DeploymentID == options.JobDeploymentID || stateOnly.Layout.JobDeploymentID != options.JobDeploymentID {
		t.Fatal("persistent preparation mixed app identity, runtime or OCI reservation", err)
	}
	first, err := Prepare(options)
	if err != nil {
		t.Fatal(err)
	}
	if first.Identity != stateOnly.Identity {
		t.Fatal("runtime preparation replaced persistent app identity")
	}
	again, err := Prepare(options)
	if err != nil || first.Identity != again.Identity || first.Layout.PolicySHA256 != again.Layout.PolicySHA256 {
		t.Fatal("preparation changed the existing generation", err)
	}
	if !strings.HasPrefix(first.HostGeneration, filepath.Join(options.StateRoot, "generations")+"/") || first.Layout.Root != "/var/lib/loki-go/secrets/dvt" {
		t.Fatal("host selector leaked into the service state mapping")
	}
	for _, check := range []struct {
		path           string
		uid, gid, mode uint32
	}{{options.StateRoot, 0, 0, 0700}, {first.HostGeneration, 10006, 10006, 0700}, {first.Layout.ApplicationRoot, 10006, 10006, 0700}, {first.Layout.MetadataSocket, 10006, 10001, 0750}, {first.Layout.Socket, 10006, 10006, 0700}} {
		path := check.path
		if path == first.Layout.ApplicationRoot {
			path = filepath.Join(first.HostGeneration, "metadata")
		}
		if path == first.Layout.MetadataSocket || path == first.Layout.Socket {
			path = filepath.Dir(path)
		}
		var st unix.Stat_t
		if unix.Lstat(path, &st) != nil || st.Uid != check.uid || st.Gid != check.gid || st.Mode&07777 != check.mode {
			t.Fatalf("prepared role directory has incorrect ownership: %s", path)
		}
	}
	if err := filepath.WalkDir(options.StateRoot, func(path string, entry os.DirEntry, err error) error {
		if err != nil {
			return err
		}
		if entry.Name() == "key-slot.json" {
			t.Fatal("preparation initialized crypto")
		}
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	original := options.Contract.Environment["PATH"]
	options.Contract.Environment["PATH"] = "changed-after-prepare"
	if first.Layout.BaseEnvironment["PATH"] != original {
		t.Fatal("returned service environment was mutable through its caller")
	}
	options.Contract.Environment["PATH"] = original
	changed := options
	changed.UID = 10007
	if _, err := Prepare(changed); err == nil {
		t.Fatal("existing deployment silently changed broker identity")
	}
	identityBytes, err := os.ReadFile(options.IdentityFile)
	if err != nil {
		t.Fatal(err)
	}
	var identity Identity
	if json.Unmarshal(identityBytes, &identity) != nil || identity != first.Identity {
		t.Fatal("failed identity change rewrote the selector")
	}
	if os.Chown(first.HostGeneration, 0, 0) != nil {
		t.Fatal("foreign-owner fixture")
	}
	if _, err := Prepare(options); err == nil {
		t.Fatal("preparation adopted nonempty foreign state")
	}
	var st unix.Stat_t
	if unix.Lstat(first.HostGeneration, &st) != nil || st.Uid != 0 {
		t.Fatal("foreign state ownership changed")
	}
}
