package identity

import "testing"

func TestUnixResolverClassifiesTrustedPeerFacts(t *testing.T) {
	resolver := UnixResolver{AgentUID: 1000}
	for _, test := range []struct {
		name     string
		pid      int32
		uid, gid uint32
		want     Kind
	}{
		{"root", 1, 0, 0, HostAdministrator},
		{"agent", 42, 1000, 2000, Agent},
		{"agent-other-pid", 9999, 1000, 2000, Agent},
		{"other", 42, 1001, 2000, Unknown},
	} {
		t.Run(test.name, func(t *testing.T) {
			got := resolver.Resolve(test.pid, test.uid, test.gid)
			if got.Kind != test.want || got.PID != test.pid || got.UID != test.uid || got.GID != test.gid {
				t.Fatalf("principal = %#v", got)
			}
		})
	}
}
