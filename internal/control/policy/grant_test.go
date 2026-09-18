package policy

import (
	"testing"

	"loki/internal/control/identity"
)

func TestGrantValuesRemainStable(t *testing.T) {
	if Agent != 1 || HostAdministration != 2 || WorkloadLaunch != 3 {
		t.Fatalf("grant values changed: agent=%d host=%d workload=%d", Agent, HostAdministration, WorkloadLaunch)
	}
}

func TestAllowsReviewedGrants(t *testing.T) {
	for _, test := range []struct {
		name      string
		principal identity.Principal
		grant     Grant
		want      bool
	}{
		{"admin-agent", identity.Principal{Kind: identity.HostAdministrator}, Agent, true},
		{"admin-host", identity.Principal{Kind: identity.HostAdministrator}, HostAdministration, true},
		{"admin-workload", identity.Principal{Kind: identity.HostAdministrator}, WorkloadLaunch, true},
		{"agent-agent", identity.Principal{Kind: identity.Agent}, Agent, true},
		{"agent-host", identity.Principal{Kind: identity.Agent}, HostAdministration, false},
		{"agent-workload", identity.Principal{Kind: identity.Agent}, WorkloadLaunch, false},
		{"executor-agent", identity.Principal{Kind: identity.Executor}, Agent, false},
		{"executor-host", identity.Principal{Kind: identity.Executor}, HostAdministration, false},
		{"executor-workload", identity.Principal{Kind: identity.Executor}, WorkloadLaunch, true},
		{"unknown-agent", identity.Principal{Kind: identity.Unknown}, Agent, false},
		{"unknown-host", identity.Principal{Kind: identity.Unknown}, HostAdministration, false},
		{"unknown-workload", identity.Principal{Kind: identity.Unknown}, WorkloadLaunch, false},
		{"unset-grant", identity.Principal{Kind: identity.HostAdministrator}, Grant(0), false},
		{"unknown-grant", identity.Principal{Kind: identity.HostAdministrator}, Grant(255), false},
	} {
		t.Run(test.name, func(t *testing.T) {
			if got := Allows(test.principal, test.grant); got != test.want {
				t.Fatalf("Allows(%#v, %d) = %v", test.principal, test.grant, got)
			}
		})
	}
}
