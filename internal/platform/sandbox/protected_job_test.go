package sandbox

import (
	"strings"
	"testing"
)

func TestPreparedDomainChecksConfiguredNetworkUntilStart(t *testing.T) {
	workload, gateway, internal, outbound := strings.Repeat("1", 64), strings.Repeat("2", 64), strings.Repeat("3", 64), strings.Repeat("4", 64)
	reference, _ := parseDomainReference(domainInstanceReference(workload, gateway, "", internal, outbound))
	snapshot := domainSnapshot{
		workload: inspectedResource{id: workload, state: ResourceState{Exists: true}, configuredNetwork: "owned-internal"},
		gateway:  inspectedResource{id: gateway, state: ResourceState{Exists: true, Running: true}, networkIDs: map[string]bool{internal: true, outbound: true}},
		internal: inspectedNetwork{name: "owned-internal", id: internal, exists: true, internal: true, members: map[string]bool{gateway: true}},
		outbound: inspectedNetwork{id: outbound, exists: true, members: map[string]bool{gateway: true}},
	}
	if reference.preparedCompletenessError(snapshot) != nil {
		t.Fatal("stopped container with exact configured network rejected")
	}
	if reference.completenessError(snapshot) == nil {
		t.Fatal("ordinary running-domain checks weakened")
	}
	wrong := snapshot
	wrong.workload.configuredNetwork = "foreign-network"
	if reference.preparedCompletenessError(wrong) == nil {
		t.Fatal("foreign configured network accepted")
	}
	wrong = snapshot
	wrong.workload.state.Running = true
	if reference.preparedCompletenessError(wrong) == nil {
		t.Fatal("running container accepted without actual attachment")
	}
	snapshot.workload.state.Running = true
	snapshot.workload.networkIDs = map[string]bool{internal: true}
	if reference.completenessError(snapshot) != nil {
		t.Fatal("actual network attachment rejected after start")
	}
	snapshot.workload.networkIDs = map[string]bool{outbound: true}
	if reference.completenessError(snapshot) == nil {
		t.Fatal("direct outbound attachment accepted")
	}
}
