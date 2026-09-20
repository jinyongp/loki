package sandbox

import (
	"strings"
	"testing"
)

func TestResourceIdentityAndOwnershipLabels(t *testing.T) {
	id := strings.Repeat("a", 32)
	policy := strings.Repeat("b", 64)
	sandboxPolicy := strings.Repeat("c", 64)
	resource, err := NewResource(id, policy, sandboxPolicy)
	if err != nil {
		t.Fatal(err)
	}
	if !resource.Valid() || resource.JobID() != id || resource.PolicySHA256() != policy ||
		resource.SandboxSHA256() != sandboxPolicy || resource.Name() != "loki-job-"+id ||
		resource.GatewayName() != "loki-job-gateway-"+id ||
		resource.InternalNetworkName() != "loki-job-net-"+id ||
		resource.OutboundNetworkName() != "loki-job-egress-"+id {
		t.Fatalf("resource = %#v", resource)
	}
	labels := resource.labels()
	if labels[resourceOwnerLabel] != resourceOwnerValue ||
		labels[resourceJobLabel] != id ||
		labels[resourcePolicyLabel] != policy ||
		labels[resourceSandboxLabel] != sandboxPolicy ||
		labels[resourceComponentLabel] != resourceComponentWorkload ||
		!resource.owns(labels) {
		t.Fatalf("labels = %#v", labels)
	}
	gateway := resource.labelsFor(resourceComponentGateway)
	if !resource.ownsComponent(gateway, resourceComponentGateway) ||
		resource.ownsComponent(gateway, resourceComponentWorkload) {
		t.Fatalf("gateway labels = %#v", gateway)
	}
	labels[resourceJobLabel] = strings.Repeat("d", 32)
	if resource.owns(labels) {
		t.Fatal("resource accepted mismatched ownership labels")
	}
	if _, err := NewResource("bad", policy, sandboxPolicy); err == nil {
		t.Fatal("invalid job ID was accepted")
	}
	if _, err := NewResource(id, "bad", sandboxPolicy); err == nil {
		t.Fatal("invalid policy digest was accepted")
	}
	if _, err := NewResource(id, policy, "bad"); err == nil {
		t.Fatal("invalid sandbox policy digest was accepted")
	}
	if (Resource{}).Valid() || (Resource{}).Name() != "" {
		t.Fatal("zero resource became valid")
	}
}
