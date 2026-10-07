package management

import (
	"context"
	"encoding/json"
	"fmt"
	"path/filepath"
	"strings"
)

// Inspect the daemon's persistent ownership records, including retained data
// of currently disabled or uninstalled tools. No container or volume prefix
// alone grants filesystem access.
func (b *DockerFullBackend) OwnedDataPaths(ctx context.Context) (map[string]string, error) {
	data, err := b.command(ctx, "listing owned data volumes", nil, "volume", "ls", "--filter", "label="+fullOwnerLabel+"="+b.Store.FullOwner(), "--format", "{{.Name}}")
	if err != nil {
		return nil, err
	}
	names, err := dockerLines(data)
	if err != nil {
		return nil, err
	}
	paths := map[string]string{}
	for _, name := range names {
		if !strings.HasPrefix(name, b.Store.FullOwner()+"-data-") {
			continue
		}
		path, err := b.backupDataPath(ctx, name)
		if err != nil {
			return nil, err
		}
		paths[name] = path
	}
	return paths, nil
}

func (b *DockerFullBackend) backupDataPath(ctx context.Context, name string) (string, error) {
	allowed := false
	for owner := range fullDataTargets {
		allowed = allowed || name == b.Store.FullOwner()+"-data-"+strings.ReplaceAll(owner, "/", "-")
	}
	if !allowed {
		return "", fmt.Errorf("unknown persistent volume; preserving it")
	}
	data, err := b.command(ctx, "verifying backup data ownership", nil, "volume", "inspect", "--format", "{{json .}}", name)
	if err != nil {
		return "", err
	}
	var volume struct {
		Name, Driver, Mountpoint string
		Labels, Options          map[string]string
	}
	if json.Unmarshal(data, &volume) != nil || volume.Name != name || volume.Driver != "local" || len(volume.Options) != 0 || volume.Labels[fullOwnerLabel] != b.Store.FullOwner() || volume.Labels[fullResourceLabel] != name || !filepath.IsAbs(volume.Mountpoint) || filepath.Base(volume.Mountpoint) != "_data" {
		return "", fmt.Errorf("backup data volume has foreign or unsupported ownership")
	}
	data, err = b.command(ctx, "checking data volume writers", nil, "container", "ls", "--all", "--filter", "volume="+name, "--format", "{{.ID}}")
	if err != nil {
		return "", err
	}
	attached, err := dockerLines(data)
	if err != nil {
		return "", err
	}
	if len(attached) != 0 {
		return "", fmt.Errorf("data volume still has attached containers; stop services before backup or restore")
	}
	if err := realDirectories(volume.Mountpoint, filepath.Dir(volume.Mountpoint)); err != nil {
		return "", err
	}
	return volume.Mountpoint, nil
}

func (b *DockerFullBackend) EnsureDataPaths(ctx context.Context, names []string) (map[string]string, error) {
	paths := map[string]string{}
	for _, name := range names {
		allowed := false
		for owner := range fullDataTargets {
			allowed = allowed || name == b.Store.FullOwner()+"-data-"+strings.ReplaceAll(owner, "/", "-")
		}
		if !allowed {
			return nil, fmt.Errorf("unknown backup volume")
		}
		if err := b.ownedVolume(ctx, FullMount{Kind: "volume", Source: name}); err != nil {
			return nil, err
		}
		path, err := b.backupDataPath(ctx, name)
		if err != nil {
			return nil, err
		}
		paths[name] = path
	}
	return paths, nil
}

func (b *DockerFullBackend) RequireIdle(ctx context.Context) error {
	reservations, err := b.Store.Deployments()
	if err != nil {
		return err
	}
	for _, reservation := range reservations {
		ids, err := b.containerIDs(ctx, reservation)
		if err != nil {
			return err
		}
		for _, id := range ids {
			c, err := b.inspectContainer(ctx, id)
			if err != nil {
				return err
			}
			if err := b.requireParent(reservation, c); err != nil {
				return err
			}
			if c.Config.Labels[fullServiceLabel] == "" && c.State.Running {
				return fmt.Errorf("active jobs block maintenance; let jobs finish before retrying")
			}
		}
	}
	return nil
}

func (b *DockerFullBackend) PurgeOwnedData(ctx context.Context) error {
	paths, err := b.OwnedDataPaths(ctx)
	if err != nil {
		return err
	}
	for name := range paths {
		if _, err := b.command(ctx, "removing owned tool data", nil, "volume", "rm", name); err != nil {
			return err
		}
	}
	return nil
}
