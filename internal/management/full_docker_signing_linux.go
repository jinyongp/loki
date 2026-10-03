package management

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"slices"

	"loki/internal/tools"
)

func (b *DockerFullBackend) SetupGitSigning(ctx context.Context, name, email string, key []byte) (json.RawMessage, error) {
	unlock, err := b.Store.deploymentLock()
	if err != nil {
		return nil, err
	}
	defer unlock()
	state, err := b.Store.Load()
	if err != nil {
		return nil, err
	}
	enabled := false
	for _, choice := range state.Config.Tools {
		if choice.ID == "git" && choice.Enabled && slices.Contains(choice.Capabilities, "signing") {
			enabled = true
		}
	}
	if !enabled {
		return nil, fmt.Errorf("enable Git's signing capability before setup")
	}
	reservations, err := b.Store.Deployments()
	if err != nil {
		return nil, err
	}
	if len(reservations) != 0 {
		return nil, fmt.Errorf("stop owned services before configuring Git signing")
	}
	resources, err := b.Store.FullResources()
	if err != nil {
		return nil, err
	}
	worker, err := resources.ServiceProgram("runtime-core", "loki")
	if err != nil {
		return nil, err
	}
	keygen, err := resources.ServiceProgram("git", "ssh-keygen")
	if err != nil {
		return nil, err
	}
	image, err := resources.Image("git", "git-workload")
	if err != nil {
		return nil, err
	}
	if err := b.pinnedImage(ctx, image); err != nil {
		return nil, err
	}
	volume := FullMount{Kind: "volume", Source: b.Store.FullOwner() + "-data-git-signing", Target: "/var/lib/loki/git/signing"}
	if err := b.ownedVolume(ctx, volume); err != nil {
		return nil, err
	}
	r, err := b.Store.ReserveDeployment(resources.Plan)
	if err != nil {
		return nil, err
	}
	args := []string{"container", "create", "--interactive", "--name", b.containerName(r, "prepare"), "--network", "none", "--read-only", "--cap-drop", "ALL", "--cap-add", "DAC_OVERRIDE", "--security-opt", "no-new-privileges:true", "--user", "0:0", "--memory", "134217728", "--pids-limit", "32", "--restart", "no", "--entrypoint", "/bin/sh"}
	for _, label := range b.labels(r, "prepare") {
		args = append(args, "--label", label)
	}
	mounts := []FullMount{volume, {Kind: "bind", Source: b.Store.ControlDirectory(), Target: "/etc/loki/activation", ReadOnly: true}}
	for _, program := range resources.Plan.Programs {
		if program.Module == "runtime-core" || program.Module == "git" {
			mounts = append(mounts, fullModuleMount(program))
		}
	}
	for _, mount := range mounts {
		argument, err := dockerMountArgument(mount)
		if err != nil {
			return nil, err
		}
		args = append(args, "--mount", argument)
	}
	script := "test ! -L /var/lib/loki/git/signing; install -d -o 0 -g 0 -m 0700 /var/lib/loki/git/signing; exec \"$1\" full-signing-setup --keygen \"$2\""
	args = append(args, image, "-ec", script, "loki-git-signing-setup", worker, keygen)
	if _, err := b.command(ctx, "creating protected Git signing setup", nil, args...); err != nil {
		return nil, err
	}
	data, err := json.Marshal(struct {
		Name       string `json:"name"`
		Email      string `json:"email"`
		PrivateKey []byte `json:"private_key"`
	}{name, email, key})
	if err != nil {
		return nil, err
	}
	defer clear(data)
	result, err := b.command(ctx, "configuring Git-owned signing", bytes.NewReader(data), "container", "start", "--attach", "--interactive", b.containerName(r, "prepare"))
	if err != nil {
		return nil, err
	}
	exitCode, err := b.command(ctx, "checking Git signing setup result", nil, "container", "inspect", "--format", "{{.State.ExitCode}}", b.containerName(r, "prepare"))
	if err != nil {
		return nil, err
	}
	if !bytes.Equal(bytes.TrimSpace(exitCode), []byte("0")) {
		return nil, fmt.Errorf("Git signing setup failed; retry integrations setup git")
	}
	if !json.Valid(result) || len(result) > tools.MaxManifestBytes {
		return nil, fmt.Errorf("invalid protected Git signing public response")
	}
	if err := b.Stop(ctx, r); err != nil {
		return nil, err
	}
	if err := b.Store.ReleaseDeployment(ctx, r.ID, b); err != nil {
		return nil, err
	}
	return json.RawMessage(result), nil
}
