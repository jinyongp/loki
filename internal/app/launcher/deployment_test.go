package launcher

import (
	"strings"
	"testing"

	"loki/modules/execution/jobs"
)

func TestRecoveredJobKeepsItsOriginalDeploymentOwner(t *testing.T) {
	owner, id := "loki-tools-"+strings.Repeat("a", 16), "deployment-"+strings.Repeat("b", 32)
	policy, sandbox := strings.Repeat("c", 64), strings.Repeat("d", 64)
	record := jobs.Record{ID: strings.Repeat("e", 32), BackendRef: "oci-deployment:" + owner + ":" + id + ":" + policy + ":" + sandbox}
	resource, err := resourceFromRecord(record)
	if err != nil {
		t.Fatal(err)
	}
	gotOwner, gotID := resource.Deployment()
	if gotOwner != owner || gotID != id || resource.PolicySHA256() != policy || resource.SandboxSHA256() != sandbox {
		t.Fatal("recovery lost original resource identity")
	}
	record.BackendRef = "oci-deployment:" + owner + ":" + id + ":" + policy
	if _, err := resourceFromRecord(record); err == nil {
		t.Fatal("partial deployed reference accepted")
	}
}
