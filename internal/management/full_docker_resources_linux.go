package management

import (
	"context"
	"encoding/csv"
	"encoding/json"
	"fmt"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
)

const fullOwnerLabel = "io.loki.full.owner"
const fullDeploymentLabel = "io.loki.full.deployment"
const fullServiceLabel = "io.loki.full.service"
const fullPlanLabel = "io.loki.full.plan"
const fullResourceLabel = "io.loki.full.resource"

var dockerIDPattern = regexp.MustCompile(`^[a-f0-9]{12,64}$`)
var dockerNamePattern = regexp.MustCompile(`^[a-z0-9][a-z0-9-]{0,127}$`)

func dockerMountArgument(mount FullMount) (string, error) {
	if mount.Kind != "volume" && mount.Kind != "bind" || !filepath.IsAbs(mount.Target) || filepath.Clean(mount.Target) != mount.Target {
		return "", fmt.Errorf("invalid full service mount")
	}
	if mount.Kind == "bind" && !filepath.IsAbs(mount.Source) || mount.Kind == "volume" && !dockerNamePattern.MatchString(mount.Source) {
		return "", fmt.Errorf("invalid full mount source")
	}
	fields := []string{"type=" + mount.Kind, "source=" + mount.Source, "target=" + mount.Target}
	if mount.ReadOnly {
		fields = append(fields, "readonly")
	}
	var result strings.Builder
	writer := csv.NewWriter(&result)
	if err := writer.Write(fields); err != nil {
		return "", err
	}
	writer.Flush()
	if err := writer.Error(); err != nil {
		return "", err
	}
	return strings.TrimSuffix(result.String(), "\n"), nil
}

func dockerLines(data []byte) ([]string, error) {
	text := strings.TrimSpace(string(data))
	if text == "" {
		return []string{}, nil
	}
	lines := strings.Split(text, "\n")
	if len(lines) > 4096 {
		return nil, fmt.Errorf("owned Docker resource count exceeds its bound")
	}
	for _, line := range lines {
		if line == "" || strings.ContainsAny(line, "\x00\r") {
			return nil, fmt.Errorf("invalid Docker resource identity output")
		}
	}
	return lines, nil
}

func (b *DockerFullBackend) ownedVolume(ctx context.Context, mount FullMount) error {
	owner := b.Store.FullOwner()
	if mount.Kind != "volume" || !strings.HasPrefix(mount.Source, owner+"-") || !dockerNamePattern.MatchString(mount.Source) {
		return fmt.Errorf("full volume does not belong to this host")
	}
	data, err := b.command(ctx, "checking an owned data volume", nil, "volume", "ls", "--filter", "name="+mount.Source, "--format", "{{.Name}}")
	if err != nil {
		return err
	}
	lines, err := dockerLines(data)
	if err != nil {
		return err
	}
	exists := false
	for _, name := range lines {
		if name == mount.Source {
			exists = true
		}
	}
	if !exists {
		if _, err := b.command(ctx, "creating an owned data volume", nil, "volume", "create", "--label", fullOwnerLabel+"="+owner, "--label", fullResourceLabel+"="+mount.Source, mount.Source); err != nil {
			return err
		}
	}
	data, err = b.command(ctx, "verifying data volume ownership", nil, "volume", "inspect", "--format", "{{json .}}", mount.Source)
	if err != nil {
		return err
	}
	var volume struct {
		Name       string
		Driver     string
		Mountpoint string
		Labels     map[string]string
	}
	if err := json.Unmarshal(data, &volume); err != nil {
		return err
	}
	if volume.Name != mount.Source || volume.Driver != "local" || volume.Labels[fullOwnerLabel] != owner || volume.Labels[fullResourceLabel] != mount.Source || !filepath.IsAbs(volume.Mountpoint) {
		return fmt.Errorf("existing volume is foreign or has an unsupported driver; preserving it")
	}
	return nil
}

func (b *DockerFullBackend) ownedNetwork(ctx context.Context, name string, internal bool) error {
	owner := b.Store.FullOwner()
	if !strings.HasPrefix(name, owner+"-") || !dockerNamePattern.MatchString(name) {
		return fmt.Errorf("full network does not belong to this host")
	}
	data, err := b.command(ctx, "checking an owned network", nil, "network", "ls", "--filter", "name="+name, "--format", "{{.Name}}")
	if err != nil {
		return err
	}
	lines, err := dockerLines(data)
	if err != nil {
		return err
	}
	exists := false
	for _, current := range lines {
		if current == name {
			exists = true
		}
	}
	if !exists {
		args := []string{"network", "create", "--driver", "bridge", "--label", fullOwnerLabel + "=" + owner, "--label", fullResourceLabel + "=" + name}
		if internal {
			args = append(args, "--internal")
		}
		args = append(args, name)
		if _, err := b.command(ctx, "creating an owned network", nil, args...); err != nil {
			return err
		}
	}
	data, err = b.command(ctx, "verifying network ownership", nil, "network", "inspect", "--format", "{{json .}}", name)
	if err != nil {
		return err
	}
	var network struct {
		Name, Driver string
		Internal     bool
		Labels       map[string]string
	}
	if err := json.Unmarshal(data, &network); err != nil {
		return err
	}
	if network.Name != name || network.Driver != "bridge" || network.Internal != internal || network.Labels[fullOwnerLabel] != owner || network.Labels[fullResourceLabel] != name {
		return fmt.Errorf("existing network is foreign or has a different policy; preserving it")
	}
	return nil
}

func (b *DockerFullBackend) pinnedImage(ctx context.Context, reference string) error {
	if !imageDigestPattern.MatchString(reference) {
		return fmt.Errorf("full services require a digest-pinned image")
	}
	data, err := b.command(ctx, "checking native service image identity", nil, "image", "inspect", "--format", "{{json .}}", reference)
	if err != nil {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		fmt.Fprintf(b.Diagnostics, "Acquiring pinned service image %s...\n", reference)
		if _, err := b.command(ctx, "acquiring a pinned service image", nil, "image", "pull", reference); err != nil {
			return err
		}
		data, err = b.command(ctx, "checking native service image identity", nil, "image", "inspect", "--format", "{{json .}}", reference)
		if err != nil {
			return err
		}
	}
	var image struct {
		Os, Architecture string
		RepoDigests      []string
	}
	if err := json.Unmarshal(data, &image); err != nil {
		return err
	}
	if image.Os != "linux" || image.Architecture != runtime.GOARCH {
		return fmt.Errorf("full service image does not match this Linux execution host")
	}
	expected := reference[strings.LastIndex(reference, "@"):]
	for _, digest := range image.RepoDigests {
		if strings.HasSuffix(digest, expected) {
			return nil
		}
	}
	return fmt.Errorf("service image does not retain its trusted repository digest")
}
