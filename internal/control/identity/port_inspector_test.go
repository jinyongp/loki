package identity

import "testing"

func TestPortInspectorIsDistinctFromAgentAndAdministrator(t *testing.T) {
	uid := uint32(10005)
	r := UnixResolver{AgentUID: 10000, PortInspectorUID: &uid}
	for _, test := range []struct {
		uid  uint32
		kind Kind
	}{{0, HostAdministrator}, {10000, Agent}, {10005, PortInspector}, {10002, Unknown}, {10003, Unknown}} {
		if principal := r.Resolve(1, test.uid, test.uid); principal.Kind != test.kind {
			t.Fatalf("uid %d received %d", test.uid, principal.Kind)
		}
	}
	uid = 0
	if r.Resolve(1, 10005, 10005).Kind != Unknown {
		t.Fatal("invalid inspector UID granted access")
	}
}
