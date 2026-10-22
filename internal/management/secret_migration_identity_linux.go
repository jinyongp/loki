//go:build linux

package management

import (
	"context"
	"encoding/json"
	"fmt"

	hostapplication "loki/internal/host/application"
)

// InspectSecretMigrationIdentity reads public selector/directory metadata only.
// Legacy-source migration requires an unmaterialized app generation; existing
// values or mutation history must use the application backup/restore path.
func (b *DockerFullBackend) InspectSecretMigrationIdentity(ctx context.Context) (hostapplication.Identity, error) {
	return b.inspectApplicationIdentity(ctx, true)
}

func (b *DockerFullBackend) inspectApplicationIdentity(ctx context.Context, requireEmpty bool) (hostapplication.Identity, error) {
	var identity hostapplication.Identity
	resources, err := b.Store.FullResources()
	if err != nil {
		return identity, err
	}
	worker, err := resources.ServiceProgram("runtime-core", "loki")
	if err != nil {
		return identity, err
	}
	image, err := resources.Image("runtime-core", "service")
	if err != nil {
		return identity, err
	}
	if err := b.pinnedImage(ctx, image); err != nil {
		return identity, err
	}
	volume, err := b.verifiedMigrationVolume(ctx, FullMount{Kind: "volume", Source: b.Store.FullOwner() + "-data-secrets", Target: "/var/lib/loki/secrets", ReadOnly: true})
	if err != nil {
		return identity, err
	}
	mounts := []FullMount{volume}
	for _, program := range resources.Plan.Programs {
		if program.Module == "runtime-core" {
			mounts = append(mounts, fullModuleMount(program))
		}
	}
	if len(mounts) != 2 {
		return identity, fmt.Errorf("identity inspection requires exactly the immutable core payload")
	}
	args := []string{"run", "--rm", "--network", "none", "--read-only", "--user", "0:0", "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--security-opt", "no-new-privileges:true", "--memory", "134217728", "--pids-limit", "32", "--log-driver", "none", "--entrypoint", worker}
	for _, mount := range mounts {
		value, err := dockerMountArgument(mount)
		if err != nil {
			return identity, err
		}
		args = append(args, "--mount", value)
	}
	args = append(args, image, "host", "prepare-secrets", "--inspect-identity", "--identity", "/var/lib/loki/secrets/deployment.json", "--state", "/var/lib/loki/secrets")
	if requireEmpty {
		args = append(args, "--require-empty-generation")
	}
	result, err := b.command(ctx, "inspecting original empty app-generation metadata", nil, args...)
	if err != nil {
		return identity, err
	}
	if json.Unmarshal(result, &identity) != nil || identity.UID != 10006 || identity.GID != 10006 || identity.AgentUID != 10000 || identity.MetadataGID != 10001 {
		return hostapplication.Identity{}, fmt.Errorf("original app metadata differs from the installed full roles")
	}
	return identity, nil
}
