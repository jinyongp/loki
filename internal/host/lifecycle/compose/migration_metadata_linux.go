//go:build linux

package compose

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"filippo.io/age"

	"loki/internal/control/appvalues"
	controlpolicy "loki/internal/control/policy"
	"loki/internal/host/assets"
	"loki/internal/host/lifecycle"
	"loki/internal/platform/privateio"
)

var errComposeMigration = errors.New("resume the original Compose secret migration and its immutable resources")

type migrationMetadata struct {
	Identity        appvalues.ApplicationIdentity `json:"identity"`
	Policy          string                        `json:"policy"`
	BaseEnvironment map[string]string             `json:"base_environment"`
}

func (b *Backend) migrationVolume(ctx context.Context, kind string) (string, error) {
	if kind != "application-state" && kind != "runtime-state" && kind != "launcher-state" {
		return "", errComposeMigration
	}
	name := b.volumeName(kind)
	body, err := b.runner.Run(ctx, nil, "volume", "inspect", name)
	if err != nil {
		return "", err
	}
	var volumes []struct {
		Name, Driver string
		Labels       map[string]string
		Options      map[string]string
	}
	if json.Unmarshal(body, &volumes) != nil || len(volumes) != 1 || volumes[0].Name != name || volumes[0].Driver != "local" || len(volumes[0].Options) != 0 {
		return "", errComposeMigration
	}
	labels := volumes[0].Labels
	if kind == "application-state" {
		owner := "loki-tools-" + applicationOwnerID(b.root)
		if labels["io.loki.application.owner"] != owner {
			return "", errComposeMigration
		}
	} else if labels["com.docker.compose.project"] != b.project || labels["com.docker.compose.volume"] != kind {
		return "", errComposeMigration
	}
	return name, nil
}

func (b *Backend) migrationMetadata(ctx context.Context, state runtimeState, resources []controlpolicy.ApplicationResource, empty bool) (migrationMetadata, error) {
	return b.migrationLayout(ctx, state, resources, empty, nil, "", false)
}

func (b *Backend) migrationLayout(ctx context.Context, state runtimeState, resources []controlpolicy.ApplicationResource, empty bool, candidate *lifecycle.OperationRecord, projection string, selectCandidate bool) (migrationMetadata, error) {
	var layout *applicationCandidateLayout
	if candidate != nil {
		original, _, err := b.migrationRecord(ctx)
		if err != nil || original.ID != candidate.ID || original.SecretMigration.RollbackRequested || selectCandidate && original.Phase != lifecycle.PhaseSwitch || !selectCandidate && (original.Phase != lifecycle.PhaseAdmitted && original.Phase != lifecycle.PhaseSnapshot && original.Phase != lifecycle.PhaseMigrate) {
			return migrationMetadata{}, errComposeMigration
		}
		i := original.SecretMigration.Intent
		layout = &applicationCandidateLayout{Before: i.Before, CandidateGeneration: i.CandidateGeneration, Policy: i.Policy, Operation: lifecycle.MigrationOperationUUID(original.ID)}
	}
	return b.applicationLayout(ctx, state, resources, empty, layout, projection, selectCandidate)
}

type applicationCandidateLayout struct {
	Before                                 appvalues.ApplicationIdentity
	CandidateGeneration, Policy, Operation string
	Restore                                bool
}

// Both original-operation owners use the same finite metadata helper. Their
// admission/phase checks stay in their wrappers; no source decoder or progress
// journal is delegated through this data-only layout request.
func (b *Backend) applicationLayout(ctx context.Context, state runtimeState, resources []controlpolicy.ApplicationResource, empty bool, candidate *applicationCandidateLayout, projection string, selectCandidate bool) (migrationMetadata, error) {
	var result migrationMetadata
	private, ok := b.runner.(PrivateRunner)
	if !ok || state.validate() != nil {
		return result, errComposeMigration
	}
	volume, err := b.migrationVolume(ctx, "application-state")
	if err != nil {
		return result, err
	}
	if resources == nil {
		resources = []controlpolicy.ApplicationResource{}
	}
	catalog, err := json.Marshal(struct {
		Resources []controlpolicy.ApplicationResource `json:"resources"`
	}{resources})
	if err != nil || len(catalog) > 1<<20 {
		return result, errComposeMigration
	}
	file, err := os.CreateTemp(b.runtimeRoot, ".migration-catalog-")
	if err != nil {
		return result, err
	}
	defer os.Remove(file.Name())
	if _, err := file.Write(catalog); err != nil {
		file.Close()
		return result, err
	}
	if err := file.Close(); err != nil {
		return result, err
	}
	// Only public policy configuration crosses this preparation boundary.
	// In particular, do not call composeEnvironment: it reads platform keys.
	github := assets.GitHubConfig()
	store, err := lifecycle.OpenFileStore(b.root)
	if err != nil {
		return result, err
	}
	integrations, err := store.ReadManagedIntegrations(ctx)
	if err != nil {
		return result, err
	}
	if integrations.GitHub.Configured && integrations.GitHub.Enabled {
		github, err = publicGitHubConfiguration(ctx, store, integrations.GitHub)
		if err != nil {
			return result, err
		}
	}
	fragment, err := os.CreateTemp(b.runtimeRoot, ".migration-public-github-")
	if err != nil {
		return result, err
	}
	defer os.Remove(fragment.Name())
	if _, err := fragment.Write(github); err != nil {
		fragment.Close()
		return result, err
	}
	if err := fragment.Close(); err != nil {
		return result, err
	}
	script := "set -eu\numask 077\ninstall -m 0600 /input/catalog.json /run/preparation/catalog.json\ninstall -m 0600 /input/github.toml /run/preparation/github.toml\nexec /opt/loki/bin/loki host prepare-secrets --metadata-only --uid 10006 --gid 10006 --agent-uid 10000 --metadata-gid 10001 --identity /var/lib/loki/app-store/deployment.json --state /var/lib/loki/app-store --service-state /var/lib/loki/secrets --runtime /run/loki/secrets --config /etc/loki/loki.toml --github-config /run/preparation/github.toml --catalog /run/preparation/catalog.json --execution-contract /usr/share/doc/loki/container-execution-contract.json"
	if empty {
		script += " --require-empty-generation"
	}
	appMount := "type=volume,source=" + volume + ",target=/var/lib/loki/app-store,readonly,volume-nocopy"
	var mounts []string
	if candidate != nil {
		if !controlpolicy.ApplicationUUID.MatchString(candidate.Operation) || len(candidate.CandidateGeneration) != 32 || lifecycle.MigrationOperationUUID(candidate.CandidateGeneration) == "" || !validSHA256(candidate.Policy) || candidate.Before.UID != 10006 || candidate.Before.GID != 10006 || candidate.Before.AgentUID != 10000 || candidate.Before.MetadataGID != 10001 || !selectCandidate && (!filepath.IsAbs(projection) || filepath.Clean(projection) != projection) {
			return result, errComposeMigration
		}
		// The JSON identity contains only validated public identifiers. Pass it
		// as a shell positional parameter, never interpolate data into code.
		script = strings.Replace(script, "--metadata-only", "--candidate-generation \"$1\" --candidate-before \"$2\" --candidate-operation \"$3\" --unit-directory= --overlay-directory=", 1)
		if selectCandidate {
			script += " --commit-candidate --candidate-policy \"$4\""
			if candidate.Restore {
				script += " --restore-candidate"
			}
		} else {
			if candidate.Restore {
				script += " --initial-maintenance-operation \"$3\""
			}
			script = "set -eu\ntest ! -L /run/loki/secrets\nif test \"$(stat -c %u /run/loki/secrets)\" != 10006; then test -z \"$(ls -A /run/loki/secrets)\"; install -d -o 0 -g 0 -m 0700 /run/loki/secrets; fi\n" + script
			mounts = []string{"--mount", applicationBind(projection, "/run/loki/secrets", false)}
		}
		appMount = "type=volume,source=" + volume + ",target=/var/lib/loki/app-store,volume-nocopy"
	}
	args := []string{"run", "--rm", "--network", "none", "--read-only", "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--cap-add", "CHOWN", "--cap-add", "FOWNER", "--security-opt", "no-new-privileges:true", "--user", "0:0", "--memory", "134217728", "--pids-limit", "32", "--log-driver", "none", "--tmpfs", "/run/preparation:rw,nosuid,nodev,size=2097152,mode=0700", "--entrypoint", "/bin/sh", "--mount", appMount, "--mount", applicationBind(file.Name(), "/input/catalog.json", true), "--mount", applicationBind(fragment.Name(), "/input/github.toml", true)}
	args = append(args, mounts...)
	args = append(args, state.CoreImage, "-ec", script)
	if candidate != nil {
		i := candidate
		before, _ := json.Marshal(i.Before)
		args = append(args, "host-application-prepare", i.CandidateGeneration, string(before), i.Operation, i.Policy)
	}
	body, err := private.RunPrivate(ctx, nil, nil, args...)
	if selectCandidate && candidate != nil {
		i := candidate
		want := i.Before
		want.Version, want.ActiveGeneration, want.PolicySHA256 = 2, i.CandidateGeneration, i.Policy
		if err != nil || json.Unmarshal(body, &result.Identity) != nil || result.Identity != want {
			return result, errComposeMigration
		}
		result.Policy = i.Policy
		return result, nil
	}
	if candidate != nil {
		var prepared struct {
			Prepared   bool   `json:"prepared"`
			Candidate  bool   `json:"candidate"`
			Generation string `json:"active_generation"`
			Policy     string `json:"policy"`
			UID        uint32 `json:"broker_uid"`
			GID        uint32 `json:"broker_gid"`
			Deployment string `json:"deployment_id"`
			Runner     string `json:"runner_id"`
		}
		i := candidate
		if err != nil || json.Unmarshal(body, &prepared) != nil || !prepared.Prepared || !prepared.Candidate || prepared.Generation != i.CandidateGeneration || prepared.Policy != i.Policy || prepared.UID != i.Before.UID || prepared.GID != i.Before.GID || prepared.Deployment != i.Before.DeploymentID || prepared.Runner != i.Before.RunnerID {
			return result, errComposeMigration
		}
		result.Identity = i.Before
		result.Identity.Version, result.Identity.ActiveGeneration, result.Identity.PolicySHA256 = 2, i.CandidateGeneration, i.Policy
		result.Policy = i.Policy
		return result, nil
	}
	if err != nil || json.Unmarshal(body, &result) != nil || !validSHA256(result.Policy) || result.Identity.UID != 10006 || result.Identity.GID != 10006 || result.Identity.AgentUID != 10000 || result.Identity.MetadataGID != 10001 || result.BaseEnvironment == nil {
		return migrationMetadata{}, errComposeMigration
	}
	return result, nil
}

// PrepareSecretMigration reads source bytes only inside the finite root helper.
// The operator gets a complete immutable public plan; no generation, host
// operation, key session or source fence is created during preparation.
func (b *Backend) PrepareSecretMigration(ctx context.Context, mappings []appvalues.MigrationProfileMapping, resources []controlpolicy.ApplicationResource, recipient, backup string, interrupt bool) (lifecycle.SecretMigrationIntent, error) {
	var intent lifecycle.SecretMigrationIntent
	key, err := age.ParseX25519Recipient(recipient)
	if err != nil || key.String() != recipient {
		return intent, errComposeMigration
	}
	if err := lifecycle.ValidateMigrationBackupDestination(backup); err != nil {
		return intent, err
	}
	lock, err := lifecycle.AcquireOperationLock(b.root)
	if err != nil {
		return intent, err
	}
	defer lock.Close()
	j, err := lifecycle.OpenOperationJournal(b.root, lock, lifecycle.OperationJournalOptions{})
	if err != nil {
		return intent, err
	}
	if _, found, err := j.Active(); err != nil || found {
		return intent, errComposeMigration
	}
	state, found, err := b.loadRuntime()
	if err != nil || !found {
		return intent, errComposeMigration
	}
	var previous struct {
		Resources []controlpolicy.ApplicationResource `json:"resources"`
	}
	catalogPath := filepath.Join(b.root, "application-resources.json")
	if _, err := os.Lstat(catalogPath); os.IsNotExist(err) {
		previous.Resources = []controlpolicy.ApplicationResource{}
	} else if err != nil {
		return intent, err
	} else if err := readPrivateJSON(catalogPath, &previous); err != nil {
		return intent, err
	}
	if previous.Resources == nil {
		return intent, errComposeMigration
	}
	before, err := b.migrationMetadata(ctx, state, previous.Resources, true)
	if err != nil || before.Policy != state.ApplicationPolicySHA256 {
		return intent, errComposeMigration
	}
	canonical, err := controlpolicy.CanonicalApplications(resources, before.BaseEnvironment)
	if err != nil {
		return intent, err
	}
	target, err := b.migrationMetadata(ctx, state, canonical, true)
	if err != nil || target.Identity != before.Identity || !reflect.DeepEqual(target.BaseEnvironment, before.BaseEnvironment) {
		return intent, errComposeMigration
	}
	source, err := b.migrationVolume(ctx, "runtime-state")
	if err != nil {
		return intent, err
	}
	exchange, ok := b.runner.(PrivateExchangeRunner)
	if !ok {
		return intent, errComposeMigration
	}
	manifest := lifecycle.SourceTransferManifest{Action: "prepare", Version: 1, SourceRoot: "/var/lib/loki/runtime", Policy: target.Policy, Plan: appvalues.MigrationPlan{Version: 1, Mappings: mappings, Resources: canonical}, Writes: []lifecycle.MigrationWriteCheckpoint{}, BaseEnvironment: target.BaseEnvironment}
	// Source inspection takes the owner's existing writer lock. The helper
	// needs that lock writable, but has no initialize/mutation operation here.
	args := []string{"run", "--rm", "--interactive", "--network", "none", "--read-only", "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--security-opt", "no-new-privileges:true", "--user", "0:0", "--memory", "2147483648", "--pids-limit", "64", "--log-driver", "none", "--entrypoint", "/opt/loki/bin/loki", "--mount", "type=volume,source=" + source + ",target=/var/lib/loki/runtime,volume-nocopy", state.CoreImage, "host", "secret-migration-source"}
	var plan appvalues.MigrationPlan
	err = exchange.ExchangePrivate(ctx, nil, args, func(channel io.ReadWriteCloser) error {
		if err := lifecycle.WriteSourceTransferManifest(channel, manifest); err != nil {
			return err
		}
		body, err := privateio.ReadBytes(channel, lifecycle.MaxSourceManifestBytes)
		if err != nil {
			return err
		}
		defer clear(body)
		decoder := json.NewDecoder(bytes.NewReader(body))
		decoder.DisallowUnknownFields()
		if decoder.Decode(&plan) != nil || decoder.Decode(new(any)) != io.EOF {
			return errComposeMigration
		}
		return nil
	})
	if err != nil {
		return intent, err
	}
	var generation [16]byte
	if _, err := rand.Read(generation[:]); err != nil {
		return intent, err
	}
	previous.Resources, err = controlpolicy.CanonicalApplications(previous.Resources, before.BaseEnvironment)
	if err != nil {
		return intent, err
	}
	intent = lifecycle.SecretMigrationIntent{Plan: plan, Before: before.Identity, BeforeResources: previous.Resources, CandidateGeneration: hex.EncodeToString(generation[:]), ExpectedPolicy: before.Policy, Policy: target.Policy, Mode: "compose", ContainerImage: state.CoreImage, ContainerGeneration: state.GenerationID, SourceRoot: "/var/lib/loki/runtime", IdentityFile: "/var/lib/loki/app-store/deployment.json", StateRoot: "/var/lib/loki/app-store", ServiceStateRoot: "/var/lib/loki/secrets", RuntimeRoot: "/run/loki/secrets", Binary: "/opt/loki/bin/devtools", Recipient: recipient, BackupPath: backup, Interrupt: interrupt}
	if !intent.Valid() || !reflect.DeepEqual(plan.Resources, canonical) || strings.ContainsAny(recipient, "\r\n") {
		return lifecycle.SecretMigrationIntent{}, errComposeMigration
	}
	return intent, nil
}
