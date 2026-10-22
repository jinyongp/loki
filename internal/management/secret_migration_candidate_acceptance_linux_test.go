//go:build linux

package management

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"

	hostapplication "loki/internal/host/application"
	"loki/internal/host/lifecycle"
	hostpolicy "loki/internal/host/policy"
	legacyfixture "loki/internal/state"
)

// This exercises actual candidate/source/selector effects. Required service
// restart, health and whole paired recovery have separate acceptance gates.
func TestRealManagedFullMigrationCandidate(t *testing.T) {
	for _, selectCandidate := range []bool{false, true} {
		t.Run(map[bool]string{false: "retain_original", true: "select_candidate"}[selectCandidate], func(t *testing.T) {
			runRealManagedFullMigrationCandidate(t, selectCandidate)
		})
	}
}

func runRealManagedFullMigrationCandidate(t *testing.T, selectCandidate bool) {
	image := os.Getenv("LOKI_TEST_NATIVE_SECRET_IMAGE")
	if image == "" {
		if os.Getenv("LOKI_REQUIRE_NATIVE_SECRET_COMPOSE") == "1" {
			t.Fatal("native shipping image required")
		}
		t.Skip("native package suite supplies the immutable image")
	}
	ctx, cancel := context.WithTimeout(t.Context(), 2*time.Minute)
	defer cancel()
	store := migrationCandidateStore(t)
	b, err := NewDockerFullBackend(store, "", io.Discard)
	if err != nil {
		t.Fatal(err)
	}
	state, err := store.Load()
	if err != nil {
		t.Fatal(err)
	}
	// This component fixture exercises the application/MCP serving stack.
	// Protected Job/launcher replacement remains a separate required gate.
	for index := range state.Config.Tools {
		if state.Config.Tools[index].ID == "workspace" {
			state.Config.Tools[index].Enabled = false
		}
	}
	if err := store.Save(state); err != nil {
		t.Fatal(err)
	}
	core, err := store.Generation(state.Installed["runtime-core"].Artifact)
	if err != nil {
		t.Fatal(err)
	}
	secretGeneration, err := store.Generation(state.Installed["secrets"].Artifact)
	if err != nil {
		t.Fatal(err)
	}
	// The ordinary metadata fixture creates private 0700 payload directories.
	// Shipping executable payload mounts are traversable by their service UID.
	for _, directory := range []string{core, secretGeneration} {
		if err := os.Chmod(directory, 0755); err != nil {
			t.Fatal(err)
		}
	}
	copyID, err := b.command(ctx, "creating isolated shipping extraction fixture", nil, "create", image)
	if err != nil {
		t.Fatal(err)
	}
	extraction := strings.TrimSpace(string(copyID))
	defer b.command(context.Background(), "removing owned shipping extraction fixture", nil, "rm", "--force", extraction)
	for source, target := range map[string]string{"/opt/loki/bin/loki": filepath.Join(core, "program"), "/opt/loki/bin/devtools": filepath.Join(core, "provider"), "/opt/loki/bin/loki-secret-broker": filepath.Join(secretGeneration, "program"), "/usr/share/doc/loki/container-execution-contract.json": filepath.Join(core, "contract.json")} {
		if _, err := b.command(ctx, "extracting immutable native fixture input", nil, "cp", extraction+":"+source, target); err != nil {
			t.Fatal(err)
		}
	}
	if worker := os.Getenv("LOKI_TEST_NATIVE_SECRET_WORKER"); worker != "" {
		body, err := os.ReadFile(worker)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(core, "program"), body, 0555); err != nil {
			t.Fatal(err)
		}
	}
	if broker := os.Getenv("LOKI_TEST_NATIVE_SECRET_BROKER"); broker != "" {
		body, err := os.ReadFile(broker)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(secretGeneration, "program"), body, 0555); err != nil {
			t.Fatal(err)
		}
	}
	for _, path := range []string{filepath.Join(core, "program"), filepath.Join(core, "provider"), filepath.Join(secretGeneration, "program")} {
		if err := os.Chmod(path, 0555); err != nil {
			t.Fatal(err)
		}
	}
	var payload FullPayload
	if err := readOwnedJSON(filepath.Join(core, "full-runtime.json"), 1<<20, &payload); err != nil {
		t.Fatal(err)
	}
	payload.Programs["devtools"] = "provider"
	payload.Images["service"] = image
	if err := atomicJSON(filepath.Join(core, "full-runtime.json"), payload); err != nil {
		t.Fatal(err)
	}
	volume := FullMount{Kind: "volume", Source: store.FullOwner() + "-data-secrets", Target: "/var/lib/loki/secrets"}
	if err := b.ownedVolume(ctx, volume); err != nil {
		t.Fatal(err)
	}
	var candidate DeploymentReservation
	var serving *DeploymentReservation
	var projection string
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 30*time.Second)
		defer stop()
		if serving != nil {
			ids, _ := b.containerIDs(cleanup, *serving)
			for _, id := range ids {
				container, err := b.inspectContainer(cleanup, id)
				if err == nil && b.requireParent(*serving, container) == nil {
					_, _ = b.command(cleanup, "cleaning exact serving fixture container", nil, "rm", "--force", id)
				}
			}
		}
		if candidate.ID != "" {
			ids, _ := b.containerIDs(cleanup, candidate)
			for _, id := range ids {
				container, err := b.inspectContainer(cleanup, id)
				if err == nil && b.requireParent(candidate, container) == nil {
					_, _ = b.command(cleanup, "cleaning owned candidate fixture", nil, "rm", "--force", id)
				}
			}
			cipher := storeSourceCipherVolume(store, strings.TrimPrefix(candidate.ID, "deployment-"))
			if err := b.ownedVolume(cleanup, cipher); err == nil {
				_, _ = b.command(cleanup, "removing original fixture ciphertext volume", nil, "volume", "rm", cipher.Source)
			}
		}
		if projection != "" {
			mount, _ := dockerMountArgument(FullMount{Kind: "bind", Source: filepath.Dir(projection), Target: "/fixture-runtime"})
			_, _ = b.command(cleanup, "cleaning owned candidate tmpfs fixture", nil, "run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--user", "0:0", "--entrypoint", "/bin/sh", "--mount", mount, image, "-ec", "rm -rf /fixture-runtime/secrets")
			_ = os.Remove(filepath.Dir(projection))
		}
		if serving != nil {
			normalRuntime := filepath.Join("/dev/shm", store.FullOwner()+"-secret-runtime")
			mount, _ := dockerMountArgument(FullMount{Kind: "bind", Source: normalRuntime, Target: "/fixture-runtime"})
			_, _ = b.command(cleanup, "cleaning selected runtime fixture", nil, "run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--user", "0:0", "--entrypoint", "/bin/sh", "--mount", mount, image, "-ec", "rm -rf /fixture-runtime/secrets")
			_ = os.Remove(normalRuntime)
		}
		if err := b.ownedVolume(cleanup, volume); err == nil {
			if _, err := b.command(cleanup, "removing owned synthetic app volume", nil, "volume", "rm", volume.Source); err != nil {
				t.Error(err)
			}
		}
		if serving != nil {
			names, _ := b.command(cleanup, "listing owned serving fixture volumes", nil, "volume", "ls", "--filter", "label="+fullOwnerLabel+"="+store.FullOwner(), "--format", "{{.Name}}")
			for _, name := range strings.Fields(string(names)) {
				if strings.HasPrefix(name, store.FullOwner()+"-") {
					_, _ = b.command(cleanup, "removing exact owned serving fixture volume", nil, "volume", "rm", name)
				}
			}
			networks, _ := b.command(cleanup, "listing exact owned fixture networks", nil, "network", "ls", "--filter", "label="+fullOwnerLabel+"="+store.FullOwner(), "--format", "{{.ID}}")
			for _, id := range strings.Fields(string(networks)) {
				_, _ = b.command(cleanup, "removing exact owned fixture network", nil, "network", "rm", id)
			}
		}
	})
	inputs := filepath.Join(store.Root, "candidate-inputs")
	if err := os.Mkdir(inputs, 0700); err != nil {
		t.Fatal(err)
	}
	if err := writeFullFile(filepath.Join(inputs, "loki.toml"), fullRuntimeConfiguration()); err != nil {
		t.Fatal(err)
	}
	if err := writeFullFile(filepath.Join(inputs, "resources.json"), []byte(`{"resources":[]}`)); err != nil {
		t.Fatal(err)
	}
	coreMount := FullMount{Kind: "bind", Source: core, Target: "/opt/loki/modules/runtime-core", ReadOnly: true}
	inputMount := FullMount{Kind: "bind", Source: inputs, Target: "/fixture"}
	base := []string{"run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--cap-add", "CHOWN", "--cap-add", "FOWNER", "--user", "0:0", "--entrypoint", "/bin/sh"}
	for _, mount := range []FullMount{coreMount, volume, inputMount} {
		value, err := dockerMountArgument(mount)
		if err != nil {
			t.Fatal(err)
		}
		base = append(base, "--mount", value)
	}
	prepare := append(append([]string{}, base...), image, "-ec", "chown 0:0 /fixture/resources.json; chmod 0600 /fixture/resources.json; /opt/loki/modules/runtime-core/program host prepare-secrets --state-only --uid 10006 --gid 10006 --agent-uid 10000 --metadata-gid 10001 --identity /var/lib/loki/secrets/deployment.json --state /var/lib/loki/secrets --service-state /var/lib/loki/secrets --runtime /run/loki/secrets --devtools /opt/loki/modules/runtime-core/provider --config /fixture/loki.toml --execution-contract /opt/loki/modules/runtime-core/contract.json --catalog /fixture/resources.json --unit-directory= --overlay-directory= >/dev/null; /opt/loki/modules/runtime-core/program host prepare-secrets --inspect-identity --identity /var/lib/loki/secrets/deployment.json")
	beforeBytes, err := b.command(ctx, "preparing existing public app identity", nil, prepare...)
	if err != nil {
		t.Fatal(err)
	}
	var before hostapplication.Identity
	if json.Unmarshal(beforeBytes, &before) != nil {
		t.Fatal("invalid existing public identity")
	}
	intent := fullMigrationIntentFixture(t, store)
	// The fixture helper normally supplies a tracked real contract. Restore
	// the extracted shipping contract after it builds its metadata-only intent.
	if _, err := b.command(ctx, "restoring extracted shipping contract", nil, "cp", extraction+":/usr/share/doc/loki/container-execution-contract.json", filepath.Join(core, "contract.json")); err != nil {
		t.Fatal(err)
	}
	intent.Before = before
	intent.IdentityFile, intent.StateRoot, intent.ServiceStateRoot, intent.RuntimeRoot = "/var/lib/loki/secrets/deployment.json", "/var/lib/loki/secrets", "/var/lib/loki/secrets", "/run/loki/secrets"
	intent.Binary = "/opt/loki/modules/runtime-core/provider"
	configuration, contract, err := store.fullApplicationInputs()
	if err != nil {
		t.Fatal(err)
	}
	beforePolicy, err := hostpolicy.CompileApplications(configuration, contract, state.ApplicationResources)
	if err != nil {
		t.Fatal(err)
	}
	afterPolicy, err := hostpolicy.CompileApplications(configuration, contract, intent.Plan.Resources)
	if err != nil {
		t.Fatal(err)
	}
	intent.ExpectedPolicy, intent.Policy = beforePolicy.Digest(), afterPolicy.Digest()
	intent.SourceRoot = "/var/lib/loki/runtime"
	backupDirectory := filepath.Join(store.Root, "operator-backup")
	if err := os.Mkdir(backupDirectory, 0700); err != nil {
		t.Fatal(err)
	}
	intent.BackupPath = filepath.Join(backupDirectory, "source.age")
	seedManagedMigrationSource(t, ctx, b, image)
	preparedPlan, err := b.PrepareSecretMigrationSource(ctx, intent.Plan.Mappings, intent.Plan.Resources)
	if err != nil || !reflect.DeepEqual(preparedPlan, intent.Plan) {
		t.Fatal("isolated full source preflight differs from complete original mapping", err)
	}
	if selectCandidate {
		captureOwnedResources := func() {
			transaction, err := store.readTransaction()
			if err == nil && transaction != nil && transaction.SecretMigration != nil {
				candidate, _ = fullMigrationReservation(*transaction.SecretMigration, transaction.Previous)
				projection = filepath.Join("/dev/shm", store.FullOwner()+"-migration-"+transaction.SecretMigration.ID, "secrets")
			}
			reservations, _ := store.Deployments()
			for _, reservation := range reservations {
				if reservation.ID != candidate.ID {
					copy := reservation
					serving = &copy
				}
			}
		}
		pending, err := b.ApplySecretMigration(ctx, &intent, "", nil)
		captureOwnedResources()
		if err != nil || pending.Phase != lifecycle.PhaseMigrate || pending.Protection != "uninitialized" || pending.Released {
			t.Fatal("original migration did not wait for explicit protection initialization", pending, err)
		}
		complete, err := b.ApplySecretMigration(ctx, nil, "init", []byte("synthetic-full-candidate-passphrase"))
		captureOwnedResources()
		if err != nil || !complete.Released || complete.State != lifecycle.OperationSucceeded || complete.Operation != pending.Operation || serving == nil {
			t.Fatal("original full migration driver did not complete serving recovery", complete, err)
		}
		if replay, err := b.ApplySecretMigration(ctx, &intent, "", nil); err != nil || replay != complete {
			t.Fatal("completed original plan started another migration", replay, err)
		}
		actual, err := b.inspectApplicationIdentity(ctx, false)
		want := before
		want.Version, want.ActiveGeneration, want.PolicySHA256 = 2, intent.CandidateGeneration, intent.Policy
		if err != nil || actual != want {
			t.Fatal("serving driver did not preserve original generation/policy selection", err)
		}
		return
	}
	err = store.WithSecretMigrationJournal(ctx, lifecycle.OperationJournalOptions{}, func(j *lifecycle.OperationJournal) error {
		operation, err := j.BeginSecretMigration(intent, time.Now())
		if err != nil {
			return err
		}
		candidate, err = fullMigrationReservation(operation, state)
		if err != nil {
			return err
		}
		projection = filepath.Join("/dev/shm", store.FullOwner()+"-migration-"+operation.ID, "secrets")
		return b.WithSecretMigrationMaintenance(ctx, j, func(scoped context.Context) error {
			preparedCandidate, err := b.PrepareSecretMigrationCandidate(scoped)
			if err != nil {
				return err
			}
			candidate = preparedCandidate
			sourceMount, _ := dockerMountArgument(storeSourceVolume(store))
			writerBytes, err := b.command(scoped, "creating owned source writer refusal fixture", nil, "run", "--rm", "--detach", "--network", "none", "--read-only", "--cap-drop", "ALL", "--user", "10000:10001", "--memory", "33554432", "--pids-limit", "8", "--label", fullOwnerLabel+"="+store.FullOwner(), "--mount", sourceMount, "--entrypoint", "/bin/sh", image, "-ec", "exec sleep 30")
			if err != nil {
				return err
			}
			writer := strings.TrimSpace(string(writerBytes))
			defer b.command(context.Background(), "cleaning exact source writer fixture", nil, "rm", "--force", writer)
			if err := b.BackupSecretMigrationSource(scoped, candidate); err == nil {
				t.Fatal("migration froze/backed up a source with a running writer")
			}
			refused, found, err := j.Active()
			if err != nil || !found || refused.Phase != lifecycle.PhaseAdmitted || refused.SecretMigration.Archive != nil {
				t.Fatal("source writer refusal advanced the original operation", err)
			}
			if _, err := b.command(scoped, "stopping exact source writer fixture", nil, "rm", "--force", writer); err != nil {
				return err
			}
			if err := b.BackupSecretMigrationSource(scoped, candidate); err != nil {
				return fmt.Errorf("original source backup: %w", err)
			}
			if err := b.FinishSecretMigrationSource(scoped, candidate); err == nil {
				t.Fatal("source writers reopened before a terminal host result")
			}
			if err := b.CommitSecretMigrationCandidate(scoped, candidate); err == nil {
				t.Fatal("candidate selected before private equality")
			}
			if err := b.BackupSecretMigrationSource(scoped, candidate); err != nil {
				return err
			}
			if err := b.StartSecretMigrationCandidate(scoped, candidate); err != nil {
				return err
			}
			status, err := b.MigrationCandidateProtection(scoped, candidate, "status", nil)
			if err != nil || !strings.Contains(string(status), `"state":"uninitialized"`) {
				t.Fatal("candidate auto-initialized protection", err)
			}
			if _, err := b.MigrationCandidateProtection(scoped, candidate, "init", []byte("synthetic-full-candidate-passphrase")); err != nil {
				return err
			}
			if err := b.Stop(scoped, candidate); err != nil {
				return err
			}
			if _, err := b.PrepareSecretMigrationCandidate(scoped); err != nil {
				return err
			}
			if err := b.StartSecretMigrationCandidate(scoped, candidate); err != nil {
				return err
			}
			status, err = b.MigrationCandidateProtection(scoped, candidate, "status", nil)
			if err != nil || !strings.Contains(string(status), `"state":"locked`) {
				t.Fatal("candidate restart retained an unlocked key session", err)
			}
			if _, err := b.MigrationCandidateProtection(scoped, candidate, "unlock", []byte("wrong-candidate-passphrase")); err == nil {
				t.Fatal("wrong passphrase unlocked candidate")
			}
			if _, err := b.MigrationCandidateProtection(scoped, candidate, "unlock", []byte("synthetic-full-candidate-passphrase")); err != nil {
				return err
			}
			if err := b.TransferSecretMigrationSource(scoped, candidate); err != nil {
				return fmt.Errorf("original source transfer: %w", err)
			}
			ready, found, err := j.Active()
			if err != nil || !found || ready.ID != operation.ID || ready.Phase != lifecycle.PhaseSwitch || ready.SecretMigration.Archive == nil || !ready.SecretMigration.Archive.Published {
				t.Fatal("full source transfer did not publish its backup and privately compare every slot", err)
			}
			if err := verifyPublishedMigrationArchive(ready); err != nil {
				return err
			}
			record, err := b.record(candidate)
			if err != nil || record.Specs[0].Memory != 1<<30 || record.Specs[0].PIDs != 64 || record.Specs[0].RestartPolicy != "no" {
				t.Fatal("private candidate lost its shipping isolation budget", err)
			}
			if err := b.Stop(scoped, candidate); err != nil {
				return err
			}
			// This component fixture deliberately keeps the original selector.
			// Supply the host's verified rollback precondition, then exercise the
			// real source owner's terminal gate disposition and exact retry.
			inspect := append(append([]string{}, base...), image, "-ec", "/opt/loki/modules/runtime-core/program host prepare-secrets --inspect-identity --identity /var/lib/loki/secrets/deployment.json")
			original, err := b.command(scoped, "verifying fixture retained its original selector", nil, inspect...)
			if err != nil || string(original) != string(beforeBytes) {
				return fmt.Errorf("candidate unexpectedly changed selection")
			}
			if selectCandidate {
				for attempt := 0; attempt < 2; attempt++ {
					if err := b.CommitSecretMigrationCandidate(scoped, candidate); err != nil {
						return err
					}
				}
				selected, found, err := j.Active()
				if err != nil || !found || selected.Phase != lifecycle.PhaseRestart || selected.State != lifecycle.OperationApplying {
					t.Fatal("selection bypassed restart/health checkpoints", err)
				}
				if err := b.FinishSecretMigrationSource(scoped, candidate); err == nil {
					t.Fatal("source writers reopened before actual restart/health")
				}
				restarted, err := b.RestartSecretMigration(scoped)
				serving = restarted.Deployment
				if err != nil || serving == nil || !restarted.Observation.Ready {
					return fmt.Errorf("selected serving restart/health: %w", err)
				}
				if err := b.FinalizeSecretMigration(scoped, candidate, *serving); err != nil {
					return err
				}
				if _, found, err := j.Active(); err != nil || found {
					t.Fatal("completed serving stack retained an unacknowledged host release", err)
				}
				return nil
			}
			if _, err := j.CompleteSecretMigration(operation.ID, true, time.Now()); err != nil {
				return err
			}
			for attempt := 0; attempt < 2; attempt++ {
				if err := b.FinishSecretMigrationSource(scoped, candidate); err != nil {
					return err
				}
			}
			return nil
		})
	})
	if err != nil {
		t.Fatal(err)
	}
	inspect := append(append([]string{}, base...), image, "-ec", "/opt/loki/modules/runtime-core/program host prepare-secrets --inspect-identity --identity /var/lib/loki/secrets/deployment.json")
	stillBefore, err := b.command(ctx, "verifying candidate preserved original selector", nil, inspect...)
	wantIdentity := before
	if selectCandidate {
		wantIdentity.Version, wantIdentity.ActiveGeneration, wantIdentity.PolicySHA256 = 2, intent.CandidateGeneration, intent.Policy
	}
	var actualIdentity hostapplication.Identity
	if err != nil || json.Unmarshal(stillBefore, &actualIdentity) != nil || actualIdentity != wantIdentity {
		t.Fatal("actual selector differs from the original host checkpoint", err)
	}
}

// The acceptance fixture seeds an isolated synthetic AES document directly.
// Production management code does not import the legacy store/decoder.
func seedManagedMigrationSource(t *testing.T, ctx context.Context, b *DockerFullBackend, image string) {
	t.Helper()
	root := filepath.Join(b.Store.Root, "synthetic-source")
	fixture := legacyfixture.Store{Dir: root}
	if _, err := fixture.Initialize(ctx, json.RawMessage(`{"version":1,"profiles":{"web":{"secrets":{"TOKEN":"synthetic-full-source-canary\nvalue"}},"github-app":{"secrets":{"PRIVATE_KEY":"synthetic-platform-preserved"}}}}`)); err != nil {
		t.Fatal(err)
	}
	volume := storeSourceVolume(b.Store)
	if err := b.ownedVolume(ctx, volume); err != nil {
		t.Fatal(err)
	}
	mount, err := dockerMountArgument(volume)
	if err != nil {
		t.Fatal(err)
	}
	created, err := b.command(ctx, "creating owned synthetic migration source", nil, "create", "--network", "none", "--read-only", "--cap-drop", "ALL", "--cap-add", "CHOWN", "--cap-add", "FOWNER", "--user", "0:0", "--label", fullOwnerLabel+"="+b.Store.FullOwner(), "--mount", mount, "--entrypoint", "/bin/sh", image, "-ec", "chown -R 10000:10001 /var/lib/loki/runtime; chmod 0700 /var/lib/loki/runtime")
	if err != nil {
		t.Fatal(err)
	}
	container := strings.TrimSpace(string(created))
	t.Cleanup(func() {
		cleanup, stop := context.WithTimeout(context.Background(), 15*time.Second)
		defer stop()
		_, _ = b.command(cleanup, "removing owned synthetic source writer", nil, "rm", "--force", container)
		if err := b.ownedVolume(cleanup, volume); err == nil {
			_, _ = b.command(cleanup, "removing owned synthetic source volume", nil, "volume", "rm", volume.Source)
		}
	})
	for _, name := range []string{"master.key", "store.json", "state.lock"} {
		if _, err := b.command(ctx, "seeding isolated synthetic encrypted source", nil, "cp", filepath.Join(root, name), container+":"+volume.Target+"/"+name); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := b.command(ctx, "protecting synthetic source role directory", nil, "start", "--attach", container); err != nil {
		t.Fatal(err)
	}
}
