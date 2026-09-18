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

func TestFixedUIDResolverClassifiesOneRoleAndRoot(t *testing.T) {
	for _, kind := range []Kind{Agent, Executor} {
		resolver := FixedUIDResolver{UID: 1001, Kind: kind}
		if !resolver.Valid() {
			t.Fatalf("resolver for kind %d is invalid", kind)
		}
		for _, test := range []struct {
			name string
			uid  uint32
			want Kind
		}{
			{"root", 0, HostAdministrator},
			{"role", 1001, kind},
			{"other", 1002, Unknown},
		} {
			t.Run(test.name, func(t *testing.T) {
				got := resolver.Resolve(42, test.uid, 2000)
				if got.Kind != test.want || got.PID != 42 || got.UID != test.uid || got.GID != 2000 {
					t.Fatalf("principal = %#v", got)
				}
			})
		}
	}
}

func TestFixedUIDResolverInvalidConfigurationFailsClosed(t *testing.T) {
	for _, resolver := range []FixedUIDResolver{
		{UID: 0, Kind: Executor},
		{UID: 1001, Kind: Unknown},
		{UID: 1001, Kind: HostAdministrator},
	} {
		if resolver.Valid() {
			t.Fatalf("invalid resolver was accepted: %#v", resolver)
		}
		for _, uid := range []uint32{0, 1001, 1002} {
			if got := resolver.Resolve(42, uid, 2000); got.Kind != Unknown {
				t.Fatalf("invalid resolver granted kind %d to uid %d", got.Kind, uid)
			}
		}
	}
}
