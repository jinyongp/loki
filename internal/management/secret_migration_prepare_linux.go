//go:build linux

package management

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"

	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/host/lifecycle"
	hostpolicy "loki/internal/host/policy"
	"loki/internal/platform/privateio"
)

// PrepareSecretMigrationSource validates exact source bytes under its owner
// lock in a private helper. The manager receives only the complete public plan.
// It neither freezes the source nor allocates a migration/alternate journal.
func (b *DockerFullBackend) PrepareSecretMigrationSource(ctx context.Context, mappings []appvalues.MigrationProfileMapping, resources []controlpolicy.ApplicationResource) (appvalues.MigrationPlan, error) {
	var plan appvalues.MigrationPlan
	if err := b.Store.requireNoPendingSecretMigration(); err != nil {
		return plan, err
	}
	c, contract, err := b.Store.fullApplicationInputs()
	if err != nil {
		return plan, err
	}
	canonical, err := controlpolicy.CanonicalApplications(resources, contract.Environment)
	if err != nil {
		return plan, err
	}
	policy, err := hostpolicy.CompileApplications(c, contract, canonical)
	if err != nil {
		return plan, err
	}
	inputs, err := b.Store.FullResources()
	if err != nil {
		return plan, err
	}
	worker, err := inputs.ServiceProgram("runtime-core", "loki")
	if err != nil {
		return plan, err
	}
	image, err := inputs.Image("runtime-core", "service")
	if err != nil {
		return plan, err
	}
	if err := b.pinnedImage(ctx, image); err != nil {
		return plan, err
	}
	source, err := b.verifiedSecretSourceVolume(ctx)
	if err != nil {
		return plan, err
	}
	mounts := []FullMount{source}
	for _, program := range inputs.Plan.Programs {
		if program.Module == "runtime-core" {
			mounts = append(mounts, fullModuleMount(program))
		}
	}
	if len(mounts) != 2 {
		return plan, fmt.Errorf("source preparation requires exactly its immutable core payload")
	}
	manifest := lifecycle.SourceTransferManifest{Action: "prepare", Version: 1, SourceRoot: source.Target, Policy: policy.Digest(), Plan: appvalues.MigrationPlan{Version: 1, Mappings: mappings, Resources: canonical}, Writes: []lifecycle.MigrationWriteCheckpoint{}, BaseEnvironment: contract.Environment}
	if !manifest.Valid() {
		return plan, fmt.Errorf("invalid public source preparation inputs")
	}
	args := []string{"run", "--rm", "--interactive", "--network", "none", "--read-only", "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--security-opt", "no-new-privileges:true", "--user", "0:0", "--memory", "2147483648", "--pids-limit", "64", "--log-driver", "none", "--label", fullOwnerLabel + "=" + b.Store.FullOwner(), "--entrypoint", worker}
	for _, mount := range mounts {
		value, err := dockerMountArgument(mount)
		if err != nil {
			return plan, err
		}
		args = append(args, "--mount", value)
	}
	args = append(args, image, "host", "secret-migration-source")
	err = b.exchangeSecretSourceHelper(ctx, args, func(channel fullSourceChannel) error {
		if err := lifecycle.WriteSourceTransferManifest(channel, manifest); err != nil {
			return err
		}
		body, err := privateio.ReadBytes(channel, lifecycle.MaxSourceManifestBytes)
		if err != nil {
			return err
		}
		defer clear(body)
		if json.Unmarshal(body, &plan) != nil || plan.Version != 1 || plan.Revision == 0 {
			return fmt.Errorf("source helper returned invalid public plan metadata")
		}
		canonical, err := json.Marshal(plan)
		if err != nil || !bytes.Equal(canonical, body) {
			return fmt.Errorf("source helper returned noncanonical public plan metadata")
		}
		return nil
	})
	return plan, err
}
