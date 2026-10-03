package sandbox

import (
	"strings"
	"testing"
)

func TestDeployedResourcesRequireExactParentForEveryComponent(t *testing.T) {
	job, policy, digest := strings.Repeat("a", 32), strings.Repeat("b", 64), strings.Repeat("c", 64)
	owner, id := "loki-v02-"+strings.Repeat("d", 16), "deployment-"+strings.Repeat("e", 32)
	resource, err := NewDeployedResource(job, policy, digest, owner, id)
	if err != nil {
		t.Fatal(err)
	}
	unscoped, err := NewResource(job, policy, digest)
	if err != nil {
		t.Fatal(err)
	}
	for _, component := range []string{resourceComponentWorkload, resourceComponentGateway, resourceComponentPublisher, resourceComponentInternalNetwork, resourceComponentOutboundNetwork} {
		labels := resource.labelsFor(component)
		if !resource.ownsComponent(labels, component) || unscoped.ownsComponent(labels, component) {
			t.Fatal("deployed scope was lost")
		}
		labels["io.loki.full.deployment"] = "deployment-" + strings.Repeat("f", 32)
		if resource.ownsComponent(labels, component) {
			t.Fatal("foreign deployment accepted")
		}
	}
	if _, err := NewDeployedResource(job, policy, digest, owner, ""); err == nil {
		t.Fatal("partial parent identity accepted")
	}
}

func TestDeploymentIdentityChangesSandboxFingerprint(t *testing.T) {
	options := validPolicyOptions()
	options.DeploymentOwner = "loki-v02-" + strings.Repeat("a", 16)
	options.DeploymentID = "deployment-" + strings.Repeat("b", 32)
	first, err := NewPolicy(options)
	if err != nil {
		t.Fatal(err)
	}
	firstPlan, err := first.Plan(WorkloadSpec{ID: strings.Repeat("c", 32), CWD: ".", Argv: []string{"/usr/bin/true"}})
	if err != nil {
		t.Fatal(err)
	}
	options.DeploymentID = "deployment-" + strings.Repeat("d", 32)
	second, err := NewPolicy(options)
	if err != nil {
		t.Fatal(err)
	}
	secondPlan, err := second.Plan(WorkloadSpec{ID: strings.Repeat("c", 32), CWD: ".", Argv: []string{"/usr/bin/true"}})
	if err != nil {
		t.Fatal(err)
	}
	if firstPlan.SandboxSHA256() == secondPlan.SandboxSHA256() {
		t.Fatal("deployment scopes share an ownership fingerprint")
	}
}
