package sandbox

import (
	"strings"
	"testing"
)

func TestResourceIdentityAndOwnershipLabels(t *testing.T) {
	id := strings.Repeat("a", 32)
	policy := strings.Repeat("b", 64)
	resource, err := NewResource(id, policy)
	if err != nil {
		t.Fatal(err)
	}
	if !resource.Valid() || resource.JobID() != id || resource.PolicySHA256() != policy ||
		resource.Name() != "loki-job-"+id {
		t.Fatalf("resource = %#v", resource)
	}
	labels := resource.labels()
	if labels[resourceOwnerLabel] != resourceOwnerValue ||
		labels[resourceJobLabel] != id ||
		labels[resourcePolicyLabel] != policy ||
		!resource.owns(labels) {
		t.Fatalf("labels = %#v", labels)
	}
	labels[resourceJobLabel] = strings.Repeat("c", 32)
	if resource.owns(labels) {
		t.Fatal("resource accepted mismatched ownership labels")
	}
	if _, err := NewResource("bad", policy); err == nil {
		t.Fatal("invalid job ID was accepted")
	}
	if _, err := NewResource(id, "bad"); err == nil {
		t.Fatal("invalid policy digest was accepted")
	}
	if (Resource{}).Valid() || (Resource{}).Name() != "" {
		t.Fatal("zero resource became valid")
	}
}
