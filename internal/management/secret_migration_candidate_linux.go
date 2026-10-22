//go:build linux

package management

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"golang.org/x/sys/unix"

	"loki/internal/host/lifecycle"
	"loki/internal/tools"
)

// This is immutable resource ownership in the existing services/registry files,
// not another migration journal. The enclosing transaction alone owns progress.
type fullMigrationCandidateBinding struct {
	Operation  string `json:"operation"`
	Generation string `json:"generation"`
	Policy     string `json:"policy"`
}

func (b *DockerFullBackend) migrationProjection(record lifecycle.OperationRecord) (string, bool, error) {
	ancestor := filepath.Join("/dev/shm", b.Store.FullOwner()+"-migration-"+record.ID)
	if err := os.Mkdir(ancestor, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", false, err
	}
	var owner, info unix.Stat_t
	var filesystem unix.Statfs_t
	if unix.Lstat(b.Store.Root, &owner) != nil || unix.Lstat(ancestor, &info) != nil || info.Mode&unix.S_IFMT != unix.S_IFDIR || info.Mode&07777 != 0700 || info.Uid != owner.Uid || unix.Statfs(ancestor, &filesystem) != nil || filesystem.Type != unix.TMPFS_MAGIC {
		return "", false, fmt.Errorf("candidate runtime requires private owner tmpfs")
	}
	projection := filepath.Join(ancestor, "secrets")
	err := os.Mkdir(projection, 0700)
	created := err == nil
	if err != nil && !errors.Is(err, os.ErrExist) {
		return "", false, err
	}
	if unix.Lstat(projection, &info) != nil || info.Mode&unix.S_IFMT != unix.S_IFDIR || info.Mode&07777 != 0700 || info.Uid != owner.Uid && info.Uid != 0 && info.Uid != 10006 {
		return "", false, fmt.Errorf("candidate runtime ownership changed")
	}
	return projection, created, nil
}

// PrepareSecretMigrationCandidate creates the private broker using the normal
// deployment registry, immutable payloads, service record and ownership checks.
// Its only writable mounts are the NEW generation and its private tmpfs role
// projection. Preparation neither initializes protection nor changes selection.
func (b *DockerFullBackend) PrepareSecretMigrationCandidate(ctx context.Context) (DeploymentReservation, error) {
	reservation, err := b.ReserveSecretMigrationCandidate(ctx)
	if err != nil {
		return DeploymentReservation{}, err
	}
	operation, err := b.migrationRecord(ctx)
	if err != nil {
		return DeploymentReservation{}, err
	}
	intent := operation.SecretMigration.Intent
	if intent.Before.UID != 10006 || intent.Before.GID != 10006 || intent.Before.AgentUID != 10000 || intent.Before.MetadataGID != 10001 || intent.IdentityFile != "/var/lib/loki/secrets/deployment.json" || intent.StateRoot != "/var/lib/loki/secrets" || intent.ServiceStateRoot != "/var/lib/loki/secrets" || intent.RuntimeRoot != "/run/loki/secrets" {
		return DeploymentReservation{}, fmt.Errorf("full migration differs from the installed fixed role paths or identities")
	}
	resources, err := b.Store.FullResources()
	if err != nil {
		return DeploymentReservation{}, err
	}
	worker, err := resources.ServiceProgram("runtime-core", "loki")
	if err != nil {
		return DeploymentReservation{}, err
	}
	provider, err := resources.ServiceProgram("runtime-core", "devtools")
	if err != nil || provider != intent.Binary {
		return DeploymentReservation{}, fmt.Errorf("candidate provider differs from immutable installed inputs")
	}
	broker, err := resources.ServiceProgram("secrets", "secret-broker")
	if err != nil {
		return DeploymentReservation{}, err
	}
	contract, err := resources.ServiceAsset("runtime-core", "execution-contract")
	if err != nil {
		return DeploymentReservation{}, err
	}
	image, err := resources.Image("runtime-core", "service")
	if err != nil {
		return DeploymentReservation{}, err
	}
	if err := b.pinnedImage(ctx, image); err != nil {
		return DeploymentReservation{}, err
	}
	projection, rebuildRuntime, err := b.migrationProjection(operation)
	if err != nil {
		return DeploymentReservation{}, err
	}
	directory, err := b.deploymentDirectory(reservation)
	if err != nil {
		return DeploymentReservation{}, err
	}
	layoutDirectory := filepath.Join(directory, "layout")
	if err := os.Mkdir(layoutDirectory, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return DeploymentReservation{}, err
	}
	if err := realDirectories(layoutDirectory); err != nil {
		return DeploymentReservation{}, err
	}
	appVolume := FullMount{Kind: "volume", Source: b.Store.FullOwner() + "-data-secrets", Target: "/var/lib/loki/secrets"}
	appHost, err := b.volumePath(ctx, appVolume)
	if err != nil {
		return DeploymentReservation{}, err
	}
	programMounts := []FullMount{}
	for _, program := range resources.Plan.Programs {
		if program.Module == "runtime-core" || program.Module == "secrets" {
			programMounts = append(programMounts, fullModuleMount(program))
		}
	}
	if len(programMounts) != 2 {
		return DeploymentReservation{}, fmt.Errorf("candidate requires only the installed core and broker payloads")
	}
	mounts := append([]FullMount{}, programMounts...)
	mounts = append(mounts, FullMount{Kind: "bind", Source: filepath.Join(appHost, "generations", intent.CandidateGeneration), Target: "/var/lib/loki/secrets"}, FullMount{Kind: "bind", Source: projection, Target: "/run/loki/secrets"}, FullMount{Kind: "bind", Source: filepath.Join(projection, "layout.json"), Target: "/run/loki/secrets/layout.json", ReadOnly: true})
	spec := fullContainerSpec{Name: b.containerName(reservation, "secrets"), Service: "secrets", Image: image, User: "10006:10006", Groups: []string{"10001"}, Command: []string{broker, "--layout", "/run/loki/secrets/layout.json"}, Mounts: mounts, Networks: []string{}, Capabilities: []string{}, Memory: 1 << 30, PIDs: 64, RestartPolicy: "no", Tmpfs: map[string]string{"/tmp": "rw,nosuid,nodev,size=134217728,uid=10006,gid=10006,mode=0700", "/var/tmp": "rw,nosuid,nodev,size=134217728,uid=10006,gid=10006,mode=0700"}}
	serviceRecord := fullDockerRecord{Schema: 1, Reservation: reservation, Worker: worker, Preparing: true, MigrationCandidate: &fullMigrationCandidateBinding{Operation: lifecycle.MigrationOperationUUID(operation.ID), Generation: intent.CandidateGeneration, Policy: intent.Policy}, Specs: []fullContainerSpec{spec}, Order: []string{"secrets"}}
	previous, err := b.record(reservation)
	if err == nil {
		if !reflect.DeepEqual(previous.Specs, serviceRecord.Specs) || !reflect.DeepEqual(previous.MigrationCandidate, serviceRecord.MigrationCandidate) {
			return DeploymentReservation{}, fmt.Errorf("original candidate resource specification changed")
		}
		if !previous.Preparing && !rebuildRuntime {
			if err := b.createContainer(ctx, reservation, spec); err != nil {
				return DeploymentReservation{}, err
			}
			return reservation, nil
		}
		if rebuildRuntime {
			ids, err := b.containerIDs(ctx, reservation)
			if err != nil {
				return DeploymentReservation{}, err
			}
			for _, id := range ids {
				container, err := b.inspectContainer(ctx, id)
				if err != nil {
					return DeploymentReservation{}, err
				}
				if err := b.requireParent(reservation, container); err != nil {
					return DeploymentReservation{}, err
				}
				if container.Config.Labels[fullServiceLabel] != "secrets" {
					continue
				}
				if err := b.verifySpec(reservation, spec, container); err != nil {
					return DeploymentReservation{}, err
				}
				if _, err := b.command(ctx, "replacing owned candidate after tmpfs loss", nil, "container", "rm", "--force", id); err != nil {
					return DeploymentReservation{}, err
				}
			}
		}
	} else if !errors.Is(err, os.ErrNotExist) {
		return DeploymentReservation{}, err
	}
	if err := atomicJSON(filepath.Join(directory, "services.json"), serviceRecord); err != nil {
		return DeploymentReservation{}, err
	}
	catalog, _ := json.Marshal(struct {
		Resources any `json:"resources"`
	}{Resources: intent.Plan.Resources})
	if len(catalog) > 1<<20 {
		return DeploymentReservation{}, fmt.Errorf("candidate catalog exceeds the service metadata bound")
	}
	if err := writeFullFile(filepath.Join(layoutDirectory, "application-resources.json"), catalog); err != nil {
		return DeploymentReservation{}, err
	}
	if err := writeFullFile(filepath.Join(layoutDirectory, "loki.toml"), fullRuntimeConfiguration()); err != nil {
		return DeploymentReservation{}, err
	}
	github := []byte("# No GitHub integration selected.\n")
	for _, choice := range resources.Plan.Enabled {
		if choice.ID == "github" {
			github, err = b.Store.ProviderConfiguration("github")
			if err != nil {
				return DeploymentReservation{}, err
			}
			break
		}
	}
	if err := writeFullFile(filepath.Join(layoutDirectory, "github.toml"), github); err != nil {
		return DeploymentReservation{}, err
	}
	before, _ := json.Marshal(intent.Before)
	arguments := []string{worker, "host", "prepare-secrets", "--uid", "10006", "--gid", "10006", "--agent-uid", "10000", "--metadata-gid", "10001", "--identity", intent.IdentityFile, "--state", intent.StateRoot, "--service-state", intent.ServiceStateRoot, "--runtime", intent.RuntimeRoot, "--devtools", provider, "--config", "/loki-layout/loki.toml", "--github-config", "/loki-layout/github.toml", "--execution-contract", contract, "--catalog", "/loki-layout/application-resources.json", "--unit-directory=", "--overlay-directory=", "--candidate-generation", intent.CandidateGeneration, "--candidate-before", string(before), "--candidate-operation", lifecycle.MigrationOperationUUID(operation.ID)}
	var script strings.Builder
	script.WriteString("test ! -L /loki-layout/application-resources.json\nchown 0:0 /loki-layout/application-resources.json\nchmod 0600 /loki-layout/application-resources.json\n")
	fmt.Fprintf(&script, "test ! -L /run/loki/secrets\nowner=$(stat -c %%u /run/loki/secrets)\nif test \"$owner\" = %d || test \"$owner\" = 0; then test -z \"$(ls -A /run/loki/secrets)\"; chown 0:0 /run/loki/secrets; chmod 0700 /run/loki/secrets; else test \"$owner\" = 10006; test \"$(stat -c %%g /run/loki/secrets)\" = 10006; test \"$(stat -c %%a /run/loki/secrets)\" = 700; fi\n", os.Geteuid())
	for index, argument := range arguments {
		if index > 0 {
			script.WriteByte(' ')
		}
		script.WriteString("'" + strings.ReplaceAll(argument, "'", "'\\''") + "'")
	}
	fmt.Fprintf(&script, " > /loki-layout/secrets-prepared.json\nchown %d /loki-layout/secrets-prepared.json\nchmod 0600 /loki-layout/secrets-prepared.json\n", os.Geteuid())
	bootstrapMounts := append([]FullMount{}, programMounts...)
	bootstrapMounts = append(bootstrapMounts, appVolume, FullMount{Kind: "bind", Source: projection, Target: "/run/loki/secrets"}, FullMount{Kind: "bind", Source: layoutDirectory, Target: "/loki-layout"})
	if err := b.bootstrap(ctx, reservation, FullTopology{Owner: b.Store.FullOwner(), Resources: resources}, FullBootstrap{Mounts: bootstrapMounts, Script: script.String()}); err != nil {
		return DeploymentReservation{}, err
	}
	var prepared struct {
		Prepared   bool   `json:"prepared"`
		Candidate  bool   `json:"candidate"`
		Policy     string `json:"policy"`
		UID        uint32 `json:"broker_uid"`
		GID        uint32 `json:"broker_gid"`
		Deployment string `json:"deployment_id"`
		Runner     string `json:"runner_id"`
		Generation string `json:"active_generation"`
	}
	if readOwnedJSON(filepath.Join(layoutDirectory, "secrets-prepared.json"), 4096, &prepared) != nil || !prepared.Prepared || !prepared.Candidate || prepared.Policy != intent.Policy || prepared.UID != 10006 || prepared.GID != 10006 || prepared.Deployment != intent.Before.DeploymentID || prepared.Runner != intent.Before.RunnerID || prepared.Generation != intent.CandidateGeneration {
		return DeploymentReservation{}, fmt.Errorf("original private candidate preparation was not confirmed")
	}
	serviceRecord.Preparing = false
	if err := atomicJSON(filepath.Join(directory, "services.json"), serviceRecord); err != nil {
		return DeploymentReservation{}, err
	}
	if err := b.createContainer(ctx, reservation, spec); err != nil {
		return DeploymentReservation{}, err
	}
	return reservation, nil
}

func migrationCandidateDigest(record lifecycle.OperationRecord) string {
	sum := sha256.Sum256([]byte("loki-secret-migration-candidate\x00" + record.PlanID))
	return hex.EncodeToString(sum[:])
}

func fullMigrationReservation(record lifecycle.OperationRecord, state Snapshot) (DeploymentReservation, error) {
	if !record.Valid() || record.Kind != lifecycle.OperationSecretMigration || record.SecretMigration == nil || record.SecretMigration.Intent.Mode != "managed-full" {
		return DeploymentReservation{}, fmt.Errorf("original full migration record is required")
	}
	enabled := false
	for _, choice := range state.Config.Tools {
		if choice.ID == "secrets" && choice.Enabled {
			enabled = true
		}
	}
	if !enabled {
		return DeploymentReservation{}, fmt.Errorf("enable the isolated secrets service before preparing migration")
	}
	reservation := DeploymentReservation{ID: "deployment-" + record.ID, Release: state.Config.Release, PlanDigest: migrationCandidateDigest(record), Artifacts: []tools.Artifact{}, Services: []string{"secrets"}}
	for _, id := range []tools.ID{"runtime-core", "secrets"} {
		installed, found := state.Installed[id]
		if !found || installed.Artifact.Target.Mode != tools.Full {
			return DeploymentReservation{}, fmt.Errorf("migration candidate requires its installed immutable core and broker")
		}
		reservation.Artifacts = append(reservation.Artifacts, installed.Artifact)
	}
	if err := (deploymentRegistry{Schema: 1, Deployments: []DeploymentReservation{reservation}}).validate(); err != nil {
		return DeploymentReservation{}, err
	}
	return reservation, nil
}

func (b *DockerFullBackend) migrationRecord(ctx context.Context) (lifecycle.OperationRecord, error) {
	if _, err := b.migrationMaintenance(ctx); err != nil {
		return lifecycle.OperationRecord{}, err
	}
	scope, _ := ctx.Value(fullMigrationMaintenanceKey{}).(*fullMigrationMaintenanceScope)
	if scope == nil {
		return lifecycle.OperationRecord{}, fmt.Errorf("original full migration owner is required")
	}
	record, found, err := scope.journal.Active()
	if err != nil || !found || record.SecretMigration == nil {
		return lifecycle.OperationRecord{}, fmt.Errorf("original full migration is unavailable")
	}
	return record, nil
}

// ReserveSecretMigrationCandidate pins only the installed core/broker in the
// EXISTING deployment registry, using the original operation's stable identity.
// It creates no service, key, selector or competing lifecycle record.
func (b *DockerFullBackend) ReserveSecretMigrationCandidate(ctx context.Context) (DeploymentReservation, error) {
	record, err := b.migrationRecord(ctx)
	if err != nil {
		return DeploymentReservation{}, err
	}
	if !candidatePreparationPhase(record) {
		return DeploymentReservation{}, fmt.Errorf("selected or terminal migrations cannot create another candidate")
	}
	state, err := b.Store.Load()
	if err != nil {
		return DeploymentReservation{}, err
	}
	reservation, err := fullMigrationReservation(record, state)
	if err != nil {
		return DeploymentReservation{}, err
	}
	registry, err := b.Store.deploymentRegistry()
	if err != nil {
		return DeploymentReservation{}, err
	}
	for _, existing := range registry.Deployments {
		if existing.ID == reservation.ID {
			if !reflect.DeepEqual(existing, reservation) {
				return DeploymentReservation{}, fmt.Errorf("migration candidate identity already belongs to another resource selection")
			}
			return existing, nil
		}
	}
	registry.Deployments = append(registry.Deployments, reservation)
	if err := registry.validate(); err != nil {
		return DeploymentReservation{}, err
	}
	if err := atomicJSON(filepath.Join(b.Store.Root, "deployments.json"), registry); err != nil {
		return DeploymentReservation{}, err
	}
	return reservation, nil
}

func candidatePreparationPhase(record lifecycle.OperationRecord) bool {
	return record.State == lifecycle.OperationApplying && (record.Phase == lifecycle.PhaseAdmitted || record.Phase == lifecycle.PhaseSnapshot || record.Phase == lifecycle.PhaseMigrate)
}

func (b *DockerFullBackend) validMigrationCandidate(record fullDockerRecord) bool {
	if record.MigrationCandidate == nil {
		return true
	}
	t, err := b.Store.readTransaction()
	if err != nil || t == nil || t.Action != "secret_migration" || t.SecretMigration == nil || t.SecretMigration.Maintenance.Released {
		return false
	}
	active := *t.SecretMigration
	want, err := fullMigrationReservation(active, t.Previous)
	if err != nil || !reflect.DeepEqual(want, record.Reservation) {
		return false
	}
	binding := record.MigrationCandidate
	return binding.Operation == lifecycle.MigrationOperationUUID(active.ID) && binding.Generation == active.SecretMigration.Intent.CandidateGeneration && binding.Policy == active.SecretMigration.Intent.Policy && len(record.Specs) == 1 && record.Specs[0].Service == "secrets" && record.Specs[0].RestartPolicy == "no"
}
