package management

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"

	"loki/internal/tools"
)

func (b *DockerFullBackend) Administration(ctx context.Context, request any) (json.RawMessage, error) {
	plan, err := b.Store.PlanFull()
	if err != nil {
		return nil, err
	}
	reservations, err := b.Store.Deployments()
	if err != nil {
		return nil, err
	}
	for _, reservation := range reservations {
		if reservation.PlanDigest != fullPlanDigest(plan) || !slices.Contains(reservation.Services, "runtime") {
			continue
		}
		record, err := b.record(reservation)
		if err != nil {
			return nil, err
		}
		for _, spec := range record.Specs {
			if spec.Service != "runtime" {
				continue
			}
			inspection, err := b.inspectContainer(ctx, spec.Name)
			if err != nil {
				return nil, err
			}
			if err := b.verifySpec(reservation, spec, inspection); err != nil {
				return nil, err
			}
			if !inspection.State.Running {
				return nil, fmt.Errorf("runtime is stopped; run loki tools start")
			}
			data, err := json.Marshal(request)
			if err != nil {
				return nil, err
			}
			defer clear(data)
			if len(data) > tools.MaxManifestBytes {
				return nil, fmt.Errorf("integration request exceeds its bound")
			}
			result, err := b.command(ctx, "checking integration authorization", bytes.NewReader(data), "container", "exec", "--interactive", "--user", "0:10001", spec.Name, record.Worker, "full-administration")
			if err != nil {
				var failure *dockerCommandFailure
				if errors.As(err, &failure) {
					for _, line := range strings.Split(failure.diagnostics, "\n") {
						if message, ok := strings.CutPrefix(line, "loki: "); ok {
							return nil, errors.New(message)
						}
					}
				}
				return nil, err
			}
			if !json.Valid(result) {
				return nil, fmt.Errorf("invalid protected integration response")
			}
			return json.RawMessage(result), nil
		}
	}
	return nil, fmt.Errorf("selected runtime has not started; run loki tools start")
}

// Import uses a networkless administrator container with only the GitHub
// provider volume, the immutable worker and read-only activation. Neither Git
// executables nor application-secret data are present. The same persisted pin
// and absence proof as ordinary service starts cover an interrupted import.
func (b *DockerFullBackend) ImportGitHub(ctx context.Context, configuration, key []byte) error {
	unlock, err := b.Store.deploymentLock()
	if err != nil {
		return err
	}
	defer unlock()
	state, err := b.Store.Load()
	if err != nil {
		return err
	}
	enabled := false
	for _, choice := range state.Config.Tools {
		if choice.ID == "github" && choice.Enabled {
			enabled = true
		}
	}
	if !enabled {
		return fmt.Errorf("enable the installed GitHub tool before setup")
	}
	reservations, err := b.Store.Deployments()
	if err != nil {
		return err
	}
	if len(reservations) != 0 {
		return fmt.Errorf("stop owned full services before importing provider configuration")
	}
	resources, err := b.Store.FullResources()
	if err != nil {
		return err
	}
	worker, err := resources.ServiceProgram("runtime-core", "loki")
	if err != nil {
		return err
	}
	image, err := resources.Image("runtime-core", "service")
	if err != nil {
		return err
	}
	if err := b.pinnedImage(ctx, image); err != nil {
		return err
	}
	volume := FullMount{Kind: "volume", Source: b.Store.FullOwner() + "-data-providers-github", Target: "/var/lib/loki/providers/github"}
	if err := b.ownedVolume(ctx, volume); err != nil {
		return err
	}
	r, err := b.Store.ReserveDeployment(resources.Plan)
	if err != nil {
		return err
	}
	args := []string{"container", "create", "--interactive", "--name", b.containerName(r, "prepare"), "--network", "none", "--read-only", "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--security-opt", "no-new-privileges:true", "--user", "0:0", "--memory", "134217728", "--pids-limit", "32", "--restart", "no", "--entrypoint", "/bin/sh"}
	for _, label := range b.labels(r, "prepare") {
		args = append(args, "--label", label)
	}
	mounts := []FullMount{volume, {Kind: "bind", Source: b.Store.ControlDirectory(), Target: "/etc/loki/activation", ReadOnly: true}}
	for _, program := range resources.Plan.Programs {
		if program.Module == "runtime-core" {
			mounts = append(mounts, fullModuleMount(program))
		}
	}
	for _, mount := range mounts {
		argument, err := dockerMountArgument(mount)
		if err != nil {
			return err
		}
		args = append(args, "--mount", argument)
	}
	// Module payload paths are local but can contain spaces or quotes. The
	// executable is passed as a shell positional argument, never interpolated.
	script := "test ! -L /var/lib/loki/providers/github; install -d -o 0 -g 0 -m 0700 /var/lib/loki/providers/github; exec \"$1\" full-provider-import --state /var/lib/loki/providers/github --tools-config /etc/loki/activation/state.json"
	args = append(args, image, "-ec", script, "loki-provider-import", worker)
	if _, err := b.command(ctx, "creating protected GitHub import", nil, args...); err != nil {
		return err
	}
	data, err := json.Marshal(struct {
		Config     []byte `json:"config"`
		PrivateKey []byte `json:"private_key"`
	}{configuration, key})
	if err != nil {
		return err
	}
	defer clear(data)
	if _, err := b.command(ctx, "saving GitHub provider credentials", bytes.NewReader(data), "container", "start", "--attach", "--interactive", b.containerName(r, "prepare")); err != nil {
		return err
	}
	exitCode, err := b.command(ctx, "checking GitHub import result", nil, "container", "inspect", "--format", "{{.State.ExitCode}}", b.containerName(r, "prepare"))
	if err != nil {
		return err
	}
	if !bytes.Equal(bytes.TrimSpace(exitCode), []byte("0")) {
		return fmt.Errorf("GitHub provider credential import failed; retry setup")
	}
	if err := b.Stop(ctx, r); err != nil {
		return err
	}
	return b.Store.ReleaseDeployment(ctx, r.ID, b)
}

var _ IntegrationBackend = (*DockerFullBackend)(nil)
