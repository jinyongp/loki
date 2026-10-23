//go:build linux

package application

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
	"loki/internal/config"
	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/execution"
	hostpolicy "loki/internal/host/policy"
)

func TestMetadataPreflightRejectsUnservableLayoutBeforeCreatingState(t *testing.T) {
	c, err := config.Load("../../../config/runtime.toml")
	if err != nil {
		t.Fatal(err)
	}
	raw, err := os.ReadFile("../../../packaging/native/execution-contract.json")
	if err != nil {
		t.Fatal(err)
	}
	contract, err := execution.Load(raw)
	if err != nil {
		t.Fatal(err)
	}
	root := t.TempDir()
	options := PrepareOptions{UID: 10006, GID: 10006, AgentUID: 10000, MetadataGID: 10001, IdentityFile: filepath.Join(root, "identity.json"), StateRoot: filepath.Join(root, "state"), ServiceStateRoot: "/var/lib/loki/secrets", RuntimeRoot: "/run/loki/secrets", Binary: "/opt/loki/bin/devtools", Principal: "agent", Configuration: c, Contract: contract}
	if err := ValidateMetadata(options); err != nil {
		t.Fatal("ordinary metadata preflight", err)
	}
	resource := controlpolicy.ApplicationResource{ID: "123e4567-e89b-42d3-a456-426614174581", Alias: "web", Repository: "owner/repo", Network: "none", Keys: map[string]string{"TOKEN": "secret"}, Principals: []controlpolicy.ApplicationGrant{{Principal: "agent", Execute: true}}}
	for index := 0; index < 17000; index++ {
		resource.Environments = append(resource.Environments, fmt.Sprintf("env-%05d-%s", index, strings.Repeat("a", 50)))
	}
	options.Resources = []controlpolicy.ApplicationResource{resource}
	if _, err := hostpolicy.CompileApplications(c, contract, options.Resources); err != nil {
		t.Fatal("fixture must be semantically valid policy", err)
	}
	if ValidateMetadata(options) == nil {
		t.Fatal("service metadata beyond its existing 1MiB reader limit accepted")
	}
	if _, err := PrepareState(options); err == nil {
		t.Fatal("unservable metadata created an inactive/active store")
	}
	for _, path := range []string{options.StateRoot, options.IdentityFile} {
		if _, err := os.Lstat(path); !os.IsNotExist(err) {
			t.Fatal("preflight touched persistent state", err)
		}
	}
}

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
	t.Run("lost selector never allocates a replacement identity", func(t *testing.T) {
		saved := options.IdentityFile + ".retained"
		if err := os.Rename(options.IdentityFile, saved); err != nil {
			t.Fatal(err)
		}
		defer func() {
			if err := os.Rename(saved, options.IdentityFile); err != nil {
				t.Error(err)
			}
		}()
		if _, err := PrepareState(options); err == nil {
			t.Fatal("lost selector manufactured a new deployment over retained state")
		}
		if _, err := os.Lstat(options.IdentityFile); !os.IsNotExist(err) {
			t.Fatal("failed recovery published a replacement selector", err)
		}
	})
	t.Run("inactive candidate and atomic policy selection", func(t *testing.T) {
		t.Cleanup(func() {
			// Restore this public-only fixture even if a negative assertion
			// fails, so later foreign-owner checks retain their original source.
			if err := os.WriteFile(options.IdentityFile, identityBytes, 0600); err != nil {
				t.Error(err)
			}
		})
		stageRuntime, err := os.MkdirTemp("/dev/shm", "loki-host-candidate-")
		if err != nil {
			t.Fatal(err)
		}
		defer os.RemoveAll(stageRuntime)
		stageOptions := options
		stageOptions.RuntimeRoot = stageRuntime
		stageOptions.MigrationOperation = "123e4567-e89b-42d3-a456-426614174481"
		stageOptions.InitialMaintenanceOperation = stageOptions.MigrationOperation
		activeAdmin, err := os.ReadFile(first.AdminLayoutFile)
		if err != nil {
			t.Fatal(err)
		}
		generation := strings.Repeat("a", 32)
		candidate, err := PrepareCandidate(stageOptions, first.Identity, generation)
		if err != nil {
			t.Fatal(err)
		}
		if candidate.Identity.Version != 2 || candidate.Identity.PolicySHA256 != candidate.Layout.PolicySHA256 || candidate.Identity.ActiveGeneration != generation || candidate.HostGeneration == first.HostGeneration || candidate.Layout.Root != first.Layout.Root || candidate.AdminLayoutFile == first.AdminLayoutFile {
			t.Fatal("candidate changed the logical root, original selector or active role projection")
		}
		current, err := loadIdentity(options)
		if err != nil || current != first.Identity {
			t.Fatal("inactive candidate selected before verification", err)
		}
		adminAfter, err := os.ReadFile(first.AdminLayoutFile)
		if err != nil || string(adminAfter) != string(activeAdmin) {
			t.Fatal("candidate overwrote active administrator projection", err)
		}
		var st unix.Stat_t
		if unix.Lstat(stageRuntime, &st) != nil || st.Uid != options.UID || st.Gid != options.GID || st.Mode&07777 != 0700 {
			t.Fatal("candidate runtime is traversable by the agent")
		}
		missing := candidate.HostGeneration + ".retained"
		if err := os.Rename(candidate.HostGeneration, missing); err != nil {
			t.Fatal(err)
		}
		if _, err := CommitCandidate(stageOptions, first.Identity, generation); err == nil {
			t.Fatal("selection manufactured an empty missing candidate")
		}
		if _, err := os.Lstat(candidate.HostGeneration); !os.IsNotExist(err) {
			t.Fatal("failed selection recreated a lost candidate generation", err)
		}
		if err := os.Rename(missing, candidate.HostGeneration); err != nil {
			t.Fatal(err)
		}
		if err := os.Rename(stageRuntime, stageRuntime+".retained"); err != nil {
			t.Fatal(err)
		}
		if _, err := CommitRestoredCandidate(stageOptions, first.Identity, generation); err == nil {
			t.Fatal("logical selector accepted a candidate without a broker restore seal")
		}
		selected, err := CommitCandidate(stageOptions, first.Identity, generation)
		if err != nil || selected != candidate.Identity {
			t.Fatal("selector/policy publication failed", err)
		}
		if _, err := os.Lstat(stageRuntime); !os.IsNotExist(err) {
			t.Fatal("selector publication recreated a runtime projection", err)
		}
		if replay, err := CommitCandidate(stageOptions, first.Identity, generation); err != nil || replay != selected {
			t.Fatal("lost selector ACK changed generation or policy", err)
		}
		seal := appvalues.ApplicationRestoreSeal{Version: 1, Operation: stageOptions.MigrationOperation, Deployment: selected.DeploymentID, Policy: selected.PolicySHA256, Imports: []appvalues.ApplicationImportProof{}}
		sealPath := filepath.Join(candidate.HostGeneration, "metadata", "restore-seal.json")
		sealBody, err := json.Marshal(seal)
		if err != nil || os.WriteFile(sealPath, sealBody, 0600) != nil {
			t.Fatal("private restore seal fixture", err)
		}
		if _, err := CommitRestoredCandidate(stageOptions, first.Identity, generation); err == nil {
			t.Fatal("logical selector accepted a seal owned by another UID")
		}
		if err := os.Chown(sealPath, int(options.UID), int(options.GID)); err != nil {
			t.Fatal(err)
		}
		if replay, err := CommitRestoredCandidate(stageOptions, first.Identity, generation); err != nil || replay != selected {
			t.Fatal("sealed logical selector could not replay without a runtime projection", err)
		}
		if _, err := CommitCandidate(stageOptions, first.Identity, strings.Repeat("b", 32)); err == nil {
			t.Fatal("stale expected selector overwrote committed generation")
		}
		if active, err := PrepareState(options); err != nil || active.Identity != selected || active.HostGeneration != candidate.HostGeneration {
			t.Fatal("restart did not select the verified generation", err)
		}
		stalePolicy := options
		stalePolicy.Resources = []controlpolicy.ApplicationResource{{ID: "123e4567-e89b-42d3-a456-426614174482", Alias: "policy-drift", Repository: "owner/policy-drift", Network: "none", Principals: []controlpolicy.ApplicationGrant{{Principal: "agent", Execute: true}}, Environments: []string{"common"}, Keys: map[string]string{"TOKEN": "secret"}}}
		compiled, err := hostpolicy.CompileApplications(stalePolicy.Configuration, stalePolicy.Contract, stalePolicy.Resources)
		if err != nil || compiled.Digest() == selected.PolicySHA256 {
			t.Fatal("changed-policy fixture is invalid", err)
		}
		if _, err := PrepareState(stalePolicy); err == nil {
			t.Fatal("restart silently rebound a selected generation to changed policy")
		}
		for _, name := range []string{"dvt", "metadata", "."} {
			t.Run("lost selected "+name, func(t *testing.T) {
				path := filepath.Join(candidate.HostGeneration, name)
				saved := path + ".retained"
				if err := os.Rename(path, saved); err != nil {
					t.Fatal(err)
				}
				defer func() {
					if err := os.Rename(saved, path); err != nil {
						t.Error(err)
					}
				}()
				if _, err := PrepareState(options); err == nil {
					t.Fatal("restart manufactured empty selected application state")
				}
				if _, err := CommitCandidate(stageOptions, first.Identity, generation); err == nil {
					t.Fatal("selector ACK replay manufactured empty selected state")
				}
				if _, err := os.Lstat(path); !os.IsNotExist(err) {
					t.Fatal("failed recovery created missing selected state", err)
				}
			})
		}
	})
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
	bad := options
	bad.StateRoot = filepath.Join(root, "foreign-state")
	bad.IdentityFile = filepath.Join(root, "must-not-create-identity.json")
	if err := os.Mkdir(bad.StateRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(bad.StateRoot, 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(bad.StateRoot, "preserve"), []byte("foreign state"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := PrepareState(bad); err == nil {
		t.Fatal("foreign nonempty public state adopted")
	}
	if _, err := os.Lstat(bad.IdentityFile); !os.IsNotExist(err) {
		t.Fatal("invalid state created a deployment identity", err)
	}
}
