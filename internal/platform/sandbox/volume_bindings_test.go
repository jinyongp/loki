package sandbox

import (
	"path/filepath"
	"strings"
	"testing"
)

func TestOwnedVolumePolicyBindsOnlyDeclaredPathsAndRetainsReadOnlyInput(t *testing.T) {
	options := validPolicyOptions()
	options.DeploymentOwner = "loki-tools-" + strings.Repeat("a", 16)
	options.DeploymentID = "deployment-" + strings.Repeat("b", 32)
	options.InputDirectory = "/srv/launcher/run-inputs"
	options.Volumes = []VolumeBinding{{Path: options.Workspace, Name: options.DeploymentOwner + "-data-workspace"}, {Path: "/srv/launcher", Name: options.DeploymentOwner + "-data-execution-launcher"}}
	policy, err := NewPolicy(options)
	if err != nil {
		t.Fatal(err)
	}
	options.Volumes[0].Name = "foreign-volume"
	spec := validWorkloadSpec()
	spec.InputPath = filepath.Join(options.InputDirectory, spec.ID+".stdin")
	plan, err := policy.Plan(spec)
	if err != nil {
		t.Fatal(err)
	}
	mounts := plan.create.HostConfig.Mounts
	if mounts[0].Type != "volume" || mounts[0].Source != policy.deploymentOwner+"-data-workspace" || mounts[0].ReadOnly || mounts[0].BindOptions != nil || mounts[0].VolumeOptions == nil || !mounts[0].VolumeOptions.NoCopy {
		t.Fatal("workspace did not use its exact owned volume", mounts[0])
	}
	input := mounts[len(mounts)-1]
	if input.Type != "volume" || !input.ReadOnly || input.VolumeOptions == nil || input.VolumeOptions.Subpath != "run-inputs/"+spec.ID+".stdin" {
		t.Fatal("input mount changed its read-only boundary or escaped its volume", input)
	}
	changed := validPolicyOptions()
	changed.DeploymentOwner = policy.deploymentOwner
	changed.DeploymentID = policy.deploymentID
	changed.InputDirectory = options.InputDirectory
	changed.Volumes = []VolumeBinding{{Path: changed.Workspace, Name: changed.DeploymentOwner + "-data-other"}}
	other, err := NewPolicy(changed)
	if err != nil || other.sandboxSHA256 == policy.sandboxSHA256 {
		t.Fatal("volume authority absent from policy identity", err)
	}
	for _, binding := range []VolumeBinding{{Path: "/etc", Name: policy.deploymentOwner + "-data-workspace"}, {Path: options.Workspace, Name: "foreign-volume"}, {Path: options.Workspace + "/../outside", Name: policy.deploymentOwner + "-data-workspace"}} {
		changed.Volumes = []VolumeBinding{binding}
		if _, err := NewPolicy(changed); err == nil {
			t.Fatal("untrusted or unrelated volume binding accepted", binding)
		}
	}
}
