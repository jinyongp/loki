package policy

import (
	"loki/internal/control/identity"
	"testing"
)

func TestPortInspectorCanOnlyInspect(t *testing.T) {
	principal := identity.Principal{Kind: identity.PortInspector}
	for _, grant := range []Grant{Agent, HostAdministration, WorkloadLaunch, PortInspection, 0, 255} {
		if Allows(principal, grant) != (grant == PortInspection) {
			t.Fatalf("unexpected inspector authority for grant %d", grant)
		}
	}
	if !Allows(identity.Principal{Kind: identity.Agent}, PortInspection) {
		t.Fatal("agent lost port inspection")
	}
}
