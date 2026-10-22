//go:build linux

package management

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	hostapplication "loki/internal/host/application"
	"loki/internal/host/lifecycle"
)

// CommitSecretMigrationCandidate selects only a fully compared, sealed target.
// Every registered service/Job resource must be absent under the original
// maintenance owner. Selection and activation retain that fence through the
// subsequent restart/health; this method never starts a process or releases it.
func (b *DockerFullBackend) CommitSecretMigrationCandidate(ctx context.Context, candidate DeploymentReservation) error {
	operation, err := b.migrationRecord(ctx)
	if err != nil {
		return err
	}
	if operation.State != lifecycle.OperationApplying || operation.Phase != lifecycle.PhaseSwitch && operation.Phase != lifecycle.PhaseRestart {
		return fmt.Errorf("candidate selection requires the original verified switch checkpoint")
	}
	if err := verifyPublishedMigrationArchive(operation); err != nil {
		return err
	}
	transaction, err := b.Store.readTransaction()
	if err != nil || transaction == nil || transaction.SecretMigration == nil || transaction.SecretMigration.ID != operation.ID {
		return fmt.Errorf("original migration transaction is unavailable")
	}
	current, err := b.Store.Load()
	if err != nil {
		return err
	}
	if actual := snapshotDigest(current); actual != snapshotDigest(transaction.Previous) && actual != snapshotDigest(transaction.Candidate) {
		return fmt.Errorf("activation changed outside the original migration")
	}
	record, err := b.record(candidate)
	if err != nil || record.MigrationCandidate == nil || record.Preparing {
		return fmt.Errorf("original candidate metadata is unavailable")
	}
	reservations, err := b.Store.Deployments()
	if err != nil {
		return err
	}
	for _, reservation := range reservations {
		if err := b.ConfirmAbsent(ctx, reservation); err != nil {
			return fmt.Errorf("verify original service and candidate shutdown before selector publication: %w", err)
		}
	}
	source, err := b.verifiedSecretSourceVolume(ctx)
	if err != nil {
		return err
	}
	if err := b.requireOfflineSecretSource(ctx, source, candidate); err != nil {
		return err
	}
	intent := operation.SecretMigration.Intent
	resources, err := b.Store.FullResources()
	if err != nil {
		return err
	}
	provider, err := resources.ServiceProgram("runtime-core", "devtools")
	if err != nil || provider != intent.Binary {
		return fmt.Errorf("candidate provider changed before selection")
	}
	contract, err := resources.ServiceAsset("runtime-core", "execution-contract")
	if err != nil {
		return err
	}
	projection, _, err := b.migrationProjection(operation)
	if err != nil {
		return err
	}
	directory, err := b.deploymentDirectory(candidate)
	if err != nil {
		return err
	}
	layoutDirectory := filepath.Join(directory, "layout")
	if err := realDirectories(layoutDirectory); err != nil {
		return err
	}
	mounts := []FullMount{{Kind: "volume", Source: b.Store.FullOwner() + "-data-secrets", Target: "/var/lib/loki/secrets"}, {Kind: "bind", Source: projection, Target: "/run/loki/secrets"}, {Kind: "bind", Source: layoutDirectory, Target: "/loki-layout"}}
	for _, program := range resources.Plan.Programs {
		if program.Module == "runtime-core" {
			mounts = append(mounts, fullModuleMount(program))
		}
	}
	if len(mounts) != 4 {
		return fmt.Errorf("selector helper requires exactly the immutable core payload")
	}
	before, _ := json.Marshal(intent.Before)
	arguments := []string{record.Worker, "host", "prepare-secrets", "--uid", "10006", "--gid", "10006", "--agent-uid", "10000", "--metadata-gid", "10001", "--identity", intent.IdentityFile, "--state", intent.StateRoot, "--service-state", intent.ServiceStateRoot, "--runtime", intent.RuntimeRoot, "--devtools", provider, "--config", "/loki-layout/loki.toml", "--github-config", "/loki-layout/github.toml", "--execution-contract", contract, "--catalog", "/loki-layout/application-resources.json", "--unit-directory=", "--overlay-directory=", "--candidate-generation", intent.CandidateGeneration, "--candidate-before", string(before), "--candidate-operation", lifecycle.MigrationOperationUUID(operation.ID), "--commit-candidate", "--candidate-policy", intent.Policy}
	var script strings.Builder
	fmt.Fprintf(&script, "test ! -L /run/loki/secrets\nowner=$(stat -c %%u /run/loki/secrets)\nif test \"$owner\" = %d || test \"$owner\" = 0; then test -z \"$(ls -A /run/loki/secrets)\"; chown 0:0 /run/loki/secrets; chmod 0700 /run/loki/secrets; else test \"$owner\" = 10006; test \"$(stat -c %%g /run/loki/secrets)\" = 10006; test \"$(stat -c %%a /run/loki/secrets)\" = 700; fi\n", os.Geteuid())
	for index, argument := range arguments {
		if index > 0 {
			script.WriteByte(' ')
		}
		script.WriteString("'" + strings.ReplaceAll(argument, "'", "'\\''") + "'")
	}
	fmt.Fprintf(&script, " > /loki-layout/selector-selected.json\nchown %d /loki-layout/selector-selected.json\nchmod 0600 /loki-layout/selector-selected.json\n", os.Geteuid())
	if err := b.bootstrap(ctx, candidate, FullTopology{Owner: b.Store.FullOwner(), Resources: resources}, FullBootstrap{Mounts: mounts, Script: script.String()}); err != nil {
		return err
	}
	var selected hostapplication.Identity
	want := intent.Before
	want.Version, want.ActiveGeneration, want.PolicySHA256 = 2, intent.CandidateGeneration, intent.Policy
	if readOwnedJSON(filepath.Join(layoutDirectory, "selector-selected.json"), 4096, &selected) != nil || selected != want {
		return fmt.Errorf("actual generation/policy selector differs from the original candidate")
	}
	if err := b.Store.Save(transaction.Candidate); err != nil {
		return err
	}
	scope := ctx.Value(fullMigrationMaintenanceKey{}).(*fullMigrationMaintenanceScope)
	_, err = scope.journal.AdvanceSecretMigration(operation.ID, lifecycle.PhaseRestart, time.Now())
	return err
}
