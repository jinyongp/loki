//go:build linux

package compose

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"

	"golang.org/x/sys/unix"
	"loki/internal/host/lifecycle"
)

func (b *Backend) migrationProjection(r lifecycle.OperationRecord) (string, error) {
	return b.applicationCandidateProjection("migration", r.ID)
}

func (b *Backend) applicationCandidateProjection(kind, id string) (string, error) {
	if kind != "migration" && kind != "restore" || len(id) != 32 || lifecycle.MigrationOperationUUID(id) == "" {
		return "", errComposeMigration
	}
	parent := filepath.Join("/dev/shm", "loki-tools-"+applicationOwnerID(b.root)+"-"+kind+"-"+id)
	if err := os.Mkdir(parent, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	var operator, st unix.Stat_t
	var filesystem unix.Statfs_t
	if unix.Lstat(b.root, &operator) != nil || unix.Lstat(parent, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Mode&07777 != 0700 || st.Uid != operator.Uid || unix.Statfs(parent, &filesystem) != nil || filesystem.Type != unix.TMPFS_MAGIC {
		return "", errComposeMigration
	}
	path := filepath.Join(parent, "secrets")
	if err := os.Mkdir(path, 0700); err != nil && !errors.Is(err, os.ErrExist) {
		return "", err
	}
	if unix.Lstat(path, &st) != nil || st.Mode&unix.S_IFMT != unix.S_IFDIR || st.Mode&07777 != 0700 || st.Uid != operator.Uid && st.Uid != 0 && st.Uid != 10006 {
		return "", errComposeMigration
	}
	return path, nil
}

func (b *Backend) migrationGenerationSource(ctx context.Context, r lifecycle.OperationRecord) (string, error) {
	return b.applicationGenerationSource(ctx, r.SecretMigration.Intent.CandidateGeneration)
}

func (b *Backend) applicationGenerationSource(ctx context.Context, generation string) (string, error) {
	if len(generation) != 32 || lifecycle.MigrationOperationUUID(generation) == "" {
		return "", errComposeMigration
	}
	volume, err := b.migrationVolume(ctx, "application-state")
	if err != nil {
		return "", err
	}
	body, err := b.runner.Run(ctx, nil, "volume", "inspect", volume)
	if err != nil {
		return "", err
	}
	var metadata []struct{ Mountpoint string }
	if json.Unmarshal(body, &metadata) != nil || len(metadata) != 1 || !filepath.IsAbs(metadata[0].Mountpoint) || filepath.Clean(metadata[0].Mountpoint) != metadata[0].Mountpoint || metadata[0].Mountpoint == "/" || strings.ContainsAny(metadata[0].Mountpoint, "\x00\r\n") {
		return "", errComposeMigration
	}
	return filepath.Join(metadata[0].Mountpoint, "generations", generation), nil
}

type applicationCandidateBinding struct{ kind, operation, generation, image string }

func migrationCandidateBinding(r lifecycle.OperationRecord) applicationCandidateBinding {
	return applicationCandidateBinding{kind: "migration", operation: r.ID, generation: r.SecretMigration.Intent.CandidateGeneration, image: r.SecretMigration.Intent.ContainerImage}
}

func (b *Backend) applicationCandidateName(binding applicationCandidateBinding, purpose string) string {
	return b.project + "-secret-" + binding.kind + "-" + binding.operation + "-" + purpose
}

func (b *Backend) applicationCandidateLabels(binding applicationCandidateBinding, purpose string) []string {
	prefix := "io.loki." + binding.kind + "."
	return []string{"--label", prefix + "owner=" + applicationOwnerID(b.root), "--label", prefix + "operation=" + binding.operation, "--label", prefix + "generation=" + binding.generation, "--label", prefix + "purpose=" + purpose}
}

func (b *Backend) migrationContainerName(r lifecycle.OperationRecord, purpose string) string {
	return b.project + "-secret-migration-" + r.ID + "-" + purpose
}

func (b *Backend) migrationLabels(r lifecycle.OperationRecord, purpose string) []string {
	return []string{"--label", "io.loki.migration.owner=" + applicationOwnerID(b.root), "--label", "io.loki.migration.operation=" + r.ID, "--label", "io.loki.migration.generation=" + r.SecretMigration.Intent.CandidateGeneration, "--label", "io.loki.migration.purpose=" + purpose}
}

type migrationContainer struct {
	ID, Name string
	Config   struct {
		Image, User     string
		Labels          map[string]string
		Entrypoint, Cmd []string
	}
	HostConfig struct {
		NetworkMode, IpcMode string
		CapAdd, CapDrop      []string
		GroupAdd             []string
		Privileged           bool
		ReadonlyRootfs       bool
		Memory               int64
		PidsLimit            *int64
		RestartPolicy        struct{ Name string }
		LogConfig            struct{ Type string }
	}
	State  struct{ Running bool }
	Mounts []struct {
		Type, Name, Source, Destination string
		RW                              bool
	}
}

// Inspect the exact original container and its confinement before either
// reusing it or signalling it. A matching name/label alone is insufficient.
func (b *Backend) inspectMigrationCandidate(ctx context.Context, r lifecycle.OperationRecord, projection, generation string) (migrationContainer, bool, error) {
	return b.inspectApplicationCandidate(ctx, migrationCandidateBinding(r), projection, generation)
}

func (b *Backend) inspectApplicationCandidate(ctx context.Context, binding applicationCandidateBinding, projection, generation string) (migrationContainer, bool, error) {
	var result migrationContainer
	if binding.kind != "migration" && binding.kind != "restore" || lifecycle.MigrationOperationUUID(binding.operation) == "" || len(binding.generation) != 32 || lifecycle.MigrationOperationUUID(binding.generation) == "" || !pinnedRestoreCandidateImage(binding.image) {
		return result, false, errComposeMigration
	}
	name := b.applicationCandidateName(binding, "candidate")
	ids, err := b.runner.Run(ctx, nil, "container", "ls", "--all", "--filter", "name=^/"+name+"$", "--format", "{{.ID}}")
	if err != nil {
		return result, false, err
	}
	if strings.TrimSpace(string(ids)) == "" {
		return result, false, nil
	}
	if len(strings.Fields(string(ids))) != 1 {
		return result, false, errComposeMigration
	}
	body, err := b.runner.Run(ctx, nil, "container", "inspect", name)
	if err != nil {
		return result, false, err
	}
	defer clear(body)
	var containers []migrationContainer
	if json.Unmarshal(body, &containers) != nil || len(containers) != 1 {
		return result, false, errComposeMigration
	}
	c := containers[0]
	labels := c.Config.Labels
	prefix := "io.loki." + binding.kind + "."
	if c.Name != "/"+name || labels[prefix+"owner"] != applicationOwnerID(b.root) || labels[prefix+"operation"] != binding.operation || labels[prefix+"generation"] != binding.generation || labels[prefix+"purpose"] != "candidate" || c.Config.Image != binding.image || c.Config.User != "10006:10006" || !reflect.DeepEqual(c.Config.Entrypoint, []string{"/opt/loki/bin/loki-secret-broker"}) || !reflect.DeepEqual(c.Config.Cmd, []string{"--layout", "/run/loki/secrets/layout.json"}) || c.HostConfig.Privileged || !c.HostConfig.ReadonlyRootfs || c.HostConfig.NetworkMode != "none" || c.HostConfig.Memory != 1<<30 || c.HostConfig.PidsLimit == nil || *c.HostConfig.PidsLimit != 64 || c.HostConfig.RestartPolicy.Name != "no" || c.HostConfig.LogConfig.Type != "none" || len(c.HostConfig.CapAdd) != 0 || !reflect.DeepEqual(c.HostConfig.CapDrop, []string{"ALL"}) || !reflect.DeepEqual(c.HostConfig.GroupAdd, []string{"10001"}) || len(c.Mounts) != 3 {
		return result, false, errComposeMigration
	}
	want := map[string]struct {
		source string
		rw     bool
	}{"/var/lib/loki/secrets": {generation, true}, "/run/loki/secrets": {projection, true}, "/run/loki/secrets/layout.json": {filepath.Join(projection, "layout.json"), false}}
	for _, mount := range c.Mounts {
		expected, exists := want[mount.Destination]
		if !exists || mount.Type != "bind" || mount.Source != expected.source || mount.RW != expected.rw {
			return result, false, errComposeMigration
		}
		delete(want, mount.Destination)
	}
	return c, true, nil
}

func pinnedRestoreCandidateImage(image string) bool {
	name, digest, ok := strings.Cut(image, "@")
	return ok && name != "" && !strings.ContainsAny(name, " \t\r\n\x00@") && strings.HasPrefix(digest, "sha256:") && validSHA256(strings.TrimPrefix(digest, "sha256:"))
}

func (b *Backend) prepareMigrationCandidate(ctx context.Context) (string, error) {
	r, _, err := b.migrationRecord(ctx)
	if err != nil || r.SecretMigration.RollbackRequested || r.State != lifecycle.OperationApplying || r.Phase != lifecycle.PhaseAdmitted && r.Phase != lifecycle.PhaseSnapshot && r.Phase != lifecycle.PhaseMigrate {
		return "", errComposeMigration
	}
	state, _, err := b.loadRuntime()
	if err != nil {
		return "", err
	}
	before, err := b.migrationMetadata(ctx, state, r.SecretMigration.Intent.BeforeResources, true)
	if err != nil || before.Identity != r.SecretMigration.Intent.Before || before.Policy != r.SecretMigration.Intent.ExpectedPolicy {
		return "", errComposeMigration
	}
	projection, err := b.migrationProjection(r)
	if err != nil {
		return "", err
	}
	if _, err := b.migrationLayout(ctx, state, r.SecretMigration.Intent.Plan.Resources, false, &r, projection, false); err != nil {
		return "", err
	}
	return projection, nil
}

func (b *Backend) startMigrationCandidate(ctx context.Context) error {
	r, _, err := b.migrationRecord(ctx)
	if err != nil || r.Phase != lifecycle.PhaseMigrate || r.SecretMigration.RollbackRequested {
		return errComposeMigration
	}
	projection, err := b.prepareMigrationCandidate(ctx)
	if err != nil {
		return err
	}
	generation, err := b.migrationGenerationSource(ctx, r)
	if err != nil {
		return err
	}
	return b.startApplicationCandidate(ctx, migrationCandidateBinding(r), projection, generation)
}

func (b *Backend) startApplicationCandidate(ctx context.Context, binding applicationCandidateBinding, projection, generation string) error {
	c, found, err := b.inspectApplicationCandidate(ctx, binding, projection, generation)
	if err != nil {
		return err
	}
	if found {
		if !c.State.Running {
			if _, err := b.runner.Run(ctx, nil, "container", "start", c.ID); err != nil {
				return err
			}
		}
		return nil
	}
	args := []string{"run", "--detach", "--name", b.applicationCandidateName(binding, "candidate"), "--network", "none", "--read-only", "--cap-drop", "ALL", "--security-opt", "no-new-privileges:true", "--user", "10006:10006", "--group-add", "10001", "--memory", "1073741824", "--pids-limit", "64", "--restart", "no", "--log-driver", "none", "--no-healthcheck", "--tmpfs", "/tmp:rw,nosuid,nodev,size=134217728,uid=10006,gid=10006,mode=0700", "--tmpfs", "/var/tmp:rw,nosuid,nodev,size=134217728,uid=10006,gid=10006,mode=0700", "--mount", applicationBind(generation, "/var/lib/loki/secrets", false), "--mount", applicationBind(projection, "/run/loki/secrets", false), "--mount", applicationBind(filepath.Join(projection, "layout.json"), "/run/loki/secrets/layout.json", true), "--entrypoint", "/opt/loki/bin/loki-secret-broker"}
	args = append(args, b.applicationCandidateLabels(binding, "candidate")...)
	args = append(args, binding.image, "--layout", "/run/loki/secrets/layout.json")
	if _, err := b.runner.Run(ctx, nil, args...); err != nil {
		return err
	}
	_, found, err = b.inspectApplicationCandidate(ctx, binding, projection, generation)
	if err != nil || !found {
		return errComposeMigration
	}
	return nil
}

func (b *Backend) stopMigrationCandidate(ctx context.Context) error {
	r, _, err := b.migrationRecord(ctx)
	if err != nil {
		return err
	}
	// Derive paths without materializing anything on a cleanup/reboot path.
	projection := filepath.Join("/dev/shm", "loki-tools-"+applicationOwnerID(b.root)+"-migration-"+r.ID, "secrets")
	generation, err := b.migrationGenerationSource(ctx, r)
	if err != nil {
		return err
	}
	return b.stopApplicationCandidate(ctx, migrationCandidateBinding(r), projection, generation)
}

func (b *Backend) stopApplicationCandidate(ctx context.Context, binding applicationCandidateBinding, projection, generation string) error {
	c, found, err := b.inspectApplicationCandidate(ctx, binding, projection, generation)
	if err != nil || !found {
		return err
	}
	if c.State.Running {
		if _, err := b.runner.Run(ctx, nil, "container", "stop", "--timeout", "15", c.ID); err != nil {
			return err
		}
	}
	_, err = b.runner.Run(ctx, nil, "container", "rm", c.ID)
	return err
}
